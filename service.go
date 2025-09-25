package mdns

import (
	"fmt"
	"net"
	"sync"
)

// ServiceHandler is a callback function that will be called for each discovered service.
type ServiceHandler func(*ServiceEntry)

// ServiceRecord contains the basic description of a service.
type ServiceRecord struct {
	Instance string   `json:"name"`     // Instance name (e.g. "My web page")
	Service  string   `json:"type"`     // Service name (e.g. _http._tcp.)
	Subtypes []string `json:"subtypes"` // Service subtypes
	Domain   string   `json:"domain"`   // If blank, assumes "local"

	// cached strings
	serviceName         string
	serviceInstanceName string
	serviceTypeName     string
}

// ServiceName returns a complete service name (e.g. _foobar._tcp.local.)
func (s *ServiceRecord) ServiceName() string {
	return s.serviceName
}

// ServiceInstanceName returns a complete service instance name
func (s *ServiceRecord) ServiceInstanceName() string {
	return s.serviceInstanceName
}

// ServiceTypeName returns the complete identifier for a DNS-SD query.
func (s *ServiceRecord) ServiceTypeName() string {
	return s.serviceTypeName
}

// NewServiceRecord constructs a ServiceRecord.
func NewServiceRecord(instance, service string, domain string) *ServiceRecord {
	service, subtypes := parseSubtypes(service)
	s := &ServiceRecord{
		Instance:    instance,
		Service:     service,
		Domain:      domain,
		serviceName: fmt.Sprintf("%s.%s.", trimDot(service), trimDot(domain)),
	}

	for _, subtype := range subtypes {
		s.Subtypes = append(s.Subtypes, fmt.Sprintf("%s._sub.%s", trimDot(subtype), s.serviceName))
	}

	// Cache service instance name
	if instance != "" {
		s.serviceInstanceName = fmt.Sprintf("%s.%s", trimDot(s.Instance), s.ServiceName())
	}

	// Cache service type name domain
	typeNameDomain := "local"
	if len(s.Domain) > 0 {
		typeNameDomain = trimDot(s.Domain)
	}
	s.serviceTypeName = fmt.Sprintf("_services._dns-sd._udp.%s.", typeNameDomain)

	return s
}

// lookupParams contains configurable properties to create a service discovery request
type lookupParams struct {
	ServiceRecord

	// handler callback to call on each discovered entry
	handler ServiceHandler

	isBrowsing  bool
	stopProbing chan struct{}
	once        sync.Once
}

// newLookupParams constructs a lookupParams.
func newLookupParams(instance, service, domain string, isBrowsing bool, handler ServiceHandler) *lookupParams {
	p := &lookupParams{
		ServiceRecord: *NewServiceRecord(instance, service, domain),
		handler:       handler,
		isBrowsing:    isBrowsing,
	}
	if !isBrowsing {
		p.stopProbing = make(chan struct{})
	}
	return p
}

func (l *lookupParams) disableProbing() {
	l.once.Do(func() { close(l.stopProbing) })
}

// ServiceEntry represents a browse/lookup result for client API.
type ServiceEntry struct {
	ServiceRecord
	HostName string   `json:"hostname"` // Host machine DNS name
	Port     int      `json:"port"`     // Service Port
	Text     []string `json:"text"`     // TXT records
	TTL      uint32   `json:"ttl"`      // TTL of the service record
	AddrIPv4 []net.IP `json:"-"`        // IPv4 addresses
	AddrIPv6 []net.IP `json:"-"`        // IPv6 addresses
}

// NewServiceEntry constructs a ServiceEntry.
func NewServiceEntry(instance, service string, domain string) *ServiceEntry {
	return &ServiceEntry{
		ServiceRecord: *NewServiceRecord(instance, service, domain),
	}
}
