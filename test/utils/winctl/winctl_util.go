/*
Copyright (c) 2018 Tigera, Inc. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// This package makes public methods out of some of the utility methods for testing windows cluster found at test/e2e/network_policy.go
// Eventually these utilities should replace those and be used for any calico tests

package winctl

import (
        "fmt"
        "k8s.io/api/core/v1"
        metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
        "k8s.io/kubernetes/test/e2e/framework"
	. "github.com/onsi/ginkgo/config"
        . "github.com/onsi/gomega"
)
//Map to store serviceName and respective endpointIP
var ServiceEndpointIP = map[string]string{}
// Check if we are running windows specific test cases.
func RunningWindowsTest () bool {
	if GinkgoConfig.FocusString == "WindowsPolicy" {
		return true
	}
	return false
}

//This is a hack for windows to use EndpointIP instead of service's
//ClusterIP, Since we have known issue with service's ClusterIP
func GetTarget(f *framework.Framework, service *v1.Service, targetPort int )string {
        var targetIP string
        //check if serviceEndpointIP is already present in map,else
        //raise a request to get it
	key := fmt.Sprintf("%s-%s",service.Namespace,service.Name)
        if IP, exist := ServiceEndpointIP[key]; exist {
                targetIP = IP
        } else {
                targetIP = getServiceEndpointIP(f, service.Namespace, service.Name)
                ServiceEndpointIP[key] = targetIP
        }
        return fmt.Sprintf("http://%s:%d", targetIP, targetPort)
}


//Since we have a known issue related to service ClusterIP on windows,hence using EndpointIP
// to connect
func getServiceEndpointIP(f *framework.Framework, svcNSName string, svcName string) string {

        if err := framework.WaitForEndpoint(f.ClientSet, svcNSName , svcName); err != nil {
                framework.Failf("Unable to get endpoint for service %s: %v", svcName, err)
        }
        endpoint, err := f.ClientSet.Core().Endpoints(svcNSName).Get(svcName, metav1.GetOptions{})
        Expect(err).NotTo(HaveOccurred())

        endpointIP := fmt.Sprintf("%s", endpoint.Subsets[0].Addresses[0].IP)
        framework.Logf("ServiceName: %s endpointIP: %s.",svcName ,endpointIP)
        return endpointIP
}


//function to cleanup ServiceName and EndpointIP map
func CleanupServiceEndpointMap() {
	for i := range ServiceEndpointIP {
		delete(ServiceEndpointIP, i)
	}
}
