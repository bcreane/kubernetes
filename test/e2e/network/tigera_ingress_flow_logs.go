package network

import (
    "bufio"
	"fmt"
	"github.com/olivere/elastic"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"os"
	"time"
	"strings"

	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/ids"
	"k8s.io/kubernetes/test/utils/calico"
	"net"

	certlib "k8s.io/client-go/util/cert"
)

var (
	ingressServicePort                    = "31564"
	ingressPath                           = "nginx"
	ingressServiceHTTPSPort               = "32555"
	ingressHTTPSPath                      = "https"
	ingressServicePortMultipleIC1         = "31561"
	ingressPathMultipleIC1                = "nginx-multiple-ic-1"
	ingressServicePortMultipleIC2         = "31562"
	ingressPathMultipleIC2                = "nginx-multiple-ic-2"
	clientPodName                         = "http-client-pod"
	clientPodImageURL                     = "byrnedo/alpine-curl" //revisit.
	kubectl                                *calico.Kubectl
	esFlowlogsIndex                       = "tigera_secure_ee_flows*"
	deploymentStringHTTP                  = `apiVersion: v1
kind: Namespace
metadata:
  name: ingress-nginx

---
apiVersion: extensions/v1beta1
kind: Ingress
metadata:
  name: nginx-ingress
  namespace: ingress-nginx
  annotations:
    kubernetes.io/ingress.class: "nginx"
    nginx.ingress.kubernetes.io/rewrite-target: /healthz
    nginx.ingress.kubernetes.io/ssl-redirect: "false"
    nginx.ingress.kubernetes.io/ssl-passthrough: "true"
spec:
  rules:
    - http:
        paths:
          - path: /nginx
            backend:
              serviceName: default-backend
              servicePort: 8080
---

apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: default-backend
  namespace: ingress-nginx
spec:
  replicas: 2
  template:
    metadata:
      labels:
        app: default-backend
    spec:
      terminationGracePeriodSeconds: 60
      containers:
        - name: default-backend
          image: gcr.io/google_containers/defaultbackend:1.0
          livenessProbe:
            httpGet:
              path: /healthz
              port: 8080
              scheme: HTTP
            initialDelaySeconds: 30
            timeoutSeconds: 5
          ports:
            - containerPort: 8080
          resources:
            limits:
              cpu: 10m
              memory: 20Mi
            requests:
              cpu: 10m
              memory: 20Mi

---

apiVersion: v1
kind: Service
metadata:
  name: default-backend
  namespace: ingress-nginx
spec:
  ports:
    - port: 80
      protocol: TCP
      targetPort: 8080
  selector:
    app: default-backend

---

apiVersion: v1
kind: ConfigMap
metadata:
  name: nginx-ingress-controller-conf
  namespace: ingress-nginx
  labels:
    app: nginx-ingress-lb
data:
  enable-vts-status: 'true'
  use-forwarded-headers: 'true'
  compute-full-forwarded-for: 'true'
  log-format-upstream: 'tigera_secure_ee_ingress: {"source_port": $realip_remote_port, "destination_ip": "$server_addr", "destination_port": $server_port, "source_ip": "$realip_remote_addr", "x-forwarded-for": "$http_x_forwarded_for", "x-real-ip": "$the_real_ip"}'
  access-log-path: '/var/log/calico/ingress/ingress.log'
---

apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: nginx-ingress-controller
  namespace: ingress-nginx
spec:
  replicas: 1
  revisionHistoryLimit: 3
  template:
    metadata:
      labels:
        app: nginx-ingress-lb
    spec:
      terminationGracePeriodSeconds: 60
      serviceAccount: nginx
      containers:
        - name: ingress-collector 
          image: gcr.io/unique-caldron-775/cnx/tigera/ingress-collector:master
          imagePullPolicy: Always
          env:
          - name: FELIX_DIAL_TARGET
            value: "/var/run/felix/nodeagent/socket"
          - name: LOG_LEVEL
            value: "debug"
          volumeMounts:
          - name: ingress-logs
            mountPath: /var/log/calico/ingress
          - name: felix-sync
            mountPath: /var/run/felix
        - name: nginx-ingress-controller
          image: quay.io/kubernetes-ingress-controller/nginx-ingress-controller:0.24.1
          imagePullPolicy: Always
          securityContext:
            allowPrivilegeEscalation: true
            capabilities:
              drop:
                - ALL
              add:
                - NET_BIND_SERVICE
          readinessProbe:
            httpGet:
              path: /healthz
              port: 10254
              scheme: HTTP
          livenessProbe:
            httpGet:
              path: /healthz
              port: 10254
              scheme: HTTP
            initialDelaySeconds: 10
            timeoutSeconds: 5
          args:
            - /nginx-ingress-controller	
            - --default-backend-service=$(POD_NAMESPACE)/default-backend
            - --configmap=$(POD_NAMESPACE)/nginx-ingress-controller-conf
            - --v=2
          env:
            - name: POD_NAME
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
            - name: POD_NAMESPACE
              valueFrom:
                fieldRef:
                  fieldPath: metadata.namespace
          ports:
            - containerPort: 80
            - containerPort: 18080
          volumeMounts:
          - name: ingress-logs
            mountPath: /var/log/calico/ingress
      volumes:
      - name: ingress-logs
        emptyDir: {}
      - name: felix-sync
        flexVolume:
          driver: nodeagent/uds
---

apiVersion: v1
kind: ServiceAccount
metadata:
  name: nginx
  namespace: ingress-nginx
---
kind: ClusterRole
apiVersion: rbac.authorization.k8s.io/v1beta1
metadata:
  name: nginx-role
rules:
  - apiGroups:
      - ""
    resources:
      - configmaps
      - endpoints
      - nodes
      - pods
      - secrets
    verbs:
      - get
      - create
      - update
      - list
      - watch
  - apiGroups:
      - ""
    resources:
      - nodes
    verbs:
      - get
  - apiGroups:
      - ""
    resources:
      - services
    verbs:
      - get
      - list
      - update
      - watch
  - apiGroups:
      - extensions
    resources:
      - ingresses
    verbs:
      - get
      - list
      - watch
  - apiGroups:
      - ""
    resources:
      - events
    verbs:
      - create
      - patch
  - apiGroups:
      - extensions
    resources:
      - ingresses/status
    verbs:
      - update
---
kind: ClusterRoleBinding
apiVersion: rbac.authorization.k8s.io/v1beta1
metadata:
  name: nginx-role
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: nginx-role
subjects:
  - kind: ServiceAccount
    name: nginx
    namespace: ingress-nginx

---

apiVersion: v1
kind: Service
metadata:
  name: nginx-ingress
  namespace: ingress-nginx
spec:
  type: NodePort
  ports:
    - port: 80
      nodePort: 31564
      name: http
    - port: 18080
      nodePort: 32000
      name: http-mgmt
  selector:
    app: nginx-ingress-lb`

	httpsIngressDeployment = `apiVersion: extensions/v1beta1
kind: Ingress
metadata:
  name: nginx-ingress-https
  namespace: ingress-nginx-https
  annotations:
    kubernetes.io/ingress.class: "nginx"
    nginx.ingress.kubernetes.io/rewrite-target: /index.html
    nginx.ingress.kubernetes.io/ssl-redirect: "false"
    nginx.ingress.kubernetes.io/backend-protocol: "HTTPS"
spec:
  rules:
    - http:
        paths:
          - path: /https
            backend:
              serviceName: nginxsvc
              servicePort: 443
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: nginx-ingress-controller-conf
  namespace: ingress-nginx-https
  labels:
    app: nginx-ingress-lb
data:
  enable-vts-status: 'true'
  use-forwarded-headers: 'true'
  log-format-upstream: 'tigera_secure_ee_ingress: {"source_port": $realip_remote_port, "destination_ip": "$server_addr", "destination_port": $server_port, "source_ip": "$realip_remote_addr", "x-forwarded-for": "$http_x_forwarded_for", "x-real-ip": "$the_real_ip"}'
  access-log-path: '/var/log/calico/ingress/ingress.log'
---

apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: nginx-ingress-controller
  namespace: ingress-nginx-https
spec:
  replicas: 1
  revisionHistoryLimit: 3
  template:
    metadata:
      labels:
        app: nginx-ingress-lb
    spec:
      terminationGracePeriodSeconds: 60
      serviceAccount: nginx
    spec:
      terminationGracePeriodSeconds: 60
      serviceAccount: nginx
      containers:
        - name: ingress-collector 
          image: gcr.io/unique-caldron-775/cnx/tigera/ingress-collector:master
          imagePullPolicy: Always
          env:
          - name: FELIX_DIAL_TARGET
            value: "/var/run/felix/nodeagent/socket"
          - name: LOG_LEVEL
            value: "debug"
          volumeMounts:
          - name: ingress-logs
            mountPath: /var/log/calico/ingress
          - name: felix-sync
            mountPath: /var/run/felix
        - name: nginx-ingress-controller
          image: quay.io/kubernetes-ingress-controller/nginx-ingress-controller:0.24.1
          imagePullPolicy: Always
          securityContext:
            allowPrivilegeEscalation: true
            capabilities:
              drop:
                - ALL
              add:
                - NET_BIND_SERVICE
          readinessProbe:
            httpGet:
              path: /healthz
              port: 10254
              scheme: HTTP
          livenessProbe:
            httpGet:
              path: /healthz
              port: 10254
              scheme: HTTP
            initialDelaySeconds: 10
            timeoutSeconds: 5
          args:
            - /nginx-ingress-controller	
            - --default-backend-service=$(POD_NAMESPACE)/nginxsvc
            - --configmap=$(POD_NAMESPACE)/nginx-ingress-controller-conf
            - --v=2
          env:
            - name: POD_NAME
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
            - name: POD_NAMESPACE
              valueFrom:
                fieldRef:
                  fieldPath: metadata.namespace
          ports:
            - containerPort: 80
            - containerPort: 18080
          volumeMounts:
          - name: ingress-logs
            mountPath: /var/log/calico/ingress
      volumes:
      - name: ingress-logs
        emptyDir: {}
      - name: felix-sync
        flexVolume:
          driver: nodeagent/uds
---

apiVersion: v1
kind: ServiceAccount
metadata:
  name: nginx
  namespace: ingress-nginx-https
---
kind: ClusterRole
apiVersion: rbac.authorization.k8s.io/v1beta1
metadata:
  name: nginx-role-https
rules:
  - apiGroups:
      - ""
    resources:
      - configmaps
      - endpoints
      - nodes
      - pods
      - secrets
    verbs:
      - get
      - create
      - update
      - list
      - watch
  - apiGroups:
      - ""
    resources:
      - nodes
    verbs:
      - get
  - apiGroups:
      - ""
    resources:
      - services
    verbs:
      - get
      - list
      - update
      - watch
  - apiGroups:
      - extensions
    resources:
      - ingresses
    verbs:
      - get
      - list
      - watch
  - apiGroups:
      - ""
    resources:
      - events
    verbs:
      - create
      - patch
  - apiGroups:
      - extensions
    resources:
      - ingresses/status
    verbs:
      - update
---
kind: ClusterRoleBinding
apiVersion: rbac.authorization.k8s.io/v1beta1
metadata:
  name: nginx-role-https
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: nginx-role-https
subjects:
  - kind: ServiceAccount
    name: nginx
    namespace: ingress-nginx-https

---

apiVersion: v1
kind: Service
metadata:
  name: nginx-ingress-https
  namespace: ingress-nginx-https
spec:
  type: NodePort
  ports:
    - port: 443
      nodePort: 32555
      name: https
  selector:
    app: nginx-ingress-lb`

	nginxservicehttps = `apiVersion: v1
kind: Service
metadata:
  name: nginxsvc
  namespace: ingress-nginx-https
  labels:
    app: nginx
spec:
  ports:
  - port: 443
    protocol: TCP
    name: https
    targetPort: 443
  selector:
    app: nginx
---
apiVersion: v1
kind: ReplicationController
metadata:
  name: my-nginx
  namespace: ingress-nginx-https
spec:
  replicas: 1
  template:
    metadata:
      labels:
        app: nginx
    spec:
      volumes:
      - name: secret-volume
        secret:
          secretName: nginxsecret
      - name: configmap-volume
        configMap:
          name: nginxconfigmap
      containers:
      - name: nginxhttps
        image: ymqytw/nginxhttps:1.5
        command: ["/home/auto-reload-nginx.sh"]
        ports:
        - containerPort: 443
        - containerPort: 80
        livenessProbe:
          httpGet:
            path: /index.html
            port: 80
          initialDelaySeconds: 30
          timeoutSeconds: 1
        volumeMounts:
        - mountPath: /etc/nginx/ssl
          name: secret-volume
        - mountPath: /etc/nginx/conf.d
          name: configmap-volume
`
	realIPHeader = "X-Real-IP:3.3.3.3"
	fwdedIPHeader = "X-Forwarded-For:4.4.4.4"

	manifestMultipleIngresses     = `apiVersion: v1
kind: Namespace
metadata:
  name: ingress-nginx-multiple-ic

---
apiVersion: extensions/v1beta1
kind: Ingress
metadata:
  name: nginx-ingress-multiple-ic-1
  namespace: ingress-nginx-multiple-ic
  annotations:
    kubernetes.io/ingress.class: "nginx"
    nginx.ingress.kubernetes.io/rewrite-target: /healthz
    nginx.ingress.kubernetes.io/ssl-redirect: "false"
    nginx.ingress.kubernetes.io/ssl-passthrough: "true"
spec:
  rules:
    - http:
        paths:
          - path: /nginx-multiple-ic-1
            backend:
              serviceName: default-backend-multiple-ic
              servicePort: 8080
---
apiVersion: extensions/v1beta1
kind: Ingress
metadata:
  name: nginx-ingress-multiple-ic-2
  namespace: ingress-nginx-multiple-ic
  annotations:
    kubernetes.io/ingress.class: "nginx"
    nginx.ingress.kubernetes.io/rewrite-target: /healthz
    nginx.ingress.kubernetes.io/ssl-redirect: "false"
    nginx.ingress.kubernetes.io/ssl-passthrough: "true"
spec:
  rules:
    - http:
        paths:
          - path: /nginx-multiple-ic-2
            backend:
              serviceName: default-backend-multiple-ic
              servicePort: 8080
---

apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: default-backend-multiple-ic
  namespace: ingress-nginx-multiple-ic
spec:
  replicas: 2
  template:
    metadata:
      labels:
        app: default-backend-multiple-ic
    spec:
      terminationGracePeriodSeconds: 60
      containers:
        - name: default-backend-multiple-ic
          image: gcr.io/google_containers/defaultbackend:1.0
          livenessProbe:
            httpGet:
              path: /healthz
              port: 8080
              scheme: HTTP
            initialDelaySeconds: 30
            timeoutSeconds: 5
          ports:
            - containerPort: 8080
          resources:
            limits:
              cpu: 10m
              memory: 20Mi
            requests:
              cpu: 10m
              memory: 20Mi

---

apiVersion: v1
kind: Service
metadata:
  name: default-backend-multiple-ic
  namespace: ingress-nginx-multiple-ic
spec:
  ports:
    - port: 80
      protocol: TCP
      targetPort: 8080
  selector:
    app: default-backend-multiple-ic

---

apiVersion: v1
kind: ConfigMap
metadata:
  name: nginx-ingress-controller-conf-multiple-ic
  namespace: ingress-nginx-multiple-ic
  labels:
    app: nginx-ingress-lb-multiple-ic
data:
  enable-vts-status: 'true'
  use-forwarded-headers: 'true'
  compute-full-forwarded-for: 'true'
  use-proxy-protocol: 'false'
  log-format-upstream: 'tigera_secure_ee_ingress: {"source_port": $realip_remote_port, "destination_ip": "$server_addr", "destination_port": $server_port, "source_ip": "$realip_remote_addr", "x-forwarded-for": "$http_x_forwarded_for", "x-real-ip": "$the_real_ip"}'
  access-log-path: '/var/log/calico/ingress/ingress.log'
---

apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: nginx-ingress-controller-multiple-ic
  namespace: ingress-nginx-multiple-ic
spec:
  replicas: 1
  revisionHistoryLimit: 3
  template:
    metadata:
      labels:
        app: nginx-ingress-lb-multiple-ic

    spec:
      terminationGracePeriodSeconds: 60
      serviceAccount: nginx-multiple-ic
      containers:
        - name: ingress-collector
          image: gcr.io/unique-caldron-775/cnx/tigera/ingress-collector:master
          imagePullPolicy: Always
          env:
          - name: FELIX_DIAL_TARGET
            value: "/var/run/felix/nodeagent/socket"
          - name: LOG_LEVEL
            value: "debug"
          volumeMounts:
          - name: ingress-logs
            mountPath: /var/log/calico/ingress
          - name: felix-sync
            mountPath: /var/run/felix
        - name: nginx-ingress-controller
          image: quay.io/kubernetes-ingress-controller/nginx-ingress-controller:0.24.1
          imagePullPolicy: Always
          securityContext:
            allowPrivilegeEscalation: true
            capabilities:
              drop:
                - ALL
              add:
                - NET_BIND_SERVICE
          readinessProbe:
            httpGet:
              path: /healthz
              port: 10254
              scheme: HTTP
          livenessProbe:
            httpGet:
              path: /healthz
              port: 10254
              scheme: HTTP
            initialDelaySeconds: 10
            timeoutSeconds: 5
          args:
            - /nginx-ingress-controller	
            - --default-backend-service=$(POD_NAMESPACE)/default-backend-multiple-ic
            - --configmap=$(POD_NAMESPACE)/nginx-ingress-controller-conf-multiple-ic
            - --v=2
          env:
            - name: POD_NAME
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
            - name: POD_NAMESPACE
              valueFrom:
                fieldRef:
                  fieldPath: metadata.namespace
          ports:
            - containerPort: 80
            - containerPort: 18080
          volumeMounts:
          - name: ingress-logs
            mountPath: /var/log/calico/ingress
      volumes:
      - name: ingress-logs
        emptyDir: {}
      - name: felix-sync
        flexVolume:
          driver: nodeagent/uds
---

apiVersion: v1
kind: ServiceAccount
metadata:
  name: nginx-multiple-ic
  namespace: ingress-nginx-multiple-ic
---
kind: ClusterRole
apiVersion: rbac.authorization.k8s.io/v1beta1
metadata:
  name: nginx-role-multiple-ic
rules:
  - apiGroups:
      - ""
    resources:
      - configmaps
      - endpoints
      - nodes
      - pods
      - secrets
    verbs:
      - get
      - create
      - update
      - list
      - watch
  - apiGroups:
      - ""
    resources:
      - nodes
    verbs:
      - get
  - apiGroups:
      - ""
    resources:
      - services
    verbs:
      - get
      - list
      - update
      - watch
  - apiGroups:
      - extensions
    resources:
      - ingresses
    verbs:
      - get
      - list
      - watch
  - apiGroups:
      - ""
    resources:
      - events
    verbs:
      - create
      - patch
  - apiGroups:
      - extensions
    resources:
      - ingresses/status
    verbs:
      - update
---
kind: ClusterRoleBinding
apiVersion: rbac.authorization.k8s.io/v1beta1
metadata:
  name: nginx-role-multiple-ic
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: nginx-role-multiple-ic
subjects:
  - kind: ServiceAccount
    name: nginx-multiple-ic
    namespace: ingress-nginx-multiple-ic

---

apiVersion: v1
kind: Service
metadata:
  name: nginx-ingress-multiple-ic-1
  namespace: ingress-nginx-multiple-ic
spec:
  type: NodePort
  ports:
    - port: 80
      nodePort: 31561
      name: http
    - port: 18080
      nodePort: 32001
      name: http-mgmt
  selector:
    app: nginx-ingress-lb-multiple-ic
---

apiVersion: v1
kind: Service
metadata:
  name: nginx-ingress-multiple-ic-2
  namespace: ingress-nginx-multiple-ic
spec:
  type: NodePort
  ports:
    - port: 80
      nodePort: 31562
      name: http
    - port: 18080
      nodePort: 32002
      name: http-mgmt
  selector:
    app: nginx-ingress-lb-multiple-ic`)

func initializeSetup(f *framework.Framework)  *elastic.Client {
	if os.Getenv("ELASTIC_HOST") == "" {
		os.Setenv("ELASTIC_HOST",  "127.0.0.1")
	}

	esclient := ids.InitClient(ids.GetURI())


	//setup ALP here.
	calicoctl := calico.ConfigureCalicoctl(f)

	result, err := calicoctl.ExecReturnError("get", "felixconfiguration", "default", "-o", "yaml", "--export")
	if err != nil {
		framework.Failf("Error with calicoctl command: %s", result)
	}

	temp := strings.TrimSpace(result)

	temp = temp + "\n  policySyncPathPrefix: /var/run/nodeagent" + "\n  flowLogsFileAggregationKindForAllowed: 1" + "\n  flowLogsFlushInterval: 1s"
	calicoctl.Apply(temp)
	return esclient
}

func resetFelixConfig(f *framework.Framework) {
	var res string

	calicoctl := calico.ConfigureCalicoctl(f)

	result, err := calicoctl.ExecReturnError("get", "felixconfiguration", "default", "-o", "yaml", "--export")
	if err != nil {
		framework.Failf("Error with calicoctl command: %s", result)
	}

	temp := strings.TrimSpace(result)

	scanner := bufio.NewScanner(strings.NewReader(temp))
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "policySyncPathPrefix: /var/run/nodeagent") || strings.Contains(scanner.Text(), "flowLogsFileAggregationKindForAllowed: 1") || strings.Contains(scanner.Text(), "flowLogsFlushInterval: 1s") {
			//ignore this config line.
			continue
		}

		res += "\n" + scanner.Text()
	}

	calicoctl.Apply(res)
}

var _ = SIGDescribe("[Feature:CNX-v3-IngressFlowLogs]", func() {
	var (
		f        = framework.NewDefaultFramework("cnx-ingress-flow-logs")
		esclient *elastic.Client
	)

	Context("Test ingress flow logs for HTTP traffic", func() {
		BeforeEach(func() {
			//cleanup if already resources present and re-create the setup.
			_, err := f.ClientSet.CoreV1().Namespaces().Get("ingress-nginx", metav1.GetOptions{})
			if err == nil {
				cleanupHTTPServiceDeployment()
			}

			esclient = initializeSetup(f)
		})

		It("Sends HTTP traffic and validates logs in elasticsearch", func() {

			//create ingress controller, ingress resources, service and pods for  service.
			setupHTTPService()

			By("Sending HTTP traffic to service", func() {
				//create a pod to send traffic to the service and check pod created / sent traffic and exited fine.
				_ = testForProtocolFlowLogs(f, "ingress-nginx", clientPodName, ingressServicePort, ingressPath, false)
			})

			By("Searching for logs related to traffic in elasticsearch logs", func() {
				//need to define the key here - based on final log format - the key would change - for example, key
				//could be sourceIP and expected value be pod IP.
				key := "dest_port" //TBD.
				expectedValue := "31564"

				ids.CheckSearchEvents(esclient, esFlowlogsIndex, key, expectedValue)
			})
		})

		AfterEach(func() {
			resetFelixConfig(f)
			time.Sleep(time.Second* 10)
			framework.Logf("Cleanup ingress flow HTTP test setup")
			_, err := f.ClientSet.CoreV1().Namespaces().Get("ingress-nginx", metav1.GetOptions{})
			if err == nil {
				cleanupHTTPServiceDeployment()
			}
		}, 1)
	})

	Context("Test ingress flow logs for HTTPS traffic", func() {
		BeforeEach(func() {
			_, err := f.ClientSet.CoreV1().Namespaces().Get("ingress-nginx-https", metav1.GetOptions{})
			if err == nil {
				deleteConfigMap(f, "nginxconfigmap", "ingress-nginx-https")
				deleteSecretForHTTPSService(f, "ingress-nginx-https", "nginxsecret")
				cleanupHTTPSService()
				cleanupHTTPSIngressDeployment()
			}

			esclient = initializeSetup(f)
		})

		AfterEach(func() {
			resetFelixConfig(f)
			time.Sleep(time.Second * 10)
			framework.Logf("Cleanup ingress flow HTTPS traffic setup")
			_, err := f.ClientSet.CoreV1().Namespaces().Get("ingress-nginx-https", metav1.GetOptions{})
			if err == nil {
				deleteConfigMap(f, "nginxconfigmap", "ingress-nginx-https")
				deleteSecretForHTTPSService(f, "ingress-nginx-https", "nginxsecret")
				cleanupHTTPSService()
				cleanupHTTPSIngressDeployment()
			}
		}, 1)

		It("Sends HTTPs traffic and validates logs in Elasticsearch", func() {
			_, err := f.ClientSet.CoreV1().Namespaces().Create(&v1.Namespace{metav1.TypeMeta{}, metav1.ObjectMeta{Name: "ingress-nginx-https"}, v1.NamespaceSpec{}, v1.NamespaceStatus{}})
			if err != nil {
				framework.Logf("FAILED to create Namespace: %v, err: %v", "ingress-nginx-https",err)
			}

			//generate certificate, keys, configmap for HTTPS service
			generateSelfSignedCertAndKeyForService(f)
			createConfigMapForHTTPsService(f, "ingress-nginx-https")

			//create ingress controller / ingress resources for HTTPS deployment
			createHTTPSIngressDeployment()

			By("Sending HTTPS traffic to service", func() {
				//create a pod to send traffic to the service and check pod created / sent traffic and exited fine.
				_ = testForProtocolFlowLogs(f, "ingress-nginx-https", clientPodName, ingressServiceHTTPSPort, ingressHTTPSPath, true)
			})

			By("Searching for logs related to HTTPS traffic in elasticsearch logs", func() {
				//need to define the key here - based on final log format - the key would change - for example, key
				//could be sourceIP and expected value be pod IP.

				key := "dest_port" //TBD.
				expectedValue := "32555"
				ids.CheckSearchEvents(esclient, esFlowlogsIndex, key, expectedValue)
			})

			err = f.ClientSet.CoreV1().Namespaces().Delete("ingress-nginx-https", &metav1.DeleteOptions{})
			if err != nil {
				framework.Logf("FAILED to create Namespace: %v, err: %v", "ingress-nginx-https",err)
			}
		})
	})

	Context("Test multiple ingresses flow logs", func() {
		BeforeEach(func() {
			_, err := f.ClientSet.CoreV1().Namespaces().Get("ingress-nginx-multiple-ic", metav1.GetOptions{})
			if err == nil {
				cleanupMultipleIngressesScenario()
			}

			esclient = initializeSetup(f)
		})

		It("Sends HTTP traffic and validate logs in Elasticsearch for multi ingresses", func() {
			//create multiple ingresses for same service.
			createMultipleIngressesScenario()
			By("Sending HTTP traffic to service", func() {
				//create a pod to send traffic to the service and check pod created / sent traffic and exited fine.
				_ = testForProtocolFlowLogs(f, "ingress-nginx-multiple-ic", clientPodName, ingressServicePortMultipleIC1, ingressPathMultipleIC1, false)
				_ = testForProtocolFlowLogs(f, "ingress-nginx-multiple-ic", clientPodName, ingressServicePortMultipleIC2, ingressPathMultipleIC2, false)
			})

			By("Searching for logs related to traffic in elasticsearch logs", func() {
				//need to define the key here - based on final log format - the key would change - for example, key
				//could be sourceIP and expected value be pod IP.
				key := "dest_port" //TBD.
				expectedValue := ingressServicePortMultipleIC1

				ids.CheckSearchEvents(esclient, esFlowlogsIndex, key, expectedValue)

				key = "dest_port" //TBD.
				expectedValue = ingressServicePortMultipleIC2

				ids.CheckSearchEvents(esclient, esFlowlogsIndex, key, expectedValue)
			})
		})

		AfterEach(func() {
			resetFelixConfig(f)
			time.Sleep(time.Second * 10)
			framework.Logf("Cleaning up multiple ingress controller setup")
			_, err := f.ClientSet.CoreV1().Namespaces().Get("ingress-nginx-multiple-ic", metav1.GetOptions{})
			if err == nil {
				cleanupMultipleIngressesScenario()
			}
		}, 1)
	})
})

//based on isHTTPS flag value - curl http or https to the backend service.
func testForProtocolFlowLogs(f *framework.Framework, namespace, podName, servicePort, servicePath string, isHTTPS bool) string {
	var clientPod *v1.Pod

	//ensure pods are running in the namespace.
	waitForPodsInNamespace(f, namespace)

	clientPod = createHTTPClientPod(f, podName, servicePort, servicePath ,isHTTPS)

	err := framework.WaitForPodSuccessInNamespace(f.ClientSet, clientPod.Name, "default")
	if err != nil {
		framework.Logf("Failed to succeed pod: %v", err)
	}
	Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("checking %s could communicate with server.", clientPod.Name))

	return clientPod.Status.PodIP
}

func createHTTPClientPod(f *framework.Framework, clientPodName, servicePort, servicePath string, isHTTPS bool) *v1.Pod {
	//use IP from internal IPs to build curl command.
	IPs := getNodeInternalIPs(f)
	framework.Logf("Private IPs are: %+v", IPs)

	//TODO - randomly pick IP from IPs instead of hard coded.
	privateIP := IPs[1]

	cmdArgs := []string{}
	cmdArgs = append(cmdArgs, "-c")
	if !isHTTPS {
		cmdArgs = append(cmdArgs, fmt.Sprintf("curl http://%s:%s/%s -H '%s' -H '%s' -w 1 --retry 100", privateIP, servicePort, servicePath, realIPHeader, fwdedIPHeader))
	} else {
		cmdArgs = append(cmdArgs, fmt.Sprintf("curl https://%s:%s/%s -H \"%s\" -H \"%s\" --insecure", privateIP, servicePort, servicePath, realIPHeader, fwdedIPHeader))
	}

	clientPod := createCurlClientPod(f, "default", clientPodName, clientPodImageURL, cmdArgs)
	return clientPod
}

func createCurlClientPod(f *framework.Framework, namespace, podName, imageURL string, cmdArgs []string) *v1.Pod {
	var nodeselector = map[string]string{}

	cleanupCurlClientPod(f, podName)

	time.Sleep(time.Second * 30)
	framework.Logf("createCurlClientPod: cmdArgs:%v", cmdArgs)
	pod, err := f.ClientSet.CoreV1().Pods(namespace).Create(&v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
			Labels: map[string]string{
				"pod-name": podName,
			},
		},
		Spec: v1.PodSpec{
			RestartPolicy: v1.RestartPolicyNever,
			NodeSelector:  nodeselector,
			Containers: []v1.Container{
				{
					Name:            fmt.Sprintf("%s-container", podName),
					Image:           imageURL,
					Args:            cmdArgs,
					Command: []string{"/bin/sh"},
					ImagePullPolicy: v1.PullAlways,
				},
			},
		},
	})
	if err != nil {
		framework.Logf("Failed to create pod: %v", err)
	}
	Expect(err).To(BeNil())
	return pod
}

//cleanup the test pod created.
func cleanupCurlClientPod(f *framework.Framework, podname string) {
	framework.Logf("Cleaning up the http test client pod %s", podname)
	if err := f.ClientSet.CoreV1().Pods("default").Delete(podname, nil); err != nil {
		framework.Logf("unable to cleanup http test client pod %v: %v", podname, err)
	}
}

func getNodeInternalIPs(f *framework.Framework) []string {
	nodeInterface := f.ClientSet.CoreV1().Nodes()
	var ips []string
	list, err := nodeInterface.List(metav1.ListOptions{})
	if err != nil {
		framework.Failf("Failed to get list of nodes: %v", err)
	}

	for _, n := range list.Items {
		for _, a := range n.Status.Addresses {
			if a.Type == v1.NodeInternalIP {
				ips = append(ips, a.Address)
			}
		}
	}
	return ips
}

//create ingress controller, ingress resources, service and pods for https service.
func setupHTTPService() error {
	//create ingress resources and ingress controller service and deployments
	err := kubectl.Create(deploymentStringHTTP, "", "")
	if err != nil {
		framework.Logf("Failed to setup ingress controller: %v", err)
	}
	Expect(err).To(BeNil())
	return err
}

//cleanup services, ingress-controller and ingress resources
func cleanupHTTPServiceDeployment() error {
	err := kubectl.DeleteYaml(deploymentStringHTTP, "", "")
	if err != nil {
		framework.Logf("Failed to cleanup ingress controller and backend deployment: %v", err)
	}
	return err
}

//generate and write certificate an key files in the default path.
func generateSelfSignedCertAndKeyForService(f *framework.Framework) {
	cert, key, err := certlib.GenerateSelfSignedCertKey("", []net.IP{}, []string{"nginxsvc"})
	if err != nil {
		framework.Logf("Error creating cert: %v", err)
	}
	Expect(err).To(BeNil())

	createSecretForHTTPSService(f, "ingress-nginx-https", "nginxsecret", key, cert)
}

func deleteSecretForHTTPSService(f *framework.Framework, namespace, secretname string) {
	err := f.ClientSet.CoreV1().Secrets(namespace).Delete(secretname, &metav1.DeleteOptions{})
	if err != nil {
		framework.Logf("Failed to delete secret %s in namespace %s due error: %v", secretname, namespace, err)
	}
	framework.Logf("Deleted secret %s in namespace %s", secretname, namespace)
}

func createSecretForHTTPSService(f *framework.Framework, namespace, secretname string, key, cert []byte) {
	data := make(map[string][]byte, 0)
	data[v1.TLSPrivateKeyKey] = key
	data[v1.TLSCertKey] = cert
	_, err := f.ClientSet.CoreV1().Secrets(namespace).Create(&v1.Secret{metav1.TypeMeta{}, metav1.ObjectMeta{Name:secretname}, data, map[string]string{}, v1.SecretTypeTLS })
	if err != nil {
		framework.Logf("Failed to create secret: %v", err)
	}
}

func deleteConfigMap(f *framework.Framework, cm, namespace string) {
	err := f.ClientSet.CoreV1().ConfigMaps(namespace).Delete(cm, &metav1.DeleteOptions{})
	if err != nil {
		framework.Logf("Failed to delete configmap %s in namespace %s due error: %v", cm, namespace, err)
	}
	framework.Logf("Deleted configmap %s in namespace %s", cm, namespace)
}

var configText string = `server {
        listen 80 default_server;
        listen [::]:80 default_server ipv6only=on;

        listen 443 ssl;

        root /usr/share/nginx/html;
        index index.html;

        server_name localhost;
        ssl_certificate /etc/nginx/ssl/tls.crt;
        ssl_certificate_key /etc/nginx/ssl/tls.key;

        location / {
                try_files $uri $uri/ =404;
        }
}`

func createConfigMapForHTTPsService(f *framework.Framework, namespace string) {
	framework.Logf("creating configmap for HTTPS service")
	data := make(map[string]string, 0)
	data["default.conf"] = configText
	bindata := map[string][]byte{}
	_, err := f.ClientSet.CoreV1().ConfigMaps(namespace).Create(&v1.ConfigMap{metav1.TypeMeta{}, metav1.ObjectMeta{Name:"nginxconfigmap"}, data, bindata})
	if err != nil {
		framework.Logf("Failed to create configmap for HTTPS service: %v", err)
	}
}

func createHTTPSIngressDeployment() {
	createHTTPSService()
	//create ingress resources and ingress controller deployments for HTTPS
	err := kubectl.Create(httpsIngressDeployment, "", "")
	if err != nil {
		framework.Logf("Failed to setup ingress controller for HTTPS: %v", err)
	}
	Expect(err).To(BeNil())
}

func cleanupHTTPSIngressDeployment() {
	err := kubectl.DeleteYaml(httpsIngressDeployment, "", "")
	if err != nil {
		framework.Logf("Failed to cleanup HTTPS ingress controller and backend deployment: %v", err)
	}
}

//creates HTTPs service
func createHTTPSService() {
	framework.Logf("Creating nginxservicehttps for serving HTTPs traffic")
	err := kubectl.Create(nginxservicehttps, "", "")
	if err != nil {
		framework.Logf("Failed to create service: %v", err)
	}
	Expect(err).To(BeNil())
}

//cleanup HTTPs service - nginxservicehttps
func cleanupHTTPSService() error {
	framework.Logf("Cleanup nginxservicehttps for serving HTTPs traffic")
	err := kubectl.DeleteYaml(nginxservicehttps, "", "")
	return err
}

func createMultipleIngressesScenario()  {
	framework.Logf("Creating multiple Ingresses for serving HTTP traffic")
	err := kubectl.Create(manifestMultipleIngresses, "", "")
	if err != nil {
		framework.Logf("Failed to create service: %v", err)
	}
	Expect(err).To(BeNil())
}

func cleanupMultipleIngressesScenario() {
	err := kubectl.DeleteYaml(manifestMultipleIngresses, "", "")
	if err != nil {
		framework.Logf("Failed to cleanup Multiple Ingresses and backend deployment: %v", err)
	}
}

//wait for pods to be running to proceed with tests.
func waitForPodsInNamespace (f *framework.Framework, namespace string)   {
	var podlist *v1.PodList
	var err error

	podlist, err = f.ClientSet.CoreV1().Pods(namespace).List(metav1.ListOptions{})
	Expect(err).To(BeNil())

	for _, p := range podlist.Items {
		err = framework.WaitTimeoutForPodRunningInNamespace(f.ClientSet, p.ObjectMeta.Name, namespace, time.Second * 15)
		Expect(err).To(BeNil())
	}
}
