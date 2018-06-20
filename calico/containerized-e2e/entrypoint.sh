#!/bin/bash

set -e

CALICO_VER=""
CALICO_FOCUS_REGEX=""
CNX_FOCUS_REGEX=""
EXT_NETWORKING=true
EXT_CONFORMANCE=false
FOCUS=""
SKIPS="Alpha|Disruptive|Experimental|Flaky|Kubectl|Serial|Volume|Feature:EgressNetworkPolicy|Pods Set QOS Class"

function combine_regex {
  local IFS="$1"
  shift
  echo "$*"
}

function focus_calico {
  CALICO_FOCUS_REGEX="\[Feature:CalicoPolicy-${CALICO_VER}\]"
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
      CALICO_FOCUS_REGEX="\[Feature:CalicoPolicy-v3\] policy ordering"
  fi
  if [ "$CALICO_VER" == "v2" ]; then
    SKIPS=$(combine_regex "|" "named port" "$SKIPS")
  fi
}

function focus_cnx {
  CNX_FOCUS_REGEX="\[Feature:CNX-${CNX_VER}\]|\[Feature:CNX\]|\[Feature:CNX-${CNX_VER}-RBAC\]"
}

function focus_combined {
  if [[ -n "$CALICO_FOCUS_REGEX" && -n "$CNX_FOCUS_REGEX" ]]; then
    FOCUS=$(combine_regex "|" "$CALICO_FOCUS_REGEX" "$CNX_FOCUS_REGEX")
  elif [[ -n "$CALICO_FOCUS_REGEX" && -z "$CNX_FOCUS_REGEX" ]]; then
    FOCUS="$CALICO_FOCUS_REGEX"
  elif [[ -z "$CALICO_FOCUS_REGEX" && -n "$CNX_FOCUS_REGEX" ]]; then
    FOCUS="$CNX_FOCUS_REGEX"
  else
    echo '[WARNING] did not match calico or cnx options'
  fi
}

function runner {
  local FOCUS
  FOCUS=$1
  e2e.test -kubeconfig=/root/kubeconfig \
    --ginkgo.focus="$FOCUS" \
    --ginkgo.skip="$SKIPS" \
    -report-dir=/report $EXTRA_ARGS
}

function focus_info {
  local FOCUS
  FOCUS=$1
  echo "[INFO] e2e.test -kubeconfig=/root/kubeconfig \
  --ginkgo.focus=\"$FOCUS\" \
  --ginkgo.skip=\"$SKIPS\" \
  -report-dir=/report $EXTRA_ARGS"
}

function usage {
  cat <<EOF
Usage: $0 \
  docker run --net=host -v \$KUBECONFIG:/root/kubeconfig gcr.io/unique-caldron-775/k8s-e2e ARGS...

Arguments:
  --calico-version (v2|v3)              Run calico tests. [default: none]
  --cnx v3                              Run CNX tests. [default: none]
  --extended-networking (true|false)    Run extended networking tests. [default: true]
  --extended-conformance (true|false)   Run extended conformance tests. [default: true]
  --focus <FOCUS>                       Set to override any of the above options with a
                                        manually specified focus.
  --extra-args <EXTRA_ARGS>             Pass additional args to the e2e.test binary.
  --skip <SKIPS>                        Control which tests are skipped by ginkgo.
EOF
  exit 0
}
while [ -n "$1" ]; do
  case "$1" in
    --calico-version) CALICO_VER=$2 ;;
    --cnx) CNX_VER=$2 ;;
    --extended-networking) EXT_NETWORKING=$2 ;;
    --extended-conformance) EXT_CONFORMANCE=$2 ;;
    --focus) FOCUS=$2 ;;
    --extra-args) EXTRA_ARGS=$2 ;;
    --skip|--skips) SKIPS=$2 ;;
    --help) usage ;;
  esac
  shift
done

# custom focus, exists w/out eval extended options
if [ -n "$FOCUS" ]; then
  focus_info "$FOCUS"
  runner "$FOCUS"
  exit 0
fi

# build out expected calico/cnx focus cmds
if [ -n "$CALICO_VER" ]; then focus_calico ; fi
if [ -n "$CNX_VER" ]; then focus_cnx ; fi
if [ -z "$FOCUS" ]; then focus_combined ; fi

# focus_combined should have crafted calico/cnx focus if provided
if [ -n "$FOCUS" ]; then
  focus_info "$FOCUS"
  runner "$FOCUS"
  mv /report/junit_01.xml /report/junit_basic.xml || true
fi

# extended secondary/tertiary focus runs whether or not calico/cnx were provided
EXT_NETWORK_FOCUS="(Network|Pods|Services).*(\[Conformance\])|\[Feature:NetworkPolicy\]|\[Feature:Ingress\]"
EXT_CONFORMANCE_FOCUS="(ConfigMap|Docker|Downward API|Events|DNS|Proxy|Scheduler|ReplicationController|ReplicaSet|CustomResourceDefinition).*(\[Conformance\])"
if [ "$EXT_NETWORKING" == true ]; then
  focus_info "$EXT_NETWORK_FOCUS"
  runner "$EXT_NETWORK_FOCUS"
  mv /report/junit_01.xml /report/junit_ext_networking.xml || true
fi
if [ "$EXT_CONFORMANCE" == true ]; then
  focus_info "$EXT_CONFORMANCE_FOCUS"
  runner "$EXT_CONFORMANCE_FOCUS"
  mv /report/junit_01.xml /report/junit_ext_conformance.xml || true
fi
