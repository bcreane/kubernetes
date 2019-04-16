package ids

import (
	"context"
	"net"
	"net/url"
	"time"

	"github.com/olivere/elastic"

	"k8s.io/kubernetes/test/e2e/framework"
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

func LogElasticDiags(c *elastic.Client, _ *framework.Framework) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ch, err := c.ClusterHealth().Pretty(true).Do(ctx)
	if err != nil {
		framework.Logf("failed to get elasticsearch cluster health")
	} else {
		framework.Logf("elastic cluster health:\n %v", ch)
	}
	r, err := c.PerformRequest(ctx, elastic.PerformRequestOptions{
		Method: "GET",
		Path:   "_cat/nodes?v&h=id,disk.total,disk.used_percent,heap.percent,uptime,ram.percent",
	})
	if err != nil {
		framework.Logf("failed to get elasticsearch node info")
	} else {
		framework.Logf("elastic nodes:\n %v", r)
	}
}
