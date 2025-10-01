// Package mdns implements a multicast DNS (mDNS) resolver and browser
// for service discovery in local networks. It supports both IPv4 and IPv6
// transport and provides interfaces for browsing services and looking up
// specific service instances.
//
// The package follows the DNS-Based Service Discovery (DNS-SD) specification
// and is compatible with standard mDNS implementations like Avahi and Bonjour.
package mdns

import (
	"context"
	"fmt"
	"log"
	"net"
	"runtime"
	"strings"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// IPType specifies the IP traffic type the client listens for.
// Note that mDNS packets may contain records of multiple types regardless
// of the transport protocol (e.g., IPv4 packets can contain AAAA records).
type IPType uint8

// IPType options for configuring client network traffic preferences.
const (
	IPv4        IPType = 0x01
	IPv6        IPType = 0x02
	IPv4AndIPv6 IPType = (IPv4 | IPv6) // Default option
)

// clientOpts holds configuration options for the mDNS client.
type clientOpts struct {
	listenOn IPType
	ifaces   []net.Interface
}

// ClientOption defines a function type for configuring client options.
type ClientOption func(*clientOpts)

// SelectIPTraffic configures the type of IP packets (IPv4, IPv6, or both)
// that the client will listen for. This selection applies to the transport
// layer but does not filter the DNS record types contained in the packets.
func SelectIPTraffic(t IPType) ClientOption {
	return func(o *clientOpts) {
		o.listenOn = t
	}
}

// SelectIfaces specifies which network interfaces should be used for
// mDNS operations. If not provided, all multicast-capable interfaces
// will be used automatically.
func SelectIfaces(ifaces []net.Interface) ClientOption {
	return func(o *clientOpts) {
		o.ifaces = ifaces
	}
}

// Resolver provides the main interface for service discovery operations,
// including browsing for services and looking up specific service instances.
type Resolver struct {
	c *client
}

// NewResolver creates a new mDNS resolver and joins the required UDP
// multicast groups to listen for mDNS messages on the specified interfaces.
//
// Returns an error if multicast group joining fails on all interfaces.
func NewResolver(options ...ClientOption) (*Resolver, error) {
	// Apply default configuration and process supplied options
	var conf = clientOpts{
		listenOn: IPv4AndIPv6,
	}
	for _, o := range options {
		if o != nil {
			o(&conf)
		}
	}

	c, err := newClient(conf)
	if err != nil {
		return nil, err
	}
	return &Resolver{
		c: c,
	}, nil
}

// Browse discovers all services of the specified type in the given domain.
// The handler function is called for each service instance found or removed.
//
// The browsing operation continues until the context is cancelled or an
// error occurs. Multiple service instances may be reported concurrently.
func (r *Resolver) Browse(ctx context.Context, service, domain string, handler ServiceHandler) error {
	params := defaultParams(service, handler)
	if domain != "" {
		params.Domain = domain
	}
	params.handler = handler
	params.isBrowsing = true

	ctx, cancel := context.WithCancel(ctx)
	go r.c.mainloop(ctx, params)

	err := r.c.query(params)
	if err != nil {
		cancel()
		return err
	}

	// Start periodic querying if initial query succeeded
	go func() {
		if err := r.c.periodicQuery(ctx, params); err != nil {
			cancel()
		}
	}()

	return nil
}

// Lookup searches for a specific service instance by name and type in the
// given domain. The handler is called when the instance is found or updated.
//
// This is more specific than Browse and targets a single service instance.
func (r *Resolver) Lookup(ctx context.Context, instance, service, domain string, handler ServiceHandler) error {
	params := defaultParams(service, handler)
	params.Instance = instance
	if domain != "" {
		params.Domain = domain
	}
	params.handler = handler

	ctx, cancel := context.WithCancel(ctx)
	go r.c.mainloop(ctx, params)

	err := r.c.query(params)
	if err != nil {
		cancel()
		return err
	}

	// Start periodic querying if initial query succeeded
	go func() {
		if err := r.c.periodicQuery(ctx, params); err != nil {
			cancel()
		}
	}()

	return nil
}

// defaultParams creates a default set of lookup parameters for the given service.
func defaultParams(service string, handler ServiceHandler) *lookupParams {
	return newLookupParams("", service, "local", false, handler)
}

// Client manages the network connections and message processing for mDNS operations.
// It handles both IPv4 and IPv6 multicast communication.
type client struct {
	ipv4conn *ipv4.PacketConn
	ipv6conn *ipv6.PacketConn
	ifaces   []net.Interface
}

// newClient creates and configures a new mDNS client with the specified options.
// It joins the required multicast groups on all selected interfaces.
func newClient(opts clientOpts) (*client, error) {
	ifaces := opts.ifaces
	if len(ifaces) == 0 {
		ifaces = listMulticastInterfaces()
	}

	// Initialize IPv4 connection if requested
	var ipv4conn *ipv4.PacketConn
	if (opts.listenOn & IPv4) > 0 {
		var err error
		ipv4conn, err = joinUdp4Multicast(ifaces)
		if err != nil {
			return nil, err
		}
	}

	// Initialize IPv6 connection if requested
	var ipv6conn *ipv6.PacketConn
	if (opts.listenOn & IPv6) > 0 {
		var err error
		ipv6conn, err = joinUdp6Multicast(ifaces)
		if err != nil {
			return nil, err
		}
	}

	return &client{
		ipv4conn: ipv4conn,
		ipv6conn: ipv6conn,
		ifaces:   ifaces,
	}, nil
}

// Constants representing DNS record types as bit flags for tracking
// which records have been received for a service entry.
const (
	flagPTR  = 1 << iota // PTR record received
	flagSRV              // SRV record received
	flagTXT              // TXT record received
	flagA                // A record received (IPv4 address)
	flagAAAA             // AAAA record received (IPv6 address)
)

// receivedMsg wraps a DNS message with the interface index where it was received.
type receivedMsg struct {
	msg     *dns.Msg
	ifIndex int
}

// mainloop is the central message processing loop that handles incoming mDNS
// responses, parses DNS records, and manages service entry lifecycle events.
//
// It runs until the context is cancelled and coordinates multiple goroutines
// for receiving and processing messages.
func (c *client) mainloop(ctx context.Context, params *lookupParams) {
	// Start message receivers for each active connection
	msgCh := make(chan *receivedMsg, 32)
	if c.ipv4conn != nil {
		go c.recv(ctx, c.ipv4conn, msgCh)
	}
	if c.ipv6conn != nil {
		go c.recv(ctx, c.ipv6conn, msgCh)
	}

	// Track service entries that have been sent to the handler
	var entries, sentEntries map[string]*ServiceEntry
	sentEntries = make(map[string]*ServiceEntry)

	for {
		select {
		case <-ctx.Done():
			c.shutdown()
			return
		case rmsg := <-msgCh:
			msg := rmsg.msg
			ifIndex := rmsg.ifIndex

			entries = make(map[string]*ServiceEntry)

			// Process all DNS sections (Answer, Authority, Additional)
			sections := append(msg.Answer, msg.Ns...)
			sections = append(sections, msg.Extra...)

			// First pass: Process PTR, SRV, and TXT records to build service entries
			for _, answer := range sections {
				switch rr := answer.(type) {
				case *dns.PTR:
					if params.ServiceName() != rr.Hdr.Name {
						continue
					}
					if params.ServiceInstanceName() != "" && params.ServiceInstanceName() != rr.Ptr {
						continue
					}

					key := rr.Ptr
					if _, ok := entries[key]; !ok {
						instance := trimDot(strings.ReplaceAll(rr.Ptr, rr.Hdr.Name, "", ))
						instance = dnsUnescape(instance)
						entries[key] = NewServiceEntry(instance, params.Service, params.Domain)
					}
					entries[key].TTL = rr.Hdr.Ttl
					entries[key].Flags |= flagPTR

				case *dns.SRV:
					if params.ServiceInstanceName() != "" && params.ServiceInstanceName() != rr.Hdr.Name {
						continue
					} else if !strings.HasSuffix(rr.Hdr.Name, params.ServiceName()) {
						continue
					}

					key := rr.Hdr.Name
					if _, ok := entries[key]; !ok {
						instance := trimDot(strings.Replace(rr.Hdr.Name, params.ServiceName(), "", 1))
						instance = dnsUnescape(instance)
						entries[key] = NewServiceEntry(instance, params.Service, params.Domain)
					}
					entries[key].HostName = rr.Target
					entries[key].Port = int(rr.Port)
					entries[key].TTL = rr.Hdr.Ttl
					entries[key].Flags |= flagSRV

				case *dns.TXT:
					if params.ServiceInstanceName() != "" && params.ServiceInstanceName() != rr.Hdr.Name {
						continue
					} else if !strings.HasSuffix(rr.Hdr.Name, params.ServiceName()) {
						continue
					}

					key := rr.Hdr.Name
					if _, ok := entries[key]; !ok {
						instance := trimDot(strings.Replace(rr.Hdr.Name, params.ServiceName(), "", 1))
						instance = dnsUnescape(instance)
						entries[key] = NewServiceEntry(instance, params.Service, params.Domain)
					}
					entries[key].Text = rr.Txt
					entries[key].TTL = rr.Hdr.Ttl
					entries[key].Flags |= flagTXT
				}
			}

			// Second pass: Associate IP addresses with service entries
			for _, answer := range sections {
				switch rr := answer.(type) {
				case *dns.A:
					for k, e := range entries {
						if e.HostName == rr.Hdr.Name {
							entries[k].AddrIPv4 = append(entries[k].AddrIPv4, rr.A)
							entries[k].Flags |= flagA
						}
					}
				case *dns.AAAA:
					for k, e := range entries {
						if e.HostName == rr.Hdr.Name {
							entries[k].AddrIPv6 = append(entries[k].AddrIPv6, rr.AAAA)
							entries[k].Flags |= flagAAAA
						}
					}
				}
			}

			// Generate Add/Rmv events based on collected entries
			if len(entries) > 0 {
				for k, e := range entries {
					// Handle service removal (TTL expiration)
					if e.TTL == 0 {
						if old, ok := sentEntries[k]; ok {
							old.Event = "Rmv"
							if ifIndex != 0 {
								old.IfIndex = ifIndex
							}
							if params.handler != nil {
								params.handler(old)
							}
							delete(sentEntries, k)
						}
						delete(entries, k)
						continue
					}

					// Skip entries that have already been sent
					if _, ok := sentEntries[k]; ok {
						continue
					}

					// For DNS-SD queries, PTR records alone are sufficient
					// For service instance lookups, require at least one IP address
					if params.ServiceRecord.ServiceTypeName() != params.ServiceRecord.ServiceName() {
						if len(e.AddrIPv4) == 0 && len(e.AddrIPv6) == 0 {
							continue
						}
					}

					// Report new service discovery
					e.Event = "Add"
					if ifIndex != 0 {
						e.IfIndex = ifIndex
					}

					if params.handler != nil {
						params.handler(e)
					}

					sentEntries[k] = e
					if !params.isBrowsing {
						params.disableProbing()
					}
				}
			}
		}
	}
}

// shutdown closes all network connections and stops all goroutines.
func (c *client) shutdown() {
	if c.ipv4conn != nil {
		c.ipv4conn.Close()
	}
	if c.ipv6conn != nil {
		c.ipv6conn.Close()
	}
}

// recv continuously reads packets from the connection, decodes them into
// DNS messages, and sends them to the message channel for processing.
//
// The function handles both IPv4 and IPv6 connections transparently and
// includes the receiving interface index with each message.
func (c *client) recv(ctx context.Context, l interface{}, msgCh chan *receivedMsg) {
	var readFrom func([]byte) (n int, ifIndex int, src net.Addr, err error)

	// Configure the appropriate read function based on connection type
	switch pConn := l.(type) {
	case *ipv6.PacketConn:
		readFrom = func(b []byte) (n int, ifIndex int, src net.Addr, err error) {
			n, cm, src, err := pConn.ReadFrom(b)
			if cm != nil {
				ifIndex = cm.IfIndex
			} else {
				ifIndex = 0
			}
			return n, ifIndex, src, err
		}
	case *ipv4.PacketConn:
		readFrom = func(b []byte) (n int, ifIndex int, src net.Addr, err error) {
			n, cm, src, err := pConn.ReadFrom(b)
			if cm != nil {
				ifIndex = cm.IfIndex
			} else {
				ifIndex = 0
			}
			return n, ifIndex, src, err
		}

	default:
		return
	}

	buf := make([]byte, 65536)
	var fatalErr error

	for {
		if ctx.Err() != nil || fatalErr != nil {
			return
		}

		n, ifIdx, _, err := readFrom(buf)
		if err != nil {
			fatalErr = err
			continue
		}

		msg := new(dns.Msg)
		if err := msg.Unpack(buf[:n]); err != nil {
			// Silently skip malformed packets
			continue
		}

		select {
		case msgCh <- &receivedMsg{msg: msg, ifIndex: ifIdx}:
			// Message submitted for processing
		case <-ctx.Done():
			return
		}
	}
}

// periodicQuery sends repeated mDNS queries with exponential backoff until
// a valid response is received or the operation is cancelled.
//
// This ensures robust service discovery even in lossy network conditions.
func (c *client) periodicQuery(ctx context.Context, params *lookupParams) error {
	// Configure backoff with appropriate parameters for mDNS
	bo := NewBackoff(60*time.Second, 4*time.Second) // max=60s, interval=4s
	bo.SetDecay(10 * time.Second)

	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		wait := bo.Duration()

		if timer == nil {
			timer = time.NewTimer(wait)
		} else {
			timer.Reset(wait)
		}

		select {
		case <-timer.C:
			// Timer expired, send next query
		case <-params.stopProbing:
			// Probing disabled externally
			return nil
		case <-ctx.Done():
			// Context cancelled
			return ctx.Err()
		}

		if err := c.query(params); err != nil {
			return err
		}
	}
}

// query constructs and sends an mDNS query based on the lookup parameters.
// The query type varies depending on whether we're browsing or looking up
// a specific instance.
func (c *client) query(params *lookupParams) error {
	var serviceName, serviceInstanceName string
	serviceName = fmt.Sprintf("%s.%s.", trimDot(params.Service), trimDot(params.Domain))

	// Build the DNS query message
	m := new(dns.Msg)
	if params.Instance != "" {
		// Service instance lookup (SRV + TXT records)
		serviceInstanceName = fmt.Sprintf("%s.%s", params.Instance, serviceName)
		m.Question = []dns.Question{
			{Name: serviceInstanceName, Qtype: dns.TypeSRV, Qclass: dns.ClassINET},
			{Name: serviceInstanceName, Qtype: dns.TypeTXT, Qclass: dns.ClassINET},
		}
	} else if len(params.Subtypes) > 0 {
		// Service subtype browsing
		m.SetQuestion(params.Subtypes[0], dns.TypePTR)
	} else {
		// Service type browsing
		m.SetQuestion(serviceName, dns.TypePTR)
	}
	m.RecursionDesired = false

	return c.sendQuery(m)
}

// sendQuery packs a DNS message and transmits it via all available
// multicast connections on all configured interfaces.
//
// The method handles platform-specific differences in multicast socket
// control message implementation.
func (c *client) sendQuery(msg *dns.Msg) error {
	buf, err := msg.Pack()
	if err != nil {
		return err
	}

	// Send via IPv4 if available
	if c.ipv4conn != nil {
		var wcm ipv4.ControlMessage
		for ifi := range c.ifaces {
			// Platform-specific interface binding
			switch runtime.GOOS {
			case "darwin", "ios", "linux":
				wcm.IfIndex = c.ifaces[ifi].Index
			default:
				if err := c.ipv4conn.SetMulticastInterface(&c.ifaces[ifi]); err != nil {
					log.Printf("[WARN] mdns: Failed to set multicast interface: %v", err)
				}
			}
			c.ipv4conn.WriteTo(buf, &wcm, ipv4Addr)
		}
	}

	// Send via IPv6 if available
	if c.ipv6conn != nil {
		var wcm ipv6.ControlMessage
		for ifi := range c.ifaces {
			// Platform-specific interface binding
			switch runtime.GOOS {
			case "darwin", "ios", "linux":
				wcm.IfIndex = c.ifaces[ifi].Index
			default:
				if err := c.ipv6conn.SetMulticastInterface(&c.ifaces[ifi]); err != nil {
					log.Printf("[WARN] mdns: Failed to set multicast interface: %v", err)
				}
			}
			c.ipv6conn.WriteTo(buf, &wcm, ipv6Addr)
		}
	}
	return nil
}
