// Copyright (c) 2018-2019 Tigera, Inc. All rights reserved.

package scheduler

import (
	"net"
	"sync"
)

type Scheduler interface {
	GetPodIP() (ip net.IP, nodeName string)
	ReleaseIP(ip string)
}

type rrNodeScheduler struct {
	nodes         []string
	lastNodeIndex int
}

// staticScheduler has no node affinity and assumes it will never run out of
// addresses
type staticScheduler struct {
	lock sync.Mutex
	rrNodeScheduler
	lastAddr net.IP
}

func NewStaticScheduler(nodes []string, firstAddr net.IP) Scheduler {
	return &staticScheduler{
		sync.Mutex{},
		rrNodeScheduler{nodes, 0},
		firstAddr,
	}
}

func (s *staticScheduler) GetPodIP() (net.IP, string) {
	s.lock.Lock()
	defer s.lock.Unlock()
	ip := nextIP(s.lastAddr)
	s.lastAddr = ip
	return ip, s.getNode()
}

func nextIP(last net.IP) net.IP {
	l := len(last) - 1
	ip := make(net.IP, len(last))
	copy(ip, last)
	if ip[l] == 255 {
		ip[l-1] = ip[l-1] + 1
		ip[l] = 0
	} else {
		ip[l] = ip[l] + 1
	}
	return ip
}

func (s *rrNodeScheduler) getNode() string {
	idx := (s.lastNodeIndex + 1) % len(s.nodes)
	s.lastNodeIndex = idx
	return s.nodes[idx]
}

func (s *staticScheduler) ReleaseIP(ip string) {
	return
}

type cidrScheduler struct {
	lock sync.Mutex
	rrNodeScheduler
	lastAddr net.IP
	pool     net.IPNet
	used     map[string]struct{}
}

func NewCIDRScheduler(nodes []string, cidr net.IPNet) Scheduler {
	return &cidrScheduler{
		lock:            sync.Mutex{},
		rrNodeScheduler: rrNodeScheduler{nodes, 0},
		lastAddr:        cidr.IP.Mask(cidr.Mask),
		pool:            cidr,
		used:            make(map[string]struct{}),
	}
}

func (s *cidrScheduler) GetPodIP() (net.IP, string) {
	s.lock.Lock()
	defer s.lock.Unlock()
	ip := s.nextIP(s.lastAddr)
	for s.usedIP(ip) {
		ip = s.nextIP(ip)
		if ip.Equal(s.lastAddr) {
			// means we've wrapped without finding a free address
			panic("out of IP addresses")
		}
	}
	s.lastAddr = ip
	return ip, s.getNode()
}

func (s *cidrScheduler) nextIP(last net.IP) net.IP {
	ip := nextIP(last)
	if s.pool.Contains(ip) {
		return ip
	}
	// start at beginning
	return s.pool.IP.Mask(s.pool.Mask)
}

func (s *cidrScheduler) usedIP(ip net.IP) bool {
	_, ok := s.used[ip.String()]
	return ok
}

func (s *cidrScheduler) ReleaseIP(ip string) {
	s.lock.Lock()
	defer s.lock.Unlock()
	delete(s.used, ip)
}
