package main

import (
	"net"
	"fmt"
)

func getLocalIP() (net.IP, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range interfaces {

		// Skip interfaces that are down or loopback (127.0.0.1).
		// AND operation to check if Flag is set
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			// Extract the IP from CIDR notation (e.g. 192.168.1.42/24).
			// Refer to "Go - Networking Doubts"
			ip, _, err := net.ParseCIDR(addr.String())

			if err != nil {
				continue
			}

			if ip.To4() != nil {
				return ip, nil
			}
		}
	}

	return nil, fmt.Errorf("no local IPv4 address found")
}
