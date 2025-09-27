// Package mdns implements multicast DNS service discovery functionality
// for local network environments, supporting both IPv4 and IPv6 protocols.
package mdns

import (
	"fmt"
	"net"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// Multicast addressing constants used by mDNS protocol.
var (
	// mdnsGroupIPv4 is the IPv4 multicast group address for mDNS as defined
	// by RFC 6762 (224.0.0.251).
	mdnsGroupIPv4 = net.IPv4(224, 0, 0, 251)

	// mdnsGroupIPv6 is the IPv6 multicast group address for mDNS as defined
	// by RFC 6762 (ff02::fb).
	mdnsGroupIPv6 = net.ParseIP("ff02::fb")

	// mdnsWildcardAddrIPv4 is the wildcard binding address for IPv4 mDNS
	// sockets. Binding to this address allows reception of all multicast
	// traffic on port 5353.
	mdnsWildcardAddrIPv4 = &net.UDPAddr{
		IP:   net.ParseIP("224.0.0.0"),
		Port: 5353,
	}

	// mdnsWildcardAddrIPv6 is the wildcard binding address for IPv6 mDNS
	// sockets. Binding to this address allows reception of all multicast
	// traffic on port 5353.
	mdnsWildcardAddrIPv6 = &net.UDPAddr{
		IP:   net.ParseIP("ff02::"),
		Port: 5353,
	}

	// ipv4Addr is the destination address for sending IPv4 mDNS queries
	// and announcements to the multicast group.
	ipv4Addr = &net.UDPAddr{
		IP:   mdnsGroupIPv4,
		Port: 5353,
	}

	// ipv6Addr is the destination address for sending IPv6 mDNS queries
	// and announcements to the multicast group.
	ipv6Addr = &net.UDPAddr{
		IP:   mdnsGroupIPv6,
		Port: 5353,
	}
)

// joinUdp6Multicast creates a UDP IPv6 socket, joins the mDNS multicast group
// on the specified interfaces, and returns a configured packet connection.
//
// The function binds to the mDNS wildcard address to receive all multicast
// traffic and sets appropriate socket options for multicast operation.
//
// Returns an error if binding fails or if unable to join the multicast group
// on any of the provided interfaces.
func joinUdp6Multicast(interfaces []net.Interface) (*ipv6.PacketConn, error) {
	// Create UDP socket bound to mDNS wildcard address
	udpConn, err := net.ListenUDP("udp6", mdnsWildcardAddrIPv6)
	if err != nil {
		return nil, err
	}

	// Configure packet connection for multicast operation
	pkConn := ipv6.NewPacketConn(udpConn)
	pkConn.SetControlMessage(ipv6.FlagInterface, true)
	_ = pkConn.SetMulticastHopLimit(255)

	// Use all multicast interfaces if none specified
	if len(interfaces) == 0 {
		interfaces = listMulticastInterfaces()
	}

	// Join multicast group on each specified interface
	var failedJoins int
	for _, iface := range interfaces {
		if err := pkConn.JoinGroup(&iface, &net.UDPAddr{IP: mdnsGroupIPv6}); err != nil {
			failedJoins++
		}
	}

	// Fail if unable to join any interface
	if failedJoins == len(interfaces) {
		pkConn.Close()
		return nil, fmt.Errorf("udp6: failed to join any of these interfaces: %v", interfaces)
	}

	return pkConn, nil
}

// joinUdp4Multicast creates a UDP IPv4 socket, joins the mDNS multicast group
// on the specified interfaces, and returns a configured packet connection.
//
// The function follows the same pattern as joinUdp6Multicast but for IPv4
// protocol, including binding to the appropriate multicast addresses.
//
// Returns an error if binding fails or if unable to join the multicast group
// on any of the provided interfaces.
func joinUdp4Multicast(interfaces []net.Interface) (*ipv4.PacketConn, error) {
	// Create UDP socket bound to mDNS wildcard address
	udpConn, err := net.ListenUDP("udp4", mdnsWildcardAddrIPv4)
	if err != nil {
		return nil, err
	}

	// Configure packet connection for multicast operation
	pkConn := ipv4.NewPacketConn(udpConn)
	pkConn.SetControlMessage(ipv4.FlagInterface, true)
	_ = pkConn.SetMulticastTTL(255)

	// Use all multicast interfaces if none specified
	if len(interfaces) == 0 {
		interfaces = listMulticastInterfaces()
	}

	// Join multicast group on each specified interface
	var failedJoins int
	for _, iface := range interfaces {
		if err := pkConn.JoinGroup(&iface, &net.UDPAddr{IP: mdnsGroupIPv4}); err != nil {
			failedJoins++
		}
	}

	// Fail if unable to join any interface
	if failedJoins == len(interfaces) {
		pkConn.Close()
		return nil, fmt.Errorf("udp4: failed to join any of these interfaces: %v", interfaces)
	}

	return pkConn, nil
}

// listMulticastInterfaces scans all system network interfaces and returns
// those that are up and support multicast communication.
//
// This is used to automatically discover suitable interfaces for mDNS
// operations when no specific interfaces are provided by the caller.
//
// Returns nil if interface enumeration fails, otherwise returns a slice
// of multicast-capable interfaces.
func listMulticastInterfaces() []net.Interface {
	var interfaces []net.Interface
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	// Filter interfaces: must be UP and support MULTICAST
	for _, ifi := range ifaces {
		if (ifi.Flags & net.FlagUp) == 0 {
			continue
		}
		if (ifi.Flags & net.FlagMulticast) > 0 {
			interfaces = append(interfaces, ifi)
		}
	}

	return interfaces
}
