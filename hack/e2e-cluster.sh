#!/bin/sh
# The end-to-end check from nothing. Starts an empty two-node k3s in
# containers, builds both images, installs the panel with the Helm chart in
# deploy/chart as a user would, creates the first account and runs
# hack/e2e.sh against it. Needs only Docker, curl and python3 (Helm is run
# from its image); this is what CI runs.
#
#   ./hack/e2e-cluster.sh           # build, test, tear down
#   KEEP=1 ./hack/e2e-cluster.sh    # leave the cluster up to look around
#
# Because the cluster starts empty, this also covers what the unit tests
# cannot: the chart, its permissions, and the panel's very first start.
set -eu
cd "$(dirname "$0")/.."

NAME=${E2E_NAME:-avsys-e2e}
# The second node, which servers are moved to and from.
AGENT=$NAME-2
K3S=${E2E_K3S_IMAGE:-rancher/k3s:v1.33.5-k3s1}
HTTP_PORT=${E2E_HTTP_PORT:-18080}
SSH_PORT=${E2E_SSH_PORT:-12222}
ROUTER_PORT=${E2E_ROUTER_PORT:-35565}
PANEL=ghcr.io/gt-drehmal/avsys:e2e
WORKSPACE=ghcr.io/gt-drehmal/avsys-workspace:e2e
URL=http://localhost:$HTTP_PORT
# What the cluster pulls from Docker Hub as the run goes: the game's image and
# the one that measures a node. Each node pulls them as soon as it is up, and
# tries again for a few minutes if Docker Hub does not answer. Should that
# not be enough, E2E_OUTSIDE_TAR, a copy from an earlier run, is loaded
# instead; with E2E_OUTSIDE_SAVE set, a copy is written there once they are
# pulled.
OUTSIDE=${E2E_OUTSIDE_IMAGES:-docker.io/itzg/minecraft-server:java21 docker.io/library/alpine:3}
OUTSIDE_TAR=${E2E_OUTSIDE_TAR:-}

k() { docker exec -i "$NAME" kubectl "$@"; }
step() { printf '\n== %s\n' "$1"; }
fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }

finish() {
  status=$?
  # The second node may have been left down by a run that failed there.
  docker unpause "$AGENT" >/dev/null 2>&1 || true
  if [ $status -ne 0 ]; then
    step "what the cluster looked like"
    k get pods -A -o wide 2>&1 | tail -20 || true
    k -n avsys logs deploy/avsys --tail=60 2>&1 || true
    # The test server is left in place on failure so that its own output,
    # including that of a game container that has since restarted, is here.
    for ns in $(k get namespaces -o name 2>/dev/null | sed -n 's|^namespace/\(mc-.*\)|\1|p'); do
      for pod in $(k -n "$ns" get pods -o name 2>/dev/null); do
        step "$ns $pod"
        k -n "$ns" get "$pod" -o jsonpath='{range .status.containerStatuses[*]}{.name}{": restarts="}{.restartCount}{" state="}{.state}{" last="}{.lastState}{"\n"}{end}' 2>&1 || true
        k -n "$ns" logs "$pod" -c minecraft --previous --tail=60 2>/dev/null | sed 's/^/previous| /' || true
        k -n "$ns" logs "$pod" -c minecraft --tail=60 2>&1 | sed 's/^/current | /' || true
      done
    done
    k get events -A --sort-by=.lastTimestamp 2>&1 | tail -25 || true
  fi
  if [ -n "${KEEP:-}" ]; then
    printf '\nCluster left running: docker exec -it %s kubectl get pods -A; panel at %s; remove %s and %s when done\n' "$NAME" "$URL" "$NAME" "$AGENT"
  else
    docker rm -f -v "$NAME" "$AGENT" >/dev/null 2>&1 || true
  fi
  exit $status
}
trap finish EXIT

# prepull NODE: what the run will need from outside, on that node, ahead of
# the run. Never fails: a node without them pulls them itself later.
prepull() {
  missing=
  for image in $OUTSIDE; do
    pulled=
    for try in 1 2 3 4 5 6; do
      if docker exec "$1" crictl pull "$image" >/dev/null 2>&1; then pulled=1; break; fi
      [ "$try" = 6 ] || sleep 20
    done
    [ -n "$pulled" ] || missing="$missing $image"
  done
  if [ -z "$missing" ]; then
    if [ -n "${E2E_OUTSIDE_SAVE:-}" ] && [ -n "$OUTSIDE_TAR" ] && [ "$1" = "$NAME" ]; then
      mkdir -p "$(dirname "$OUTSIDE_TAR")"
      # shellcheck disable=SC2086 # one argument per image
      if docker exec "$1" ctr images export /tmp/outside.tar $OUTSIDE >/dev/null &&
         docker cp "$1:/tmp/outside.tar" "$OUTSIDE_TAR.new" >/dev/null; then
        mv "$OUTSIDE_TAR.new" "$OUTSIDE_TAR"
        touch "$(dirname "$OUTSIDE_TAR")/fresh"
        echo "ok: kept a copy of what the cluster pulled, for runs to come"
      fi
      docker exec "$1" rm -f /tmp/outside.tar || true
    fi
    return 0
  fi
  if [ -n "$OUTSIDE_TAR" ] && [ -s "$OUTSIDE_TAR" ] && docker exec -i "$1" ctr images import - < "$OUTSIDE_TAR" >/dev/null 2>&1; then
    echo "note: Docker Hub did not give $1$missing; the copy from an earlier run is used"
  else
    echo "note: Docker Hub did not give $1$missing, and there is no copy from an earlier run"
  fi
  return 0
}

# kubelet_started NODE: waits, without going into it, until a node's k3s has
# started its kubelet. Before then, k3s moves what runs in its container
# into a cgroup of its own and turns on the cgroup controllers, which fails
# while a process run in the container with docker exec is in the way: its
# kubelet then cannot start, and the node never joins.
kubelet_started() {
  for _ in $(seq 1 120); do
    docker logs "$1" 2>&1 | grep -q '"Started kubelet"' && return 0
    sleep 1
  done
  return 1
}

step "images"
if [ -z "${E2E_SKIP_BUILD:-}" ]; then
  docker build -q -t "$PANEL" --build-arg VERSION=e2e . >/dev/null
  docker build -q -t "$WORKSPACE" -f images/workspace/Dockerfile . >/dev/null
fi
echo "ok: $PANEL and $WORKSPACE"

step "empty cluster"
docker rm -f -v "$NAME" "$AGENT" >/dev/null 2>&1 || true
docker run -d --name "$NAME" --privileged \
  -p "127.0.0.1:$HTTP_PORT:80" -p "127.0.0.1:$SSH_PORT:2222" -p "127.0.0.1:$ROUTER_PORT:25565" \
  "$K3S" server --disable traefik --disable metrics-server >/dev/null
kubelet_started "$NAME" || fail "the cluster did not come up"
for _ in $(seq 1 60); do
  k get --raw /readyz >/dev/null 2>&1 && k get serviceaccount default >/dev/null 2>&1 && break
  sleep 2
done
k wait --for=condition=Ready node --all --timeout=120s >/dev/null || fail "the cluster did not come up"
prepull "$NAME" &
prepulls=$!
# The ports published above are the ones the manifest's LoadBalancer service
# listens on, which k3s binds on its node.
# The panel runs the workspace image of its own version, so both are needed.
docker save "$PANEL" "$WORKSPACE" | docker exec -i "$NAME" ctr images import - >/dev/null
echo "ok: k3s is up with both images loaded"

step "second node"
# An agent beside it, joined through the server's address on Docker's
# network, with the same images, as anything may be scheduled there.
joining=$(docker exec "$NAME" cat /var/lib/rancher/k3s/server/node-token)
address=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$NAME")
docker run -d --name "$AGENT" --privileged -e K3S_URL="https://$address:6443" -e K3S_TOKEN="$joining" "$K3S" agent >/dev/null
# Its pulls start once its container runtime answers.
(
  kubelet_started "$AGENT" || exit 0
  for _ in $(seq 1 60); do docker exec "$AGENT" crictl info >/dev/null 2>&1 && break; sleep 2; done
  prepull "$AGENT"
) &
prepulls="$prepulls $!"
# It joins within about 20 seconds. One that has not in a minute and a half
# is not going to: what it said is shown, and it is started again, once.
joined() {
  for _ in $(seq 1 45); do
    [ "$(k get nodes --no-headers 2>/dev/null | grep -c ' Ready ')" = 2 ] && return 0
    sleep 2
  done
  return 1
}
# agent_said: the nodes, and what went wrong on the second one.
agent_said() {
  k get nodes -o wide 2>&1 || true
  docker ps -a --filter "name=^$AGENT\$" --format '{{.Names}}: {{.Status}}' 2>&1 || true
  docker logs "$AGENT" 2>&1 | grep -E '^E[0-9]|level=(error|fatal)|too many open files' | tail -20 | sed 's/^/agent| /' || true
}
if ! joined; then
  echo "note: the second node did not join; it is started again"
  agent_said
  docker restart "$AGENT" >/dev/null
  joined || { agent_said; fail "the second node did not join"; }
fi
docker save "$PANEL" "$WORKSPACE" | docker exec -i "$AGENT" ctr images import - >/dev/null
AGENT_NODE=$(k get nodes -l '!node-role.kubernetes.io/control-plane' -o jsonpath='{.items[0].metadata.name}')
[ -n "$AGENT_NODE" ] || fail "the second node is not named"
echo "ok: two nodes, both with the images"

step "install"
# The panel and its volumes go on the first node, so that the second can be
# taken down under the servers on it while the panel goes on.
k cordon "$AGENT_NODE" >/dev/null
# What the chart makes, with its default values (deploy/install.yaml) and
# with each set in deploy/chart/ci, is first checked against the cluster's
# API without being made: these are the only check of the values the
# install below does not use.
k create namespace avsys >/dev/null
for manifest in deploy/install.yaml deploy/chart/ci/*.yaml; do
  case "$manifest" in *-values.yaml) continue ;; esac
  k apply --dry-run=server -f - < "$manifest" >/dev/null || fail "$manifest is refused by the cluster"
done
echo "ok: the cluster takes what the chart makes, with each set of values"
# Helm reaches the cluster from the k3s container's own network.
kubeconfig=$(mktemp)
docker exec "$NAME" cat /etc/rancher/k3s/k3s.yaml > "$kubeconfig"
# The offsite copy is a directory on the panel's own volume: nowhere a real
# one should be, and enough to run it through here. Servers' extra ports
# get a range of public ports. Nobody has to use two-factor, as installed,
# so that the first account makes its API token straight away: having to is
# tested by the panel's own tests (internal/api, web/tests).
HELM= HELM_DOCKER_ARGS="--network container:$NAME -v $kubeconfig:/kubeconfig:ro -e KUBECONFIG=/kubeconfig" \
  ./hack/chart.sh helm install avsys deploy/chart --namespace avsys \
  --set image.tag=e2e --set image.pullPolicy=IfNotPresent \
  --set publicURL="$URL" \
  --set offsite.repository=/data/offsite-e2e \
  --set-string offsite.password="$(python3 -c 'import secrets; print(secrets.token_urlsafe(18))')" \
  --set extraPorts.range=25600-25609 \
  --set signIn.twoFactor=nobody >/dev/null || fail "helm install failed"
rm -f "$kubeconfig"
k -n avsys rollout status deploy/avsys --timeout=180s >/dev/null || fail "the panel did not become ready"
k uncordon "$AGENT_NODE" >/dev/null
restarts=$(k -n avsys get pods -o jsonpath='{.items[*].status.containerStatuses[*].restartCount}')
[ "$restarts" = 0 ] || fail "the panel restarted $restarts time(s) during its first start"
echo "ok: installed with the chart, first start was clean"
# Updating itself from the Nodes page: the panel may change the image of its
# own Deployment (tried, not made), and no other Deployment anywhere.
panel_sa=system:serviceaccount:avsys:avsys
k -n avsys patch deployment avsys --as "$panel_sa" --dry-run=server --type strategic \
  -p '{"spec":{"template":{"spec":{"containers":[{"name":"avsys","image":"ghcr.io/gt-drehmal/avsys:v9.9.9"}]}}}}' >/dev/null ||
  fail "the panel may not move its own Deployment to a new release"
[ "$(k -n avsys auth can-i patch deployments/another --as "$panel_sa")" = no ] || fail "the panel may change another Deployment beside it"
[ "$(k -n default auth can-i patch deployments --as "$panel_sa")" = no ] || fail "the panel may change Deployments elsewhere"
echo "ok: the panel may update its own Deployment, and no other"
for _ in $(seq 1 60); do
  curl -fsS -o /dev/null "$URL/api/v1/meta" 2>/dev/null && break
  sleep 2
done
meta=$(curl -fsS "$URL/api/v1/meta") || fail "the panel is not reachable through its LoadBalancer service"
case "$meta" in *'"setupRequired":true'*) ;; *) fail "a new install should ask for its first account: $meta" ;; esac
echo "ok: reachable through the service, waiting for its first account"

step "first account"
JAR=$(mktemp)
password=$(python3 -c 'import secrets; print(secrets.token_urlsafe(18))')
curl -fsS -o /dev/null -c "$JAR" -H 'X-Avsys: 1' -d "{\"username\":\"e2e\",\"password\":\"$password\"}" "$URL/api/v1/auth/register"
admin=$(curl -fsS -b "$JAR" "$URL/api/v1/me" | python3 -c 'import json,sys; print(json.load(sys.stdin)["user"]["isAdmin"])')
[ "$admin" = True ] || fail "the first account is not an instance administrator"
level=$(curl -fsS -b "$JAR" "$URL/api/v1/me" | python3 -c 'import json,sys; print(json.load(sys.stdin)["twoFactorLevel"])')
[ "$level" = nobody ] || fail "installed with signIn.twoFactor=nobody, the panel has $level"
status=$(curl -sS -o /dev/null -w '%{http_code}' -H 'X-Avsys: 1' -d '{"username":"second","password":"another password"}' "$URL/api/v1/auth/register")
[ "$status" = 403 ] || fail "a second account was created without an invite (HTTP $status)"
TOKEN=$(curl -fsS -b "$JAR" -H 'X-Avsys: 1' -d '{"name":"e2e"}' "$URL/api/v1/me/tokens" | python3 -c 'import json,sys; print(json.load(sys.stdin)["secret"])')
rm -f "$JAR"
echo "ok: first account is the administrator, and signups then close"

# Why a game stopped is in the log of the container that exited, which only
# the cluster can show: the panel's console is of the one running now.
# Until the game has been started again, the one that exited is still the
# current one.
EXITED_LOG="docker exec -i $NAME kubectl -n mc-\$1 logs \$2 -c minecraft --previous --tail=80 2>/dev/null || docker exec -i $NAME kubectl -n mc-\$1 logs \$2 -c minecraft --tail=80"
# What the image prints before the game's own log begins, such as which
# launch flags it used, is only in the container's log too.
GAME_LOG="docker exec -i $NAME kubectl -n mc-\$1 logs \$2 -c minecraft"
# Extra ports are reached at a node's own address, which is the
# container's on Docker's network, from the node itself: the machine running
# Docker may not reach that network.
node_ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$NAME")
# The second node is taken down by freezing its container, as a node that
# stops answering, and brought back by letting it go on.
# The node has no Python; the workspace image, loaded here already, has, and
# is run in the node's network to reach its ports as a player would.
ON_NODE_PYTHON="docker run --rm -i --network container:$NAME --entrypoint python3 $WORKSPACE"
# The panel's own programs, restic among them, run in its container; and the
# panel restarted, as an update restarts it.
PANEL_EXEC="docker exec -i $NAME kubectl -n avsys exec -i deploy/avsys --"
PANEL_RESTART="docker exec $NAME kubectl -n avsys rollout restart deploy/avsys && docker exec $NAME kubectl -n avsys rollout status deploy/avsys --timeout=180s"
# shellcheck disable=SC2086 # one process each
wait $prepulls
AVSYS_E2E_DOWN_NODE=$AGENT_NODE AVSYS_E2E_NODE_DOWN="docker pause $AGENT" AVSYS_E2E_NODE_UP="docker unpause $AGENT" AVSYS_E2E_ON_DOWN_NODE="docker exec -i $AGENT" \
AVSYS_E2E_PANEL_EXEC=$PANEL_EXEC AVSYS_E2E_PANEL_RESTART=$PANEL_RESTART AVSYS_E2E_NODE_IP=$node_ip AVSYS_E2E_ON_NODE="docker exec -i $NAME" AVSYS_E2E_PYTHON_ON_NODE=$ON_NODE_PYTHON AVSYS_E2E_PORT_RANGE=25600-25609 AVSYS_URL=$URL AVSYS_TOKEN=$TOKEN AVSYS_SSH_PORT=$SSH_PORT AVSYS_ROUTER_PORT=$ROUTER_PORT AVSYS_E2E_KEEP_ON_FAILURE=1 AVSYS_E2E_EXITED_LOG=$EXITED_LOG AVSYS_E2E_CONTAINER_LOG=$GAME_LOG ./hack/e2e.sh
