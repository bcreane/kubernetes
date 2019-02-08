/*
Copyright 2019 Tigera Inc.
*/

package ids

import "github.com/onsi/ginkgo"

func SIGDescribe(text string, body func()) bool {
	return ginkgo.Describe("[sig-ids] "+text, body)
}
