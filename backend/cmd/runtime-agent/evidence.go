package main

import (
	"net"
	"net/netip"
)

// The socket peer is observed directly. Forwarded headers are not trusted as a
// client's identity or location without a separate customer trust configuration.
func observedClientIP(remote string) string {
	host, _, e := net.SplitHostPort(remote)
	if e != nil {
		return ""
	}
	ip, e := netip.ParseAddr(host)
	if e != nil || ip.IsUnspecified() || ip.Zone() != "" {
		return ""
	}
	return ip.Unmap().String()
}
