package ids

import (
	"net"
	"net/url"
	"time"
)

func MakeNet(cidr string) *net.IPNet {
	_, net, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err.Error())
	}
	return net
}

func MakeTime(tm string) time.Time {
	t, err := time.Parse(time.RFC3339, tm)
	if err != nil {
		panic(err.Error())
	}
	return t
}

func MakeDate(tm string) time.Time {
	t, err := time.Parse("2006-01-02", tm)
	if err != nil {
		panic(err.Error())
	}
	return t
}

func RoundTimeToDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func MakeURI(spec string) *url.URL {
	u, err := url.Parse(spec)
	if err != nil {
		panic(err.Error())
	}
	return u
}
