// Package lan tells whether an address belongs to a home or virtual
// network, where plain http is acceptable: a classroom LAN, or a VPN such
// as Radmin VPN, Tailscale or ZeroTier that already encrypts the traffic
// between friends' computers in different cities.
package lan

import "net"

var vpnNets = []*net.IPNet{
	mustCIDR("100.64.0.0/10"), // Tailscale and other CGNAT-range VPNs
	mustCIDR("26.0.0.0/8"),    // Radmin VPN
}

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// IsVPN reports whether ip is in a range a home VPN hands out.
func IsVPN(ip net.IP) bool {
	for _, n := range vpnNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// IsHome reports whether ip is a local-network or home-VPN address.
func IsHome(ip net.IP) bool {
	return ip != nil && (ip.IsPrivate() || IsVPN(ip))
}
