#!/bin/bash

set -e

CALICO_VER=""
EXT_NETWORKING=""
EXT_CONFORMANCE=""
DEFAULT_TIMEOUTS="6m"
E2E_PREFIX=""

# Focus regexes from various sources.
CALICO_FOCUS=""
CNX_FOCUS=""
DEF_FOCUS=""
OPT_FOCUS=""
FOCUS=""

# Skip regexes from various sources.
CALICO_SKIPS=""
CNX_SKIPS=""
DEF_SKIPS="Alpha|Disruptive|Experimental|Flaky|Kubectl|Serial|Volume|Feature:EgressNetworkPolicy|Pods Set QOS Class"
OPT_SKIPS=""
SKIPS=""

function combine_regex {
  local IFS="$1"
  shift
  parms=()
  for i in $*; do
    if [[ -n "$i" ]]; then
      parms+=("$i")
    fi
  done
  echo "${parms[*]}"
}

function focus_calico {
  CALICO_FOCUS="\[Feature:CalicoPolicy-${CALICO_VER}\]"
  if [ "$CALICO_VER" == "v3" ]; then
      # CALICO_VER=v3 should run all of the [Feature:CalicoPolicy-v3]
      # tests, but we recently tried that and found that lots of them
      # had problems, and don't immediately want to take on fixing
      # those up.  We know, however, that the v3 policy ordering tests
      # work, so arrange for now for "v3" to mean running those tests.
      #
      # Note that even that reduced set is strictly better than "v2",
      # as we have no tests at all that are tagged with
      # [Feature:CalicoPolicy-v2].
      CALICO_FOCUS="\[Feature:CalicoPolicy-v3\] policy ordering"
  fi
  if [ "$CALICO_VER" == "v2" ]; then
    CALICO_SKIPS="named port"
  fi
}

function focus_cnx {
  CNX_FOCUS="\[Feature:CNX-${CNX_VER}\]|\[Feature:CNX\]|\[Feature:CNX-${CNX_VER}-RBAC\]"
}

function runner {
  local FOCUS
  FOCUS=$1
  e2e.test -kubeconfig=/root/kubeconfig \
    --ginkgo.focus="$FOCUS" \
    --ginkgo.skip="$SKIPS" \
    -report-dir=/report \
    --node-schedulable-timeout="$DEFAULT_TIMEOUTS" \
    --system-pods-startup-timeout="$DEFAULT_TIMEOUTS" \
    $E2E_PREFIX $EXTRA_ARGS
}

function focus_info {
  local FOCUS
  FOCUS=$1
  echo "[INFO] e2e.test -kubeconfig=/root/kubeconfig \
  --ginkgo.focus=\"$FOCUS\" \
  --ginkgo.skip=\"$SKIPS\" \
  -report-dir=/report $E2E_PREFIX $EXTRA_ARGS"
}

function usage {
  cat <<EOF
Usage: $0 \
  docker run --net=host -v \$KUBECONFIG:/root/kubeconfig gcr.io/unique-caldron-775/k8s-e2e ARGS...

Arguments:
  --calico-version (v2|v3)              Run calico tests. [default: none]
  --cnx v3                              Run CNX tests. [default: none]
  --extended-networking (true|false)    Run extended networking tests. [default: false]
  --extended-conformance (true|false)   Run extended conformance tests. [default: false]
  --extra-args <EXTRA_ARGS>             Pass additional args to the e2e.test binary.
  --focus <FOCUS>                       Control which tests are run by ginkgo. This is in addition to any
                                        executed tests controlled by calico/cnx options.
  --prefix <prefix>                     A prefix to be added to cloud resources created during testing.
                                        [default: e2e]
  --skip <SKIPS>                        Control which tests are skipped by ginkgo. This is in addition to any
                                        skipped tests controlled by calico/cnx/extended options.
EOF
  exit 0
}
while [ -n "$1" ]; do
  case "$1" in
    --calico-version) CALICO_VER=$2; shift ;;
    --cnx) CNX_VER=$2; shift ;;
    --extended-networking) EXT_NETWORKING=$2; shift ;;
    --extended-conformance) EXT_CONFORMANCE=$2; shift ;;
    --focus) OPT_FOCUS=$2; shift ;;
    --extra-args) EXTRA_ARGS="$EXTRA_ARGS $2"; shift ;;
    --prefix) E2E_PREFIX="--prefix $2"; shift ;;
    --skip|--skips) OPT_SKIPS=$2; shift ;;
    --help) usage ;;
  esac
  shift
done

# build out expected calico/cnx focus cmds
if [ -n "$CALICO_VER" ]; then focus_calico ; fi
if [ -n "$CNX_VER" ]; then focus_cnx ; fi

# Combine the focus and skip regexes.
FOCUS=$(combine_regex "|" "$DEF_FOCUS" "$OPT_FOCUS" "$CALICO_FOCUS" "$CNX_FOCUS")
SKIPS=$(combine_regex "|" "$DEF_SKIPS" "$OPT_SKIPS" "$CALICO_SKIPS" "$CNX_SKIPS")

# focus_combined should have crafted calico/cnx focus if provided
if [ -n "$FOCUS" ]; then
  focus_info "$FOCUS"
  runner "$FOCUS"
  mv /report/junit_01.xml /report/junit_basic.xml || true
fi

# extended secondary/tertiary focus runs whether or not calico/cnx were provided
EXT_NETWORK_FOCUS="(Network|Pods|Services).*(\[Conformance\])|\[Feature:NetworkPolicy\]|\[Feature:Ingress\]"
EXT_CONFORMANCE_FOCUS="(ConfigMap|Docker|Downward API|Events|DNS|Proxy|Scheduler|ReplicationController|ReplicaSet|CustomResourceDefinition).*(\[Conformance\])"
if [ "$EXT_NETWORKING" == "true" ]; then
  focus_info "$EXT_NETWORK_FOCUS"
  runner "$EXT_NETWORK_FOCUS"
  mv /report/junit_01.xml /report/junit_ext_networking.xml || true
fi
if [ "$EXT_CONFORMANCE" == "true" ]; then
  focus_info "$EXT_CONFORMANCE_FOCUS"
  runner "$EXT_CONFORMANCE_FOCUS"
  mv /report/junit_01.xml /report/junit_ext_conformance.xml || true
fi
