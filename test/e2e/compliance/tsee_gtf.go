package compliance

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	apiv3 "github.com/projectcalico/libcalico-go/lib/apis/v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"
)

const (
	namespace      = "calico-monitoring"
	uiServiceName  = "cnx-manager"
	serviceAccount = "tigera-compliance-server"
)

var _ = Describe("[Feature:CNX-v3-Compliance-CIS]", func() {
	var f = framework.NewDefaultFramework("cnx-compliance-cis")
	var kubectl *calico.Kubectl

	Context("Compliance CIS benchmarks", func() {
		BeforeEach(func() {
			// Patch compliance-controller deployment to set JOB_START_DELAY=0s
			deploy, err := f.ClientSet.AppsV1().Deployments(namespace).Get("compliance-controller", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			deploy.Spec.Template.Spec.Containers[0].Env = append(deploy.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{Name: "TIGERA_COMPLIANCE_JOB_START_DELAY", Value: "0s"})
			_, err = f.ClientSet.AppsV1().Deployments(namespace).Update(deploy)

			// Wait until all the pods are up and running
			pods, err := f.ClientSet.CoreV1().Pods(namespace).List(metav1.ListOptions{})
			Expect(err).NotTo(HaveOccurred())
			framework.WaitForPodsRunningReady(f.ClientSet, namespace, int32(len(pods.Items)), 0, time.Minute, map[string]string{})
		})

		AfterEach(func() {
			err := kubectl.Delete("globalreports.projectcalico.org", "", "--all", "")
			Expect(err).To(BeNil())
		})

		It("Generates a CIS benchmark report.", func() {
			By("Creating the benchmark report config.")
			err := kubectl.Create(fmt.Sprintf(`
apiVersion: projectcalico.org/v3
kind: GlobalReport
metadata:
  name: tigera-sample-report-cis
spec:
  reportType: "cis-benchmark"
  schedule: "%d/5 * * * *"
  cis:
    includeUnscoredTests: true
    highThreshold: 100
    medThreshold: 50
`, time.Now().Minute()+1), "", "")
			Expect(err).NotTo(HaveOccurred())

			By("Waiting until a job is created for the report")
			Eventually(func() bool {
				output, err := kubectl.Get("globalreports", "", "tigera-sample-report-cis", "", "json", "", false)
				Expect(err).NotTo(HaveOccurred())

				report := new(apiv3.GlobalReport)
				Expect(json.Unmarshal([]byte(output), report)).NotTo(HaveOccurred())

				return len(report.Status.LastSuccessfulReportJobs) > 0
			}, 6*time.Minute, 10*time.Second).Should(BeTrue())

			By("Checking the resulting report")
			// Fetch compliance server service account
			svcAcct, err := f.ClientSet.CoreV1().ServiceAccounts(namespace).Get(serviceAccount, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			Expect(len(svcAcct.Secrets)).To(Equal(1))

			// Fetch compliance server service account token
			secret, err := f.ClientSet.CoreV1().Secrets(namespace).Get(svcAcct.Secrets[0].Name, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())

			// Make request
			req, err := http.NewRequest(http.MethodGet, "https://cnx-manager.calico-monitoring.svc.cluster.local:9443/compliance/reports", nil)
			Expect(err).NotTo(HaveOccurred())
			req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", secret.Data["token"]))

			client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
			res, err := client.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer res.Body.Close()

			// Convert response to array of reports
			raw, err := ioutil.ReadAll(res.Body)
			Expect(err).NotTo(HaveOccurred())

			reports := map[string]interface{}{}
			Expect(json.Unmarshal(raw, &reports)).NotTo(HaveOccurred())

			// Ensure that there is at least 1 report
			reportList := reports["reports"].([]interface{})
			Expect(len(reportList)).To(BeNumerically(">", 0))
		})
	})
})
