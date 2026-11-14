#!/bin/sh
# The Helm chart in deploy/chart, and what is made from it.
#
#   ./hack/chart.sh render   # deploy/install.yaml and the renders in deploy/chart/ci
#   ./hack/chart.sh check    # helm lint with each values set, and values that must be refused
#   ./hack/chart.sh helm ... # helm itself, as below
#
# deploy/install.yaml is the chart with its default values, for installing
# with kubectl alone. Each deploy/chart/ci/<name>-values.yaml is rendered to
# deploy/chart/ci/<name>.yaml, so a change to the chart shows in review as a
# change to what it makes. CI renders again and fails if anything differs.
#
# Helm is run from its image, at a fixed version, so that every machine
# renders the same; HELM=helm uses the one installed instead. HELM_DOCKER_ARGS
# are added to that docker run, as a network and a kubeconfig to reach a
# cluster through.
set -eu
cd "$(dirname "$0")/.."

HELM_IMAGE=alpine/helm:4.3.1@sha256:e3f8d5c462d75470fbb1fce79ad5c20063657096d1f98c0fcea869e2600e2e5e
helm() {
  if [ -n "${HELM:-}" ]; then
    "$HELM" "$@"
  else
    # shellcheck disable=SC2086 # several arguments, split on purpose
    docker run --rm -q -u "$(id -u):$(id -g)" -e HOME=/tmp -v "$PWD/deploy:/deploy:ro" -w / ${HELM_DOCKER_ARGS:-} "$HELM_IMAGE" "$@"
  fi
}
template() { helm template avsys deploy/chart --namespace avsys "$@"; }

render() {
  {
    cat <<'EOF'
# Installs avSYS into the `avsys` namespace.
#
#   kubectl apply -f install.yaml
#
# Before applying, set AVSYS_PUBLIC_URL below to the address you will open
# the panel at. The three ports are exposed by a LoadBalancer Service, at the
# bottom. The MinecraftServer CRD is installed by the panel itself on startup.
#
# This is the Helm chart in deploy/chart with its default values, made by
# ./hack/chart.sh render; do not edit it in the repository. For anything
# the defaults do not cover, install the chart instead (see the README).
apiVersion: v1
kind: Namespace
metadata:
  name: avsys
EOF
    template
  } > deploy/install.yaml
  for values in deploy/chart/ci/*-values.yaml; do
    name=$(basename "$values" -values.yaml)
    template -f "$values" > "deploy/chart/ci/$name.yaml"
  done
}

check() {
  helm lint --strict deploy/chart
  for values in deploy/chart/ci/*-values.yaml; do
    helm lint --strict deploy/chart -f "$values"
  done
  # Values that would make an install that does not work are refused, with
  # what to set instead.
  refused() {
    want=$1
    shift
    if out=$(template "$@" 2>&1); then
      echo "FAIL: rendered with $*, which should be refused" >&2
      exit 1
    fi
    case "$out" in
      *"$want"*) echo "ok: refused $*" ;;
      *) echo "FAIL: $* was refused, but not saying \"$want\": $out" >&2; exit 1 ;;
    esac
  }
  refused "LoadBalancer, NodePort or hostPort" --set expose.mode=Ingress
  refused "expose.nodePorts.ssh and expose.nodePorts.minecraft" --set expose.mode=NodePort
  refused "expose.nodePorts.ssh and expose.nodePorts.minecraft" --set expose.mode=NodePort --set expose.nodePorts.ssh=30022
  refused "needs expose.node" --set expose.mode=hostPort
  refused "signIn.github needs a clientSecret" --set signIn.github.clientId=abc
  refused "signIn.google has a clientSecret but no clientId" --set signIn.google.clientSecret=abc
  refused "offsite.repository needs a password" --set offsite.repository=s3:https://s3.example.com/bucket
  refused "offsite.secretEnv names keys of offsite.existingSecret" --set offsite.repository=s3:https://s3.example.com/bucket --set offsite.password=pw --set offsite.secretEnv.AWS_ACCESS_KEY_ID=key
  refused "packs.listener needs packs.url" --set packs.listener.enabled=true
  refused "serviceAccount.name is needed" --set serviceAccount.create=false
}

case "${1:-}" in
  render) render ;;
  check) check ;;
  helm) shift; helm "$@" ;;
  *) echo "usage: $0 render|check|helm" >&2; exit 2 ;;
esac
