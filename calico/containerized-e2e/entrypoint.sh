#!/bin/bash
if [[ $USE_EXT_CONFORMANCE_FOCUS ]]; then
  FOCUS="(ConfigMap|Docker|Downward API|Events|DNS|Proxy|Scheduler|ReplicationController|ReplicaSet|CustomResourceDefinition).*(\[Conformance\])"
elif [[ "$USE_EXT_NETWORKING_FOCUS" ]]; then
  FOCUS="(Network|Pods|Services).*(\[Conformance\])|\[Feature:NetworkPolicy\]|\[Feature:Ingress\]"
elif [[ ! $FOCUS ]]; then
  FOCUS="(Networking).*(\[Conformance\])|\[Feature:NetworkPolicy\]"
fi

SKIP="Alpha|Disruptive|Experimental|Flaky|Kubectl|Serial|Volume|Feature:EgressNetworkPolicy"

echo Using Focus: $FOCUS
e2e.test -kubeconfig=/root/kubeconfig --ginkgo.focus="$FOCUS" --ginkgo.skip="$SKIP" -report-dir=/report "$EXTRA_ARGS"

