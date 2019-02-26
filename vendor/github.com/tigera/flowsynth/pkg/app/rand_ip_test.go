package app

import (
	"math/rand"
	"net"
	"testing"
)

func TestRandIP(t *testing.T) {
	_, n, err := net.ParseCIDR(defaultIPNet)

	if err != nil {
		t.Fatalf("Could not parse %v: %v", defaultIPNet, err)
	}

	a := randIP()

	if !n.Contains(a) {
		t.Errorf("%v does not contain %v", n, a)
	}
}

func TestRandIPMatches(t *testing.T) {
	maxTries := 2

	a := randIP()

	for i := 0; i < maxTries; i++ {
		b := randIP()
		if !a.Equal(b) {
			return
		}
	}

	t.Errorf("randIP returned %v %v times in a row", a, 1+maxTries)
}

func TestRandIPWithNet(t *testing.T) {
	cidr := "192.0.2.0/24"
	_, n, err := net.ParseCIDR(cidr)

	if err != nil {
		t.Fatalf("Could not parse %v: %v", cidr, err)
	}

	a := randIP(n)

	if !n.Contains(a) {
		t.Errorf("%v does not contain %v", n, a)
	}
}

func TestRandIPWithIP(t *testing.T) {
	cidr := "192.0.2.0"
	n := &net.IPNet{
		net.ParseIP(cidr),
		net.CIDRMask(32, 32),
	}

	a := randIP(n)

	if !n.IP.Equal(a) {
		t.Errorf("%v does not match %v", a, n)
	}
}

func TestRandIPWithZero(t *testing.T) {
	cidr := "0.0.0.0"
	n := &net.IPNet{
		net.ParseIP(cidr),
		net.CIDRMask(0, 32),
	}

	randIP(n)
}

func TestRandIPWithTwoNets(t *testing.T) {
	rand.Seed(1)
	maxTries := 10

	cidr1 := "192.0.2.0/25"
	_, n1, err := net.ParseCIDR(cidr1)

	if err != nil {
		t.Fatalf("Could not parse %v: %v", cidr1, err)
	}

	cidr2 := "192.0.2.128/25"
	_, n2, err := net.ParseCIDR(cidr2)

	if err != nil {
		t.Fatalf("Could not parse %v: %v", cidr2, err)
	}

	var foundN1, foundN2 bool

	for i := 0; i < maxTries; i++ {
		a := randIP(n1, n2)

		if n1.Contains(a) {
			foundN1 = true
		} else if n2.Contains(a) {
			foundN2 = true
		} else {
			t.Errorf("%v was not in %v or %v", a, n1, n2)
		}

		if foundN1 && foundN2 {
			return
		}
	}

	if !foundN1 {
		t.Errorf("Did not get a random IP in %v", n1)
	}

	if !foundN2 {
		t.Errorf("Did not get a random IP in %v", n2)
	}
}

func TestRandIPFillsAllBits(t *testing.T) {
	rand.Seed(1)
	iterations := 100

	n := &net.IPNet{
		make(net.IP, 4),
		net.CIDRMask(0, 32),
	}

	a := randIP(n)

	for iterations > 0 {
		iterations--

		b := randIP(n)

		if len(a) != len(b) {
			t.Fatalf("len(a): %v != len(b):  %v", len(a), len(b))
		}
		for i := range a {
			a[i] |= b[i]
		}
	}

	for i, v := range a {
		if v != 0xff {
			t.Errorf("Did not fill all bits in a[%d] = %x", i, v)
		}
	}
}

func TestRandIPFillsCorrectBits(t *testing.T) {
	rand.Seed(1)
	iterations := 100

	n := &net.IPNet{
		make(net.IP, 4),
		net.CIDRMask(3, 32),
	}

	a := randIP(n)

	for iterations > 0 {
		iterations--

		b := randIP(n)

		a[0] |= b[0]
	}

	if a[0] != 0x1f {
		t.Errorf("Did not fill all bits in a[%d] = %x", 0, a[0])
	}
}
