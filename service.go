// Package mdns defines the core data structures and interfaces for
// multicast DNS service discovery, including service records, lookup
// parameters, and discovery result handling.
package mdns

import (
	"fmt"
	"net"
	"sync"
)

// ServiceHandler is a callback function type that processes discovered
// service entries. It is invoked for each service instance found during
// browsing or lookup operations.
//
// The function receives a ServiceEntry pointer containing all available
// service information including network addresses, port, and metadata.
type ServiceHandler func(*ServiceEntry)

// ServiceRecord contains the fundamental description of a service for
// both registration and discovery purposes. It follows the DNS-SD
// naming conventions and structure.
type ServiceRecord struct {
	Instance string   `json:"name"`     // Instance name (e.g. "My Web Server")
	Service  string   `json:"type"`     // Service type (e.g. _http._tcp.)
	Subtypes []string `json:"subtypes"` // Optional service subtypes
	Domain   string   `json:"domain"`   // Domain (defaults to "local" if empty)

	// Cached computed names for efficient DNS query construction
	serviceName         string // Full service name (e.g. _http._tcp.local.)
	serviceInstanceName string // Full instance name (e.g. My Web Server._http._tcp.local.)
	serviceTypeName     string // DNS-SD service type enumeration name
}

// ServiceName returns the complete service name in the format
// required for DNS queries (e.g. "_http._tcp.local.").
//
// This is the name used for PTR record queries when browsing services.
func (s *ServiceRecord) ServiceName() string {
	return s.serviceName
}

// ServiceInstanceName returns the complete service instance name
// in the format required for DNS queries (e.g. "My Web Server._http._tcp.local.").
//
// This is the name used for SRV and TXT record queries when looking up
// specific service instances.
func (s *ServiceRecord) ServiceInstanceName() string {
	return s.serviceInstanceName
}

// ServiceTypeName returns the complete identifier for DNS-SD service
// type enumeration queries as specified in RFC 6763.
//
// The format is "_services._dns-sd._udp.<domain>" and is used to
// discover all available service types in a domain.
func (s *ServiceRecord) ServiceTypeName() string {
	return s.serviceTypeName
}

// NewServiceRecord constructs a new ServiceRecord with the given parameters
// and precomputes the DNS names for efficient query processing.
//
// The service parameter may include subtypes using the format
// "service,subtype1,subtype2" as per DNS-SD conventions.
func NewServiceRecord(instance, service string, domain string) *ServiceRecord {
	// Parse service type and any subtypes
	service, subtypes := parseSubtypes(service)

	s := &ServiceRecord{
		Instance:    instance,
		Service:     service,
		Domain:      domain,
		serviceName: fmt.Sprintf("%s.%s.", trimDot(service), trimDot(domain)),
	}

	// Build subtype names in the format "subtype._sub.servicename"
	for _, subtype := range subtypes {
		s.Subtypes = append(s.Subtypes, fmt.Sprintf("%s._sub.%s", trimDot(subtype), s.serviceName))
	}

	// Cache the service instance name if instance is provided
	if instance != "" {
		s.serviceInstanceName = fmt.Sprintf("%s.%s", trimDot(s.Instance), s.ServiceName())
	}

	// Cache the DNS-SD service type enumeration name
	typeNameDomain := "local"
	if len(s.Domain) > 0 {
		typeNameDomain = trimDot(s.Domain)
	}
	s.serviceTypeName = fmt.Sprintf("_services._dns-sd._udp.%s.", typeNameDomain)

	return s
}

// lookupParams contains all configurable properties needed to create
// and manage a service discovery request, including query parameters
// and response handling configuration.
type lookupParams struct {
	ServiceRecord

	// handler is the callback function invoked for each discovered service entry
	handler ServiceHandler

	// isBrowsing indicates whether this is a service browsing operation (true)
	// or a specific service instance lookup (false)
	isBrowsing bool

	// stopProbing channel signals when to stop sending periodic queries
	stopProbing chan struct{}

	// once ensures probing is disabled only once
	once sync.Once
}

// newLookupParams constructs a new lookupParams structure with the
// specified service discovery parameters and handler configuration.
//
// The isBrowsing parameter determines the query strategy: browsing
// queries for PTR records while lookup queries for SRV/TXT records.
func newLookupParams(instance, service, domain string, isBrowsing bool, handler ServiceHandler) *lookupParams {
	p := &lookupParams{
		ServiceRecord: *NewServiceRecord(instance, service, domain),
		handler:       handler,
		isBrowsing:    isBrowsing,
	}

	// Only create stop channel for lookup operations (not browsing)
	if !isBrowsing {
		p.stopProbing = make(chan struct{})
	}
	return p
}

// disableProbing signals that periodic querying should stop, typically
// called when a service instance has been successfully discovered.
//
// This method uses sync.Once to ensure the channel is closed only once,
// preventing panics from multiple close attempts.
func (l *lookupParams) disableProbing() {
	l.once.Do(func() {
		if l.stopProbing != nil {
			close(l.stopProbing)
		}
	})
}

// ServiceEntry represents a complete service discovery result containing
// all available information about a service instance found on the network.
//
// This structure is passed to the ServiceHandler callback and contains
// both the service description and network connectivity information.
type ServiceEntry struct {
	ServiceRecord

	HostName string   `json:"hostname"` // Host machine's DNS name (FQDN)
	Port     int      `json:"port"`     // Service port number
	Text     []string `json:"text"`     // Service metadata from TXT records
	TTL      uint32   `json:"ttl"`      // Time-to-live from DNS record
	AddrIPv4 []net.IP `json:"-"`        // IPv4 addresses for the service
	AddrIPv6 []net.IP `json:"-"`        // IPv6 addresses for the service

	// Event indicates whether this is an "Add" or "Rmv" event for
	// service availability changes during browsing
	Event string `json:"event"`

	// Flags bitmask indicating which DNS record types have been received
	// for this service entry (PTR, SRV, TXT, A, AAAA)
	Flags uint32 `json:"flags"`

	// IfIndex specifies the network interface index where the service
	// was discovered, useful for multi-homed systems
	IfIndex int `json:"ifindex"`
}

// NewServiceEntry constructs a new ServiceEntry with the basic service
// identification parameters. Additional fields like addresses and port
// are populated as DNS responses are received and processed.
func NewServiceEntry(instance, service string, domain string) *ServiceEntry {
	return &ServiceEntry{
		ServiceRecord: *NewServiceRecord(instance, service, domain),
	}
}
