#!/bin/sh
# The end-to-end check from nothing. Starts an empty single-node k3s in a
# container, builds both images, installs the panel from deploy/install.yaml
# as a user would, creates the first account and runs hack/e2e.sh against it.
# Needs only Docker, curl and python3; this is what CI runs.
#
#   ./hack/e2e-cluster.sh           # build, test, tear down
#   KEEP=1 ./hack/e2e-cluster.sh    # leave the cluster up to look around
#
# Because the cluster starts empty, this also covers what the unit tests
# cannot: the manifest, its permissions, and the panel's very first start.
set -eu
cd "$(dirname "$0")/.."

NAME=${E2E_NAME:-avsys-e2e}
K3S=${E2E_K3S_IMAGE:-rancher/k3s:v1.33.5-k3s1}
HTTP_PORT=${E2E_HTTP_PORT:-18080}
SSH_PORT=${E2E_SSH_PORT:-12222}
ROUTER_PORT=${E2E_ROUTER_PORT:-35565}
PANEL=ghcr.io/gt-drehmal/avsys:e2e
WORKSPACE=ghcr.io/gt-drehmal/avsys-workspace:e2e
URL=http://localhost:$HTTP_PORT

k() { docker exec -i "$NAME" kubectl "$@"; }
step() { printf '\n== %s\n' "$1"; }
fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }

finish() {
  status=$?
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
    printf '\nCluster left running: docker exec -it %s kubectl get pods -A; panel at %s\n' "$NAME" "$URL"
  else
    docker rm -f -v "$NAME" >/dev/null 2>&1 || true
  fi
  exit $status
}
trap finish EXIT

step "images"
if [ -z "${E2E_SKIP_BUILD:-}" ]; then
  docker build -q -t "$PANEL" --build-arg VERSION=e2e . >/dev/null
  docker build -q -t "$WORKSPACE" -f images/workspace/Dockerfile . >/dev/null
fi
echo "ok: $PANEL and $WORKSPACE"

step "empty cluster"
docker rm -f -v "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" --privileged \
  -p "127.0.0.1:$HTTP_PORT:80" -p "127.0.0.1:$SSH_PORT:2222" -p "127.0.0.1:$ROUTER_PORT:25565" \
  "$K3S" server --disable traefik --disable metrics-server >/dev/null
for _ in $(seq 1 60); do
  k get --raw /readyz >/dev/null 2>&1 && k get serviceaccount default >/dev/null 2>&1 && break
  sleep 2
done
k wait --for=condition=Ready node --all --timeout=120s >/dev/null || fail "the cluster did not come up"
# The ports published above are the ones the manifest's LoadBalancer service
# listens on, which k3s binds on its node.
# The panel runs the workspace image of its own version, so both are needed.
docker save "$PANEL" "$WORKSPACE" | docker exec -i "$NAME" ctr images import - >/dev/null
echo "ok: k3s is up with both images loaded"

# offsite gives the panel an offsite copy that is a directory on its own
# volume: nowhere a real one should be, and enough to run it through here.
offsite() {
  python3 -c '
import secrets, sys
at = "              value: /backups\n"
manifest = sys.stdin.read()
assert manifest.count(at) == 1, "the manifest no longer sets the backup directory the way this expects"
env = "            - name: %s\n              value: \"%s\"\n"
print(manifest.replace(at, at + env % ("AVSYS_OFFSITE_REPOSITORY", "/data/offsite-e2e") + env % ("AVSYS_OFFSITE_PASSWORD", secrets.token_urlsafe(18))), end="")'
}

step "install"
sed -e "s|image: ghcr.io/gt-drehmal/avsys:edge|image: $PANEL|" \
    -e "s|imagePullPolicy: Always|imagePullPolicy: IfNotPresent|" \
    -e "s|value: https://panel.example.com|value: $URL|" deploy/install.yaml | offsite | k apply -f - >/dev/null
k -n avsys rollout status deploy/avsys --timeout=180s >/dev/null || fail "the panel did not become ready"
restarts=$(k -n avsys get pods -o jsonpath='{.items[*].status.containerStatuses[*].restartCount}')
[ "$restarts" = 0 ] || fail "the panel restarted $restarts time(s) during its first start"
echo "ok: installed from deploy/install.yaml, first start was clean"
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
status=$(curl -sS -o /dev/null -w '%{http_code}' -H 'X-Avsys: 1' -d '{"username":"second","password":"another password"}' "$URL/api/v1/auth/register")
[ "$status" = 403 ] || fail "a second account was created without an invite (HTTP $status)"
TOKEN=$(curl -fsS -b "$JAR" -H 'X-Avsys: 1' -d '{"name":"e2e"}' "$URL/api/v1/me/tokens" | python3 -c 'import json,sys; print(json.load(sys.stdin)["secret"])')
rm -f "$JAR"
echo "ok: first account is the administrator, and signups then close"

AVSYS_URL=$URL AVSYS_TOKEN=$TOKEN AVSYS_SSH_PORT=$SSH_PORT AVSYS_ROUTER_PORT=$ROUTER_PORT AVSYS_E2E_KEEP_ON_FAILURE=1 ./hack/e2e.sh
