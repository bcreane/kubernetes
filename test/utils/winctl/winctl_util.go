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
	"os"
	"strings"

	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/test/e2e/framework"

	. "github.com/onsi/ginkgo/config"
	. "github.com/onsi/gomega"
)

//Map to store serviceName and respective endpointIP
var ServiceEndpointIP = map[string]string{}

// Check if we are running windows specific test cases.
func RunningWindowsTest() bool {
	return strings.Contains(GinkgoConfig.FocusString, "WindowsPolicy")
}

// Temporarily disable readiness check for 1903 cluster.
func DisableReadiness() bool {
	return os.Getenv("WINDOWS_OS") == "1903"
}

// Get Porter image based on windows OS version
func GetPorterImage() string {
	os := os.Getenv("WINDOWS_OS")
	if os == "" {
		framework.Failf("WINDOWS_OS env not specified,Please set env properly")
		return ""
	}
	if os == "1809" || os == "1803" || os == "1903" {
		return "caltigera/porter:" + os
	} else {
		framework.Failf("OS Version currently not supported")
	}

	return ""
}

// Get client image and powershell command based on windows OS version
func GetClientImageAndCommand() (string, string) {
	os := os.Getenv("WINDOWS_OS")
	if os == "" {
		framework.Failf("WINDOWS_OS env not specified,Please set env properly")
		return "", ""
	}
	if os == "1809" {
		return "mcr.microsoft.com/windows/servercore:" + os, "powershell.exe"
	} else if os == "1803" {
		return "microsoft/powershell:nanoserver", "C:\\Program Files\\PowerShell\\pwsh.exe"
	} else if os == "1903" {
		return "mcr.microsoft.com/windows/servercore/insider:10.0.18362.113", "powershell.exe"
	} else {
		framework.Failf("OS Version currently not supported")
	}
	return "", ""
}

//This is a hack for windows to use EndpointIP instead of service's
//ClusterIP, Since we have known issue with service's ClusterIP
func GetTarget(f *framework.Framework, service *v1.Service, targetPort int) (string, string) {
	var targetIP string
	//check if serviceEndpointIP is already present in map,else
	//raise a request to get it
	key := fmt.Sprintf("%s-%s", service.Namespace, service.Name)
	if ip, exist := ServiceEndpointIP[key]; exist {
		targetIP = ip
	} else {
		targetIP = getServiceEndpointIP(f, service.Namespace, service.Name)
		ServiceEndpointIP[key] = targetIP
	}
	serviceTarget := fmt.Sprintf("http://%s:%d", service.Spec.ClusterIP, targetPort)
	podTarget := fmt.Sprintf("http://%s:%d", targetIP, targetPort)
	fmt.Printf("podTarget :%s and serviceTarget :%s \n", podTarget, serviceTarget)
	return podTarget, serviceTarget
}

//Since we have a known issue related to service ClusterIP on windows,hence using EndpointIP
// to connect
func getServiceEndpointIP(f *framework.Framework, svcNSName string, svcName string) string {
	var err error
	// The default timeout is 1 minute but sometimes Windows pods take a little longer than that.
	for tries := 3; tries > 0; tries-- {
		err = framework.WaitForEndpoint(f.ClientSet, svcNSName, svcName)
		if err == nil {
			break
		}
	}
	if err != nil {
		framework.Failf("Unable to get endpoint for service %s: %v", svcName, err)
	}
	endpoint, err := f.ClientSet.Core().Endpoints(svcNSName).Get(svcName, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())
	Expect(endpoint.Subsets).To(HaveLen(1), fmt.Sprintf("Failed to find endpoint subset for service %s", svcName))
	Expect(endpoint.Subsets[0].Addresses).To(HaveLen(1), fmt.Sprintf("Failed to find endpoint address for service %s", svcName))
	endpointIP := fmt.Sprintf("%s", endpoint.Subsets[0].Addresses[0].IP)
	framework.Logf("ServiceName: %s endpointIP: %s.", svcName, endpointIP)
	return endpointIP
}

//function to cleanup ServiceName and EndpointIP map
func CleanupServiceEndpointMap() {
	for i := range ServiceEndpointIP {
		delete(ServiceEndpointIP, i)
	}
}
