// Package mdns provides a complete implementation of multicast DNS service
// registration and discovery, allowing services to announce their availability
// and respond to queries on the local network.
package mdns

import (
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// multicastRepetitions defines the number of times multicast responses
// are sent for query messages, following RFC 6762 recommendations.
const (
	multicastRepetitions = 2
)

// Register creates and announces a new service instance on the local network.
// It automatically determines the host's IP addresses and hostname, and starts
// the service announcement process using mDNS protocol.
//
// The function validates required parameters and returns a Server instance
// that manages the service lifecycle. The service will be automatically
// probed for conflicts and announced on the network.
//
// Returns an error if any required parameter is missing or if network
// initialization fails.
func Register(instance, service, domain string, port int, text []string, ifaces []net.Interface) (*Server, error) {
	entry := NewServiceEntry(instance, service, domain)
	entry.Port = port
	entry.Text = text

	// Validate required parameters
	if entry.Instance == "" {
		return nil, fmt.Errorf("missing service instance name")
	}
	if entry.Service == "" {
		return nil, fmt.Errorf("missing service name")
	}
	if entry.Domain == "" {
		entry.Domain = "local."
	}
	if entry.Port == 0 {
		return nil, fmt.Errorf("missing port")
	}

	// Determine hostname if not provided
	var err error
	if entry.HostName == "" {
		entry.HostName, err = os.Hostname()
		if err != nil {
			return nil, fmt.Errorf("could not determine host")
		}
	}

	// Ensure hostname is fully qualified in the service domain
	if !strings.HasSuffix(trimDot(entry.HostName), entry.Domain) {
		entry.HostName = fmt.Sprintf("%s.%s.", trimDot(entry.HostName), trimDot(entry.Domain))
	}

	// Use all multicast interfaces if none specified
	if len(ifaces) == 0 {
		ifaces = listMulticastInterfaces()
	}

	// Collect IP addresses from all specified interfaces
	for _, iface := range ifaces {
		v4, v6 := addrsForInterface(&iface)
		entry.AddrIPv4 = append(entry.AddrIPv4, v4...)
		entry.AddrIPv6 = append(entry.AddrIPv6, v6...)
	}

	if entry.AddrIPv4 == nil && entry.AddrIPv6 == nil {
		return nil, fmt.Errorf("could not determine host IP addresses")
	}

	// Initialize server and start service announcement
	s, err := newServer(ifaces)
	if err != nil {
		return nil, err
	}

	s.service = entry
	go s.mainloop()
	go s.probe()

	return s, nil
}

// RegisterProxy registers a service proxy with explicitly provided network
// configuration, skipping automatic hostname and IP address detection.
//
// This function is useful for services running behind proxies or in containers
// where the external visibility differs from the internal host configuration.
//
// Returns an error if any required parameter is invalid or if network
// initialization fails.
func RegisterProxy(instance, service, domain string, port int, host string, ips []string, text []string, ifaces []net.Interface) (*Server, error) {
	entry := NewServiceEntry(instance, service, domain)
	entry.Port = port
	entry.Text = text
	entry.HostName = host

	// Validate all required parameters
	if entry.Instance == "" {
		return nil, fmt.Errorf("missing service instance name")
	}
	if entry.Service == "" {
		return nil, fmt.Errorf("missing service name")
	}
	if entry.HostName == "" {
		return nil, fmt.Errorf("missing host name")
	}
	if entry.Domain == "" {
		entry.Domain = "local"
	}
	if entry.Port == 0 {
		return nil, fmt.Errorf("missing port")
	}

	// Ensure hostname is fully qualified in the service domain
	if !strings.HasSuffix(trimDot(entry.HostName), entry.Domain) {
		entry.HostName = fmt.Sprintf("%s.%s.", trimDot(entry.HostName), trimDot(entry.Domain))
	}

	// Parse and validate provided IP addresses
	for _, ip := range ips {
		ipAddr := net.ParseIP(ip)
		if ipAddr == nil {
			return nil, fmt.Errorf("failed to parse given IP: %v", ip)
		} else if ipv4 := ipAddr.To4(); ipv4 != nil {
			entry.AddrIPv4 = append(entry.AddrIPv4, ipAddr)
		} else if ipv6 := ipAddr.To16(); ipv6 != nil {
			entry.AddrIPv6 = append(entry.AddrIPv6, ipAddr)
		} else {
			return nil, fmt.Errorf("the IP is neither IPv4 nor IPv6: %#v", ipAddr)
		}
	}

	// Use all multicast interfaces if none specified
	if len(ifaces) == 0 {
		ifaces = listMulticastInterfaces()
	}

	// Initialize server and start service announcement
	s, err := newServer(ifaces)
	if err != nil {
		return nil, err
	}

	s.service = entry
	go s.mainloop()
	go s.probe()

	return s, nil
}

// qClassCacheFlush is the top bit of the qclass field used to indicate
// that a record should flush conflicting cache entries (RFC 6762 section 10.2).
const (
	qClassCacheFlush uint16 = 1 << 15
)

// Server manages the network connections and protocol handling for a registered
// mDNS service. It responds to queries, sends announcements, and handles
// the complete service lifecycle.
type Server struct {
	service  *ServiceEntry
	ipv4conn *ipv4.PacketConn
	ipv6conn *ipv6.PacketConn
	ifaces   []net.Interface

	shouldShutdown chan struct{}
	shutdownLock   sync.Mutex
	shutdownEnd    sync.WaitGroup
	isShutdown     bool
	ttl            uint32
}

// newServer initializes a new mDNS server with the specified network interfaces.
// It creates and joins multicast groups for both IPv4 and IPv6 protocols.
//
// Returns an error if no supported network interfaces can be initialized.
func newServer(ifaces []net.Interface) (*Server, error) {
	// Initialize IPv4 multicast connection
	ipv4conn, err4 := joinUdp4Multicast(ifaces)
	if err4 != nil {
		log.Printf("[zeroconf] no suitable IPv4 interface: %s", err4.Error())
	}
	
	// Initialize IPv6 multicast connection
	ipv6conn, err6 := joinUdp6Multicast(ifaces)
	if err6 != nil {
		log.Printf("[zeroconf] no suitable IPv6 interface: %s", err6.Error())
	}
	
	// Require at least one working connection
	if err4 != nil && err6 != nil {
		return nil, fmt.Errorf("no supported interface")
	}

	s := &Server{
		ipv4conn:       ipv4conn,
		ipv6conn:       ipv6conn,
		ifaces:         ifaces,
		ttl:            3200, // Default TTL for service records
		shouldShutdown: make(chan struct{}),
	}

	return s, nil
}

// mainloop starts the packet reception goroutines for IPv4 and IPv6 connections.
// These goroutines will run until the server is shut down.
func (s *Server) mainloop() {
	if s.ipv4conn != nil {
		go s.recv4(s.ipv4conn)
	}
	if s.ipv6conn != nil {
		go s.recv6(s.ipv6conn)
	}
}

// Shutdown gracefully stops the server, unregisters the service, and closes
// all network connections. It waits for all goroutines to complete.
func (s *Server) Shutdown() {
	s.shutdown()
}

// SetText updates the service's TXT records and announces the changes to
// the network. This can be used to update service metadata at runtime.
func (s *Server) SetText(text []string) {
	s.service.Text = text
	s.announceText()
}

// TTL sets the Time-To-Live value for DNS responses sent by this server.
// This controls how long other hosts should cache the service records.
func (s *Server) TTL(ttl uint32) {
	s.ttl = ttl
}

// shutdown performs the actual shutdown procedure, ensuring thread-safe
// operation and proper cleanup of resources.
//
// Returns an error if the server is already shutdown or if unregistration fails.
func (s *Server) shutdown() error {
	s.shutdownLock.Lock()
	defer s.shutdownLock.Unlock()
	if s.isShutdown {
		return errors.New("server is already shutdown")
	}

	// Send unregistration announcement
	err := s.unregister()

	// Signal shutdown to all goroutines
	close(s.shouldShutdown)

	// Close network connections
	if s.ipv4conn != nil {
		s.ipv4conn.Close()
	}
	if s.ipv6conn != nil {
		s.ipv6conn.Close()
	}

	// Wait for all goroutines to complete
	s.shutdownEnd.Wait()
	s.isShutdown = true

	return err
}

// recv4 continuously receives and processes IPv4 packets until shutdown.
// It runs in a separate goroutine and handles incoming mDNS queries.
func (s *Server) recv4(c *ipv4.PacketConn) {
	if c == nil {
		return
	}
	buf := make([]byte, 65536)
	s.shutdownEnd.Add(1)
	defer s.shutdownEnd.Done()
	for {
		select {
		case <-s.shouldShutdown:
			return
		default:
			var ifIndex int
			n, cm, from, err := c.ReadFrom(buf)
			if err != nil {
				continue
			}
			if cm != nil {
				ifIndex = cm.IfIndex
			}
			_ = s.parsePacket(buf[:n], ifIndex, from)
		}
	}
}

// recv6 continuously receives and processes IPv6 packets until shutdown.
// It runs in a separate goroutine and handles incoming mDNS queries.
func (s *Server) recv6(c *ipv6.PacketConn) {
	if c == nil {
		return
	}
	buf := make([]byte, 65536)
	s.shutdownEnd.Add(1)
	defer s.shutdownEnd.Done()
	for {
		select {
		case <-s.shouldShutdown:
			return
		default:
			var ifIndex int
			n, cm, from, err := c.ReadFrom(buf)
			if err != nil {
				continue
			}
			if cm != nil {
				ifIndex = cm.IfIndex
			}
			_ = s.parsePacket(buf[:n], ifIndex, from)
		}
	}
}

// parsePacket decodes a raw DNS packet and dispatches it for query handling.
// It silently ignores malformed packets to maintain service availability.
func (s *Server) parsePacket(packet []byte, ifIndex int, from net.Addr) error {
	var msg dns.Msg
	if err := msg.Unpack(packet); err != nil {
		return err
	}
	return s.handleQuery(&msg, ifIndex, from)
}

// handleQuery processes an incoming DNS query message and generates
// appropriate responses based on the query type and service configuration.
func (s *Server) handleQuery(query *dns.Msg, ifIndex int, from net.Addr) error {
	// Process each question in the query message
	var err error
	for _, q := range query.Question {
		resp := dns.Msg{}
		resp.SetReply(query)
		resp.Compress = true
		resp.RecursionDesired = false
		resp.Authoritative = true
		resp.Question = nil // RFC6762: responses must not contain questions
		resp.Answer = []dns.RR{}
		resp.Extra = []dns.RR{}
		
		if err = s.handleQuestion(q, &resp, query, ifIndex); err != nil {
			continue
		}
		
		// Skip if no answer was generated
		if len(resp.Answer) == 0 {
			continue
		}

		// Send response via appropriate method (unicast/multicast)
		if isUnicastQuestion(q) {
			if e := s.unicastResponse(&resp, ifIndex, from); e != nil {
				err = e
			}
		} else {
			if e := s.multicastResponse(&resp, ifIndex); e != nil {
				err = e
			}
		}
	}

	return err
}

// isKnownAnswer implements RFC6762 section 7.1 known-answer suppression.
// It checks if a response would be redundant based on answers already present
// in the query message, reducing unnecessary network traffic.
func isKnownAnswer(resp *dns.Msg, query *dns.Msg) bool {
	if len(resp.Answer) == 0 || len(query.Answer) == 0 {
		return false
	}

	if resp.Answer[0].Header().Rrtype != dns.TypePTR {
		return false
	}
	answer := resp.Answer[0].(*dns.PTR)

	for _, known := range query.Answer {
		hdr := known.Header()
		if hdr.Rrtype != answer.Hdr.Rrtype {
			continue
		}
		ptr := known.(*dns.PTR)
		if ptr.Ptr == answer.Ptr && hdr.Ttl >= answer.Hdr.Ttl/2 {
			return true
		}
	}

	return false
}

// handleQuestion processes a single DNS question and populates the response
// with appropriate records based on the question type and service configuration.
func (s *Server) handleQuestion(q dns.Question, resp *dns.Msg, query *dns.Msg, ifIndex int) error {
	if s.service == nil {
		return nil
	}

	switch q.Name {
	case s.service.ServiceTypeName():
		s.serviceTypeName(resp, s.ttl)
		if isKnownAnswer(resp, query) {
			resp.Answer = nil
		}

	case s.service.ServiceName():
		s.composeBrowsingAnswers(resp, ifIndex)
		if isKnownAnswer(resp, query) {
			resp.Answer = nil
		}

	case s.service.ServiceInstanceName():
		s.composeLookupAnswers(resp, s.ttl, ifIndex, false)
	default:
		// Handle service subtype queries
		for _, subtype := range s.service.Subtypes {
			subtype = fmt.Sprintf("%s._sub.%s", subtype, s.service.ServiceName())
			if q.Name == subtype {
				s.composeBrowsingAnswers(resp, ifIndex)
				if isKnownAnswer(resp, query) {
					resp.Answer = nil
				}
				break
			}
		}
	}

	return nil
}

// composeBrowsingAnswers constructs DNS records for service browsing queries,
// including PTR records for service discovery and additional SRV/TXT records.
func (s *Server) composeBrowsingAnswers(resp *dns.Msg, ifIndex int) {
	// PTR record pointing to service instance
	ptr := &dns.PTR{
		Hdr: dns.RR_Header{
			Name:   s.service.ServiceName(),
			Rrtype: dns.TypePTR,
			Class:  dns.ClassINET,
			Ttl:    s.ttl,
		},
		Ptr: s.service.ServiceInstanceName(),
	}
	resp.Answer = append(resp.Answer, ptr)

	// Additional records for service details
	txt := &dns.TXT{
		Hdr: dns.RR_Header{
			Name:   s.service.ServiceInstanceName(),
			Rrtype: dns.TypeTXT,
			Class:  dns.ClassINET,
			Ttl:    s.ttl,
		},
		Txt: s.service.Text,
	}
	srv := &dns.SRV{
		Hdr: dns.RR_Header{
			Name:   s.service.ServiceInstanceName(),
			Rrtype: dns.TypeSRV,
			Class:  dns.ClassINET,
			Ttl:    s.ttl,
		},
		Priority: 0,
		Weight:   0,
		Port:     uint16(s.service.Port),
		Target:   s.service.HostName,
	}
	resp.Extra = append(resp.Extra, srv, txt)

	// Add IP address records
	resp.Extra = s.appendAddrs(resp.Extra, s.ttl, ifIndex, false)
}

// composeLookupAnswers constructs complete DNS records for service instance
// lookup queries, including all relevant service metadata and IP addresses.
func (s *Server) composeLookupAnswers(resp *dns.Msg, ttl uint32, ifIndex int, flushCache bool) {
	// Service instance records with cache flush bit for announcements
	ptr := &dns.PTR{
		Hdr: dns.RR_Header{
			Name:   s.service.ServiceName(),
			Rrtype: dns.TypePTR,
			Class:  dns.ClassINET,
			Ttl:    ttl,
		},
		Ptr: s.service.ServiceInstanceName(),
	}
	srv := &dns.SRV{
		Hdr: dns.RR_Header{
			Name:   s.service.ServiceInstanceName(),
			Rrtype: dns.TypeSRV,
			Class:  dns.ClassINET | qClassCacheFlush,
			Ttl:    ttl,
		},
		Priority: 0,
		Weight:   0,
		Port:     uint16(s.service.Port),
		Target:   s.service.HostName,
	}
	txt := &dns.TXT{
		Hdr: dns.RR_Header{
			Name:   s.service.ServiceInstanceName(),
			Rrtype: dns.TypeTXT,
			Class:  dns.ClassINET | qClassCacheFlush,
			Ttl:    ttl,
		},
		Txt: s.service.Text,
	}
	dnssd := &dns.PTR{
		Hdr: dns.RR_Header{
			Name:   s.service.ServiceTypeName(),
			Rrtype: dns.TypePTR,
			Class:  dns.ClassINET,
			Ttl:    ttl,
		},
		Ptr: s.service.ServiceName(),
	}
	resp.Answer = append(resp.Answer, srv, txt, ptr, dnssd)

	// Subtype records if any
	for _, subtype := range s.service.Subtypes {
		resp.Answer = append(resp.Answer,
			&dns.PTR{
				Hdr: dns.RR_Header{
					Name:   subtype,
					Rrtype: dns.TypePTR,
					Class:  dns.ClassINET,
					Ttl:    ttl,
				},
				Ptr: s.service.ServiceInstanceName(),
			})
	}

	// Add IP address records
	resp.Answer = s.appendAddrs(resp.Answer, ttl, ifIndex, flushCache)
}

// serviceTypeName handles DNS-SD service type enumeration queries by
// returning a PTR record for the service type as specified in RFC6762 section 9.
func (s *Server) serviceTypeName(resp *dns.Msg, ttl uint32) {
	dnssd := &dns.PTR{
		Hdr: dns.RR_Header{
			Name:   s.service.ServiceTypeName(),
			Rrtype: dns.TypePTR,
			Class:  dns.ClassINET,
			Ttl:    ttl,
		},
		Ptr: s.service.ServiceName(),
	}
	resp.Answer = append(resp.Answer, dnssd)
}

// probe performs service instance probing and conflict detection followed by
// service announcement as specified in RFC6762 section 8.
//
// The function sends multiple probe packets with random delays to detect
// conflicts, then sends announcements with exponential backoff.
func (s *Server) probe() {
	// Create probe query with service instance details
	q := new(dns.Msg)
	q.SetQuestion(s.service.ServiceInstanceName(), dns.TypePTR)
	q.RecursionDesired = false

	srv := &dns.SRV{
		Hdr: dns.RR_Header{
			Name:   s.service.ServiceInstanceName(),
			Rrtype: dns.TypeSRV,
			Class:  dns.ClassINET,
			Ttl:    s.ttl,
		},
		Priority: 0,
		Weight:   0,
		Port:     uint16(s.service.Port),
		Target:   s.service.HostName,
	}
	txt := &dns.TXT{
		Hdr: dns.RR_Header{
			Name:   s.service.ServiceInstanceName(),
			Rrtype: dns.TypeTXT,
			Class:  dns.ClassINET,
			Ttl:    s.ttl,
		},
		Txt: s.service.Text,
	}
	q.Ns = []dns.RR{srv, txt}

	// Send probe packets with random delays
	randomizer := rand.New(rand.NewSource(time.Now().UnixNano()))
	for i := 0; i < multicastRepetitions; i++ {
		if err := s.multicastResponse(q, 0); err != nil {
			log.Println("[ERR] zeroconf: failed to send probe:", err.Error())
		}
		time.Sleep(time.Duration(randomizer.Intn(250)) * time.Millisecond)
	}

	// Send service announcements with exponential backoff
	timeout := 1 * time.Second
	for i := 0; i < multicastRepetitions; i++ {
		for _, intf := range s.ifaces {
			resp := new(dns.Msg)
			resp.MsgHdr.Response = true
			resp.Compress = true
			resp.Answer = []dns.RR{}
			resp.Extra = []dns.RR{}
			s.composeLookupAnswers(resp, s.ttl, intf.Index, true)
			if err := s.multicastResponse(resp, intf.Index); err != nil {
				log.Println("[ERR] zeroconf: failed to send announcement:", err.Error())
			}
		}
		time.Sleep(timeout)
		timeout *= 2
	}
}

// announceText sends a multicast announcement specifically for updated TXT records,
// using the cache flush bit to ensure clients update their cached values.
func (s *Server) announceText() {
	resp := new(dns.Msg)
	resp.MsgHdr.Response = true

	txt := &dns.TXT{
		Hdr: dns.RR_Header{
			Name:   s.service.ServiceInstanceName(),
			Rrtype: dns.TypeTXT,
			Class:  dns.ClassINET | qClassCacheFlush,
			Ttl:    s.ttl,
		},
		Txt: s.service.Text,
	}

	resp.Answer = []dns.RR{txt}
	s.multicastResponse(resp, 0)
}

// unregister sends a final announcement with zero TTL to indicate service
// shutdown, allowing clients to remove the service from their caches.
func (s *Server) unregister() error {
	resp := new(dns.Msg)
	resp.MsgHdr.Response = true
	resp.Answer = []dns.RR{}
	resp.Extra = []dns.RR{}
	s.composeLookupAnswers(resp, 0, 0, true)
	return s.multicastResponse(resp, 0)
}

// appendAddrs adds A and AAAA records for the service's IP addresses to the
// DNS record list, with appropriate TTL and cache flush settings.
func (s *Server) appendAddrs(list []dns.RR, ttl uint32, ifIndex int, flushCache bool) []dns.RR {
	v4 := s.service.AddrIPv4
	v6 := s.service.AddrIPv6
	
	// If no addresses configured, use addresses from the specified interface
	if len(v4) == 0 && len(v6) == 0 {
		iface, _ := net.InterfaceByIndex(ifIndex)
		if iface != nil {
			a4, a6 := addrsForInterface(iface)
			v4 = append(v4, a4...)
			v6 = append(v6, a6...)
		}
	}
	
	// Use recommended TTL for address records (RFC6762 section 10)
	if ttl > 0 {
		ttl = 120
	}
	
	var cacheFlushBit uint16
	if flushCache {
		cacheFlushBit = qClassCacheFlush
	}
	
	// Add IPv4 address records
	for _, ipv4 := range v4 {
		a := &dns.A{
			Hdr: dns.RR_Header{
				Name:   s.service.HostName,
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET | cacheFlushBit,
				Ttl:    ttl,
			},
			A: ipv4,
		}
		list = append(list, a)
	}
	
	// Add IPv6 address records
	for _, ipv6 := range v6 {
		aaaa := &dns.AAAA{
			Hdr: dns.RR_Header{
				Name:   s.service.HostName,
				Rrtype: dns.TypeAAAA,
				Class:  dns.ClassINET | cacheFlushBit,
				Ttl:    ttl,
			},
			AAAA: ipv6,
		}
		list = append(list, aaaa)
	}
	return list
}

// addrsForInterface extracts IPv4 and IPv6 addresses from a network interface,
// filtering out loopback addresses and categorizing IPv6 addresses by scope.
func addrsForInterface(iface *net.Interface) ([]net.IP, []net.IP) {
	var v4, v6, v6local []net.IP
	addrs, _ := iface.Addrs()
	for _, address := range addrs {
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				v4 = append(v4, ipnet.IP)
			} else {
				switch ip := ipnet.IP.To16(); ip != nil {
				case ip.IsGlobalUnicast():
					v6 = append(v6, ipnet.IP)
				case ip.IsLinkLocalUnicast():
					v6local = append(v6local, ipnet.IP)
				}
			}
		}
	}
	// Use link-local addresses if no global addresses available
	if len(v6) == 0 {
		v6 = v6local
	}
	return v4, v6
}

// unicastResponse sends a DNS response directly to the query source address
// instead of using multicast, as requested by the client via the unicast bit.
func (s *Server) unicastResponse(resp *dns.Msg, ifIndex int, from net.Addr) error {
	buf, err := resp.Pack()
	if err != nil {
		return err
	}
	addr := from.(*net.UDPAddr)
	
	// Send via appropriate protocol (IPv4/IPv6)
	if addr.IP.To4() != nil {
		if ifIndex != 0 {
			var wcm ipv4.ControlMessage
			wcm.IfIndex = ifIndex
			_, err = s.ipv4conn.WriteTo(buf, &wcm, addr)
		} else {
			_, err = s.ipv4conn.WriteTo(buf, nil, addr)
		}
		return err
	} else {
		if ifIndex != 0 {
			var wcm ipv6.ControlMessage
			wcm.IfIndex = ifIndex
			_, err = s.ipv6conn.WriteTo(buf, &wcm, addr)
		} else {
			_, err = s.ipv6conn.WriteTo(buf, nil, addr)
		}
		return err
	}
}

// multicastResponse sends a DNS response to the mDNS multicast group so
// all interested hosts on the network can receive it.
//
// The function handles platform-specific differences in multicast socket
// control message implementation.
func (s *Server) multicastResponse(msg *dns.Msg, ifIndex int) error {
	buf, err := msg.Pack()
	if err != nil {
		return err
	}
	
	// Send via IPv4 multicast
	if s.ipv4conn != nil {
		var wcm ipv4.ControlMessage
		if ifIndex != 0 {
			// Platform-specific interface binding
			switch runtime.GOOS {
			case "darwin", "ios", "linux":
				wcm.IfIndex = ifIndex
			default:
				iface, _ := net.InterfaceByIndex(ifIndex)
				if err := s.ipv4conn.SetMulticastInterface(iface); err != nil {
					log.Printf("[WARN] mdns: Failed to set multicast interface: %v", err)
				}
			}
			s.ipv4conn.WriteTo(buf, &wcm, ipv4Addr)
		} else {
			// Send to all interfaces
			for _, intf := range s.ifaces {
				switch runtime.GOOS {
				case "darwin", "ios", "linux":
					wcm.IfIndex = intf.Index
				default:
					if err := s.ipv4conn.SetMulticastInterface(&intf); err != nil {
						log.Printf("[WARN] mdns: Failed to set multicast interface: %v", err)
					}
				}
				s.ipv4conn.WriteTo(buf, &wcm, ipv4Addr)
			}
		}
	}

	// Send via IPv6 multicast
	if s.ipv6conn != nil {
		var wcm ipv6.ControlMessage
		if ifIndex != 0 {
			// Platform-specific interface binding
			switch runtime.GOOS {
			case "darwin", "ios", "linux":
				wcm.IfIndex = ifIndex
			default:
				iface, _ := net.InterfaceByIndex(ifIndex)
				if err := s.ipv6conn.SetMulticastInterface(iface); err != nil {
					log.Printf("[WARN] mdns: Failed to set multicast interface: %v", err)
				}
			}
			s.ipv6conn.WriteTo(buf, &wcm, ipv6Addr)
		} else {
			// Send to all interfaces
			for _, intf := range s.ifaces {
				switch runtime.GOOS {
				case "darwin", "ios", "linux":
					wcm.IfIndex = intf.Index
				default:
					if err := s.ipv6conn.SetMulticastInterface(&intf); err != nil {
						log.Printf("[WARN] mdns: Failed to set multicast interface: %v", err)
					}
				}
				s.ipv6conn.WriteTo(buf, &wcm, ipv6Addr)
			}
		}
	}
	return nil
}

// isUnicastQuestion checks if a DNS question has the unicast response bit set
// as specified in RFC6762 section 18.12, indicating the client prefers
// unicast responses instead of multicast.
func isUnicastQuestion(q dns.Question) bool {
	return q.Qclass&qClassCacheFlush != 0
}