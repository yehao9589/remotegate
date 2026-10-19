package main

import "net"

// Prefer the interface bound by the direct dialer. For ordinary sockets, match
// the chosen local address to a system interface; never guess from a default route.
func socketEgressInterface(conn net.Conn) string {
	if routed, ok := conn.(interface{ EgressInterface() string }); ok {
		if iface := routed.EgressInterface(); iface != "" {
			return iface
		}
	}
	local, ok := conn.LocalAddr().(*net.TCPAddr)
	if !ok || local.IP.IsUnspecified() {
		return ""
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range interfaces {
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err == nil && ip.Equal(local.IP) {
				return iface.Name
			}
		}
	}
	return ""
}
