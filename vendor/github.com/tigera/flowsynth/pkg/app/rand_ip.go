package app

import (
	"fmt"
	"math/rand"
	"net"
)

const defaultIPNet = "35.32.0.0/16"

func randIP(nets ...*net.IPNet) net.IP {
	var n *net.IPNet

	if len(nets) == 0 {
		var err error
		_, n, err = net.ParseCIDR(defaultIPNet)
		if err != nil {
			panic(fmt.Sprintf("Could not parse %v: %v", defaultIPNet, err))
		}
	} else {
		n = nets[rand.Intn(len(nets))]
	}

	ip := make(net.IP, len(n.IP))
	copy(ip, n.IP)

	filled, bits := n.Mask.Size()

	c := len(ip) - 1
	for filled < bits {
		if bits-filled >= 8 {
			ip[c] = byte(rand.Int())
			c--
			filled += 8
		} else {
			ip[c] |= (0xff >> uint(8-(bits-filled))) & byte(rand.Int())
			break
		}
	}

	return ip
}
