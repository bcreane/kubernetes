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
	"context"

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
  log-format-upstream: 'tigera_secure_ee_ingress: {"source_port": $realip_remote_port, "destination_ip": "$server_addr", "destination_port": $server_port, "source_ip": "$realip_remote_addr", "x-forwarded-for": "$http_x_forwarded_for", "x-real-ip": "$http_x_real_ip"}'
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
      imagePullSecrets:
      - name: cnx-pull-secret
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
  log-format-upstream: 'tigera_secure_ee_ingress: {"source_port": $realip_remote_port, "destination_ip": "$server_addr", "destination_port": $server_port, "source_ip": "$realip_remote_addr", "x-forwarded-for": "$http_x_forwarded_for", "x-real-ip": "$http_x_real_ip"}'
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
      imagePullSecrets:
      - name: cnx-pull-secret
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
  name: nginx-ingress-controller-conf-multiple-ic-1
  namespace: ingress-nginx-multiple-ic
  labels:
    app: nginx-ingress-lb-multiple-ic-1
data:
  enable-vts-status: 'true'
  use-forwarded-headers: 'true'
  compute-full-forwarded-for: 'true'
  use-proxy-protocol: 'false'
  log-format-upstream: 'tigera_secure_ee_ingress: {"source_port": $realip_remote_port, "destination_ip": "$server_addr", "destination_port": $server_port, "source_ip": "$realip_remote_addr", "x-forwarded-for": "$http_x_forwarded_for", "x-real-ip": "$http_x_real_ip"}'
  access-log-path: '/var/log/calico/ingress/ingress.log'
---

apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: nginx-ingress-controller-multiple-ic-1
  namespace: ingress-nginx-multiple-ic
spec:
  replicas: 1
  revisionHistoryLimit: 3
  template:
    metadata:
      labels:
        app: nginx-ingress-lb-multiple-ic-1

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
            - --configmap=$(POD_NAMESPACE)/nginx-ingress-controller-conf-multiple-ic-1
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
      imagePullSecrets:
      - name: cnx-pull-secret
      volumes:
      - name: ingress-logs
        emptyDir: {}
      - name: felix-sync
        flexVolume:
          driver: nodeagent/uds
---

apiVersion: v1
kind: ConfigMap
metadata:
  name: nginx-ingress-controller-conf-multiple-ic-2
  namespace: ingress-nginx-multiple-ic
  labels:
    app: nginx-ingress-lb-multiple-ic-2
data:
  enable-vts-status: 'true'
  use-forwarded-headers: 'true'
  compute-full-forwarded-for: 'true'
  use-proxy-protocol: 'false'
  log-format-upstream: 'tigera_secure_ee_ingress: {"source_port": $realip_remote_port, "destination_ip": "$server_addr", "destination_port": $server_port, "source_ip": "$realip_remote_addr", "x-forwarded-for": "$http_x_forwarded_for", "x-real-ip": "$http_x_real_ip"}'
  access-log-path: '/var/log/calico/ingress/ingress.log'
---

apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: nginx-ingress-controller-multiple-ic-2
  namespace: ingress-nginx-multiple-ic
spec:
  replicas: 1
  revisionHistoryLimit: 3
  template:
    metadata:
      labels:
        app: nginx-ingress-lb-multiple-ic-2

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
            - --configmap=$(POD_NAMESPACE)/nginx-ingress-controller-conf-multiple-ic-2
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
      imagePullSecrets:
      - name: cnx-pull-secret
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
    app: nginx-ingress-lb-multiple-ic-1
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
    app: nginx-ingress-lb-multiple-ic-2`)

const (
	endTimeField              = "end_time" //field in elasticsearch document end_time. Used for Time range based query.
)

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
			var start time.Time
			var x_real_ip_hdr string = "X-Real-IP: 3.3.3.3"
			//create ingress controller, ingress resources, service and pods for  service.
			setupHTTPService(f)

			By("Sending HTTP traffic to service", func() {
				//create a pod to send traffic to the service and check pod created / sent traffic and exited fine.
				start = time.Now()
				_ = testForProtocolFlowLogs(f, "ingress-nginx", clientPodName, ingressServicePort, ingressPath, x_real_ip_hdr, false)
			})

			By("Searching for logs related to traffic in elasticsearch logs", func() {
				end := start.Add(time.Minute * 2)

				searchES(esclient, esFlowlogsIndex, &start, &end, "3.3.3.3")
			})
		})

		AfterEach(func() {
			cleanupCurlClientPod(f, clientPodName)
			resetFelixConfig(f)
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
			cleanupCurlClientPod(f, clientPodName)
			resetFelixConfig(f)
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
			var x_real_ip_hdr string = "X-Real-IP: 5.5.5.5"
			var start time.Time

			_, err := f.ClientSet.CoreV1().Namespaces().Create(&v1.Namespace{metav1.TypeMeta{}, metav1.ObjectMeta{Name: "ingress-nginx-https"}, v1.NamespaceSpec{}, v1.NamespaceStatus{}})
			if err != nil {
				framework.Logf("FAILED to create Namespace: %v, err: %v", "ingress-nginx-https",err)
			}

			//generate certificate, keys, configmap for HTTPS service
			generateSelfSignedCertAndKeyForService(f)
			createConfigMapForHTTPsService(f, "ingress-nginx-https")

			//create ingress controller / ingress resources for HTTPS deployment
			createHTTPSIngressDeployment(f)

			By("Sending HTTPS traffic to service", func() {
				start = time.Now()
				//create a pod to send traffic to the service and check pod created / sent traffic and exited fine.
				_ = testForProtocolFlowLogs(f, "ingress-nginx-https", clientPodName, ingressServiceHTTPSPort, ingressHTTPSPath, x_real_ip_hdr, true)
			})

			By("Searching for logs related to HTTPS traffic in elasticsearch logs", func() {
				end := start.Add(time.Minute * 2)
				searchES(esclient, esFlowlogsIndex, &start, &end, "5.5.5.5")
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
			var (
				x_real_ip_hdr1 string = "X-Real-IP: 7.7.7.7"
				x_real_ip_hdr2 string = "X-Real-IP: 9.9.9.9"
				start time.Time
			)

			//create multiple ingresses for same service.
			createMultipleIngressesScenario(f)
			By("Sending HTTP traffic to service", func() {
				start = time.Now()
				//create a pod to send traffic to the service and check pod created / sent traffic and exited fine.
				_ = testForProtocolFlowLogs(f, "ingress-nginx-multiple-ic", clientPodName, ingressServicePortMultipleIC1, ingressPathMultipleIC1, x_real_ip_hdr1, false)
				_ = testForProtocolFlowLogs(f, "ingress-nginx-multiple-ic", clientPodName, ingressServicePortMultipleIC2, ingressPathMultipleIC2, x_real_ip_hdr2, false)
			})

			By("Searching for logs related to traffic in elasticsearch logs", func() {
				end := start.Add(time.Minute * 2)
				searchES(esclient, esFlowlogsIndex, &start, &end, "7.7.7.7")

				searchES(esclient, esFlowlogsIndex, &start, &end, "9.9.9.9")
			})
		})

		AfterEach(func() {
			cleanupCurlClientPod(f, clientPodName)
			resetFelixConfig(f)
			framework.Logf("Cleaning up multiple ingress controller setup")
			_, err := f.ClientSet.CoreV1().Namespaces().Get("ingress-nginx-multiple-ic", metav1.GetOptions{})
			if err == nil {
				cleanupMultipleIngressesScenario()
			}
		}, 1)
	})
})

//query ES for ip until found with timeout
func searchES(esclient *elastic.Client, index string, start, end *time.Time, original_source_ips string) {
	queries := buildIngressFlowLogQuery(start, end, original_source_ips)

	framework.Logf("searchES: client: %+v index: %v start:%v end:%v original_source_ips:%v", esclient, index, start.String(), end.String(), original_source_ips)
	Eventually(func() bool {
		return foundInES(esclient, index, start, end, original_source_ips, queries)
	}, 5*time.Minute, 3*time.Second).Should(BeTrue())
}

//check if expected original source ip is found in ES
func foundInES(esclient *elastic.Client, index string, start, end *time.Time, original_source_ips string, queries elastic.Query) bool {
	searchResult, err := (esclient.Search().
		Index(esFlowlogsIndex).
		Size(0).
		Query(queries).
		Do(context.Background()))
	if err != nil {
		framework.Logf("Failed to search: error: %v", err)
	}
	return searchResult.Hits.TotalHits > 0
}

//Build a boolean query for ingress flow logs
func buildIngressFlowLogQuery(start, end *time.Time, original_source_ips string) elastic.Query {
	queries := []elastic.Query{}

	if start != nil && end != nil {
		withinTimeRange := elastic.NewRangeQuery(endTimeField)
		if start != nil {
			withinTimeRange = withinTimeRange.From((*start).Unix())
		}
		if end != nil {
			withinTimeRange = withinTimeRange.To((*end).Unix())
		}
		queries = append(queries, withinTimeRange)
	}

	tq := elastic.NewTermsQuery("original_source_ips", original_source_ips)
	queries = append(queries, tq)

	return elastic.NewBoolQuery().Must(queries...)
}

func initializeSetup(f *framework.Framework)  *elastic.Client {
	if os.Getenv("ELASTIC_HOST") == "" {
		os.Setenv("ELASTIC_HOST",  "127.0.0.1")
	}

	esclient := ids.InitClient(ids.GetURI())


	//setup connection to Felix via the Policy Sync API
	calicoctl := calico.ConfigureCalicoctl(f)

	result, err := calicoctl.ExecReturnError("get", "felixconfiguration", "default", "-o", "yaml", "--export")
	if err != nil {
		framework.Failf("Error with calicoctl command: %s", result)
	}

	temp := strings.TrimSpace(result)

	temp = temp + "\n  policySyncPathPrefix: /var/run/nodeagent"
	calicoctl.Apply(temp)

	calico.SetCalicoNodeEnvironmentWithRetry(f.ClientSet, "FELIX_FLOWLOGSFLUSHINTERVAL", "1")
	calico.SetCalicoNodeEnvironmentWithRetry(f.ClientSet, "FELIX_FLOWLOGSFILEAGGREGATIONKINDFORALLOWED", "1")
	calico.RestartCalicoNodePods(f.ClientSet, "")

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
		if strings.Contains(scanner.Text(), "policySyncPathPrefix: /var/run/nodeagent") {
			//ignore this config line.
			continue
		}

		res += "\n" + scanner.Text()
	}

	calicoctl.Apply(res)

	calico.SetCalicoNodeEnvironmentWithRetry(f.ClientSet, "FELIX_FLOWLOGSFLUSHINTERVAL", "300")
	calico.SetCalicoNodeEnvironmentWithRetry(f.ClientSet, "FELIX_FLOWLOGSFILEAGGREGATIONKINDFORALLOWED", "2")
	calico.RestartCalicoNodePods(f.ClientSet, "")
}

//based on isHTTPS flag value - curl http or https to the backend service.
func testForProtocolFlowLogs(f *framework.Framework, namespace, podName, servicePort, servicePath, x_real_ip_hdr string, isHTTPS bool) string {
	var clientPod *v1.Pod

	//ensure pods are running in the namespace.
	for waitForPodsInNamespace(f, namespace) == false {
		framework.Logf("testForProtocolFlowLogs: Waiting for pods to be ready to serve traffic!!!!")
		time.Sleep(time.Second * 3)
	}

	clientPod = createHTTPClientPod(f, podName, servicePort, servicePath, x_real_ip_hdr, isHTTPS)

	err := framework.WaitForPodSuccessInNamespace(f.ClientSet, clientPod.Name, "default")
	if err != nil {
		framework.Logf("Failed to succeed pod: %v", err)
	}
	Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("checking %s could communicate with server.", clientPod.Name))

	return clientPod.Status.PodIP
}

func createHTTPClientPod(f *framework.Framework, clientPodName, servicePort, servicePath, x_real_ip_hdr string, isHTTPS bool) *v1.Pod {
	//use IP from internal IPs to build curl command.
	IPs := getNodeInternalIPs(f)
	framework.Logf("Private IPs are: %+v", IPs)

	//TODO - randomly pick IP from IPs instead of hard coded.
	privateIP := IPs[1]

	cmdArgs := []string{}
	cmdArgs = append(cmdArgs, "-c")
	if !isHTTPS {
		cmdArgs = append(cmdArgs, fmt.Sprintf("curl http://%s:%s/%s -H '%s' -H '%s' -w 1 --retry 100", privateIP, servicePort, servicePath, x_real_ip_hdr, fwdedIPHeader))
	} else {
		cmdArgs = append(cmdArgs, fmt.Sprintf("curl https://%s:%s/%s -H \"%s\" -H \"%s\" --insecure", privateIP, servicePort, servicePath, x_real_ip_hdr, fwdedIPHeader))
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
					ImagePullPolicy: v1.PullIfNotPresent, //TBD - should probably version lock it as well.
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
func setupHTTPService(f *framework.Framework) error {
	//create ingress resources and ingress controller service and deployments
	err := kubectl.Create(deploymentStringHTTP, "", "")
	if err != nil {
		framework.Logf("Failed to setup ingress controller: %v", err)
	}
	Expect(err).To(BeNil())
	err = framework.WaitForService(f.ClientSet, "ingress-nginx", "default-backend", true, framework.Poll, framework.ServiceRespondingTimeout)
	if err != nil {
		framework.Logf("Failed to see service %v running", "default-backend")
	}
	Expect(err).NotTo(HaveOccurred())

	err = framework.WaitForService(f.ClientSet, "ingress-nginx", "nginx-ingress", true, framework.Poll, framework.ServiceRespondingTimeout)
	if err != nil {
		framework.Logf("Failed to see service %v running", "nginx-ingress")
	}
	Expect(err).NotTo(HaveOccurred())
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

func createHTTPSIngressDeployment(f *framework.Framework) {
	createHTTPSService()
	//create ingress resources and ingress controller deployments for HTTPS
	err := kubectl.Create(httpsIngressDeployment, "", "")
	if err != nil {
		framework.Logf("Failed to setup ingress controller for HTTPS: %v", err)
	}
	Expect(err).To(BeNil())
	err = framework.WaitForService(f.ClientSet, "ingress-nginx-https", "nginxsvc", true, framework.Poll, framework.ServiceRespondingTimeout)
	if err != nil {
		framework.Logf("Failed to see service %v running", "nginxsvc")
	}
	Expect(err).NotTo(HaveOccurred())

	err = framework.WaitForService(f.ClientSet, "ingress-nginx-https", "nginx-ingress-https", true, framework.Poll, framework.ServiceRespondingTimeout)
	if err != nil {
		framework.Logf("Failed to see service %v running", "nginx-ingress-https")
	}
	Expect(err).NotTo(HaveOccurred())

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

func createMultipleIngressesScenario(f *framework.Framework)  {
	framework.Logf("Creating multiple Ingresses for serving HTTP traffic")
	err := kubectl.Create(manifestMultipleIngresses, "", "")
	if err != nil {
		framework.Logf("Failed to create service: %v", err)
	}
	Expect(err).To(BeNil())

	err = framework.WaitForService(f.ClientSet, "ingress-nginx-multiple-ic", "default-backend-multiple-ic", true, framework.Poll, framework.ServiceRespondingTimeout)
	if err != nil {
		framework.Logf("Failed to see service %v running", "default-backend-multiple-ic")
	}
	Expect(err).NotTo(HaveOccurred())

	err = framework.WaitForService(f.ClientSet, "ingress-nginx-multiple-ic", "nginx-ingress-multiple-ic-1", true, framework.Poll, framework.ServiceRespondingTimeout)
	if err != nil {
		framework.Logf("Failed to see service %v running", "ingress-nginx-multiple-ic-1")
	}
	Expect(err).NotTo(HaveOccurred())

	err = framework.WaitForService(f.ClientSet, "ingress-nginx-multiple-ic", "nginx-ingress-multiple-ic-2", true, framework.Poll, framework.ServiceRespondingTimeout)
	if err != nil {
		framework.Logf("Failed to see service %v running", "ingress-nginx-multiple-ic-2")
	}
	Expect(err).NotTo(HaveOccurred())

}

func cleanupMultipleIngressesScenario() {
	err := kubectl.DeleteYaml(manifestMultipleIngresses, "", "")
	if err != nil {
		framework.Logf("Failed to cleanup Multiple Ingresses and backend deployment: %v", err)
	}
}

//wait for pods to be running to proceed with tests. Returns bool indicating if Pods and containers ready to serve.
func waitForPodsInNamespace (f *framework.Framework, namespace string) bool {
	var podlist *v1.PodList
	var err error

	podlist, err = f.ClientSet.CoreV1().Pods(namespace).List(metav1.ListOptions{})
	if err != nil {
		framework.Logf("waitForPodsInNamespace: Failed to list pods error:%v", err)
	}
	Expect(err).To(BeNil())

	if len(podlist.Items) == 0 {
		framework.Logf("waitForPodsInNamespace: No pods listed for namespace %v, retry", namespace)
		return false
	}

	for _, p := range podlist.Items {
		err = framework.WaitForPodNameRunningInNamespace(f.ClientSet, p.ObjectMeta.Name, namespace)
		if err != nil {
			framework.Logf("waitForPodsInNamespace: Failed to wait: %v", err)
		}
		Expect(err).To(BeNil())
	}

	for _,p := range podlist.Items {
		for _, cs := range p.Status.ContainerStatuses {
			framework.Logf("waitForPodsInNamespace: Container status: %+v", cs)
			if !cs.Ready {
				framework.Logf("waitForPodsInNamespace: Container is not ready: %+v", cs)
				return false
			}
		}
	}

	return true
}
