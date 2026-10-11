#!/bin/sh
# Measures the single-core speed of every ready node and records it as the
# avsys.dev/score label, which avSYS uses to decide where new servers go: the
# highest-scoring node that has room.
#
#   ./hack/rank-nodes.sh            measure and label
#   ./hack/rank-nodes.sh --dry-run  measure and print only
#
# Uses the current kubectl context. It runs one short pod per node in a
# temporary namespace (the 7-Zip benchmark on one thread, which leans on
# memory much as a game server does) and removes the namespace afterwards.
# Scores are only meaningful relative to each other.
set -eu

DRY=${1:-}
NS=avsys-bench-$$

cleanup() { kubectl delete namespace "$NS" --wait=false >/dev/null 2>&1 || true; }
trap cleanup EXIT

nodes=$(kubectl get nodes --no-headers | awk '$2 == "Ready" {print $1}')
[ -n "$nodes" ] || { echo "no ready nodes" >&2; exit 1; }

kubectl create namespace "$NS" >/dev/null
for n in $nodes; do
  cat <<POD | kubectl apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: bench-$n
  namespace: $NS
spec:
  restartPolicy: Never
  nodeSelector:
    kubernetes.io/hostname: $n
  containers:
    - name: bench
      image: alpine:3
      command: ["sh", "-c"]
      args:
        - |
          apk add --no-cache 7zip >/dev/null 2>&1 || exit 1
          for run in 1 2; do 7z b 1 -mmt1 -md24 | awk '/^Tot:/ {print \$4}'; done
POD
done

echo "measuring $(echo "$nodes" | wc -w | tr -d ' ') nodes..." >&2
for _ in $(seq 1 120); do
  left=$(kubectl -n "$NS" get pods --no-headers | grep -cvE "Completed|Error" || true)
  [ "$left" = 0 ] && break
  sleep 5
done

printf '%-16s %s\n' NODE SCORE
for n in $nodes; do
  # The better of the two runs: the slower one is more likely to have been disturbed.
  score=$(kubectl -n "$NS" logs "bench-$n" 2>/dev/null | sort -n | tail -1)
  if [ -z "$score" ]; then
    printf '%-16s %s\n' "$n" "failed"
    continue
  fi
  printf '%-16s %s\n' "$n" "$score"
  [ "$DRY" = "--dry-run" ] || kubectl label node "$n" "avsys.dev/score=$score" --overwrite >/dev/null
done | sort -k2 -rn
