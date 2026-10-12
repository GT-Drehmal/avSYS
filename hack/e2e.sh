#!/bin/sh
# End-to-end check against a running panel and its cluster: creates a throwaway
# organization and a Paper server with plugins, then exercises the paths that
# only a real server can prove, and cleans up after itself.
#
#   AVSYS_URL=http://localhost:8080 AVSYS_TOKEN=avsys_... ./hack/e2e.sh
#
# The token must belong to an instance admin (it creates an organization).
# Optional: AVSYS_SSH_PORT (2222), AVSYS_ROUTER_HOST (127.0.0.1), AVSYS_ROUTER_PORT (25565).
# AVSYS_E2E_KEEP_ON_FAILURE=1 leaves the organization and server in place when a check fails.
set -eu

: "${AVSYS_URL:?set AVSYS_URL to the panel address}"
: "${AVSYS_TOKEN:?set AVSYS_TOKEN to an instance admin API token}"
SSH_PORT=${AVSYS_SSH_PORT:-2222}
ROUTER_HOST=${AVSYS_ROUTER_HOST:-127.0.0.1}
ROUTER_PORT=${AVSYS_ROUTER_PORT:-25565}
HERE=$(cd "$(dirname "$0")" && pwd)

API=$AVSYS_URL/api/v1
ORG=e2e-$(date +%s)
SRV=$API/orgs/$ORG/servers/probe
HOST=probe.$ORG.test
WORK=$(mktemp -d)

call() { curl -sS -H "Authorization: Bearer $AVSYS_TOKEN" "$@"; }
field() { python3 -c "import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1], {'d': d}))" "$1"; }
step() { printf '\n== %s\n' "$1"; }
fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }
expect() { case "$2" in *"$3"*) printf 'ok: %s\n' "$1" ;; *) fail "$1: expected to find '$3' in: $2" ;; esac; }

cleanup() {
  status=$?
  if [ $status -ne 0 ] && [ -n "${AVSYS_E2E_KEEP_ON_FAILURE:-}" ]; then
    # Whoever started this wants to look at what is left, and will remove it.
    # The key goes regardless: its private half is deleted with $WORK, so it
    # would only be clutter on the administrator's account.
    [ -n "${KEY_ID:-}" ] && call -X DELETE "$API/me/ssh-keys/$KEY_ID" >/dev/null 2>&1 || true
    rm -rf "$WORK"
    echo "FAILED (exit $status); $ORG left in place"
    return
  fi
  step "cleaning up"
  for name in $(call "$API/orgs/$ORG/servers" | field "' '.join(s['name'] for s in d)" 2>/dev/null); do
    call -X DELETE "$API/orgs/$ORG/servers/$name" >/dev/null 2>&1 || true
  done
  # The organization can only go once its servers are gone.
  for _ in $(seq 1 30); do
    [ "$(call "$API/orgs/$ORG/servers" | field 'len(d)' 2>/dev/null)" = 0 ] && break
    sleep 2
  done
  call -X DELETE "$API/orgs/$ORG?snapshots=delete" >/dev/null 2>&1 || true
  [ -n "${KEY_ID:-}" ] && call -X DELETE "$API/me/ssh-keys/$KEY_ID" >/dev/null 2>&1 || true
  rm -rf "$WORK"
  [ $status -eq 0 ] && echo "PASS" || echo "FAILED (exit $status)"
}
trap cleanup EXIT

# The last lines of the game's log, for explaining a failure. The stream sends
# a reset each time it reattaches, followed by the log so far.
game_log() {
  call -N --max-time 5 "${1:-$SRV}/console" 2>/dev/null | python3 -c '
import json, sys
lines = []
for raw in sys.stdin:
    if not raw.startswith("data: "):
        continue
    try:
        data = json.loads(raw[6:])
    except ValueError:
        continue
    if data is None:
        lines = []
    elif isinstance(data, list):
        lines += [str(line) for line in data]
print("\n".join("  | " + line for line in lines[-40:]))' || true
}

# wait_phase PHASE [SERVER]: the probe unless another server is named.
wait_phase() {
  target=${2:-$SRV}
  for _ in $(seq 1 120); do
    phase=$(call "$target" | field "d['status'].get('phase')")
    [ "$phase" = "$1" ] && return 0
    if [ "$phase" = Error ]; then
      message=$(call "$target" | field "d['status'].get('message')")
      printf 'server went to Error: %s\nThe end of its log:\n' "$message" >&2
      game_log "$target" >&2
      fail "server went to Error: $message"
    fi
    sleep 3
  done
  printf 'The end of its log:\n' >&2
  game_log "$target" >&2
  fail "timed out waiting for $1 (last phase: $phase)"
}

step "organization and server"
call -d "{\"name\":\"E2E $ORG\",\"slug\":\"$ORG\"}" "$API/orgs" >/dev/null
POLICY=$API/orgs/$ORG/backup-policy
expect "daily snapshots are on unless switched off" "$(call "$POLICY" | field "(d['enabled'], d['keep']['daily'], 'next' in d)")" "(True, 7, True)"
expect "a policy that keeps nothing is refused" "$(call -o /dev/null -w '%{http_code}' -X PUT -d '{"enabled":true,"minute":540,"keep":{"daily":0}}' "$POLICY")" 400
# Off for this run, so that nothing happens by the clock in the middle of it.
# The schedule is run by hand further down.
expect "they can be switched off" "$(call -X PUT -d '{"enabled":false,"minute":540,"keep":{"daily":7,"weekly":4,"monthly":12,"yearly":2}}' "$POLICY" | field "(d['enabled'], 'next' in d)")" "(False, False)"
# WorldEdit is here on purpose: its newest builds need a newer Java than this
# game version ships with, so it proves a version that actually loads is chosen.
created=$(call -d "{\"name\":\"probe\",\"spec\":{\"eula\":true,\"loader\":\"paper\",\"version\":\"1.21.4\",
  \"resources\":{\"memory\":\"2Gi\"},\"storage\":{\"size\":\"2Gi\"},\"expose\":{\"hostname\":\"$HOST\"},
  \"properties\":{\"motd\":\"e2e probe\"},
  \"mods\":[{\"source\":\"modrinth\",\"kind\":\"plugin\",\"project\":\"luckperms\"},{\"source\":\"modrinth\",\"kind\":\"plugin\",\"project\":\"worldedit\"}]}}" "$API/orgs/$ORG/servers")
case "$created" in *'"spec"'*) ;; *) fail "the server was not created: $created" ;; esac
expect "content is pinned when added" "$(echo "$created" | field "all(m.get('version') for m in d['spec']['mods'])")" True
expect "content fits the game's Java" "$(echo "$created" | field "d['java']['effective'] == d['java']['game'] == 21")" True
wait_phase Running
echo "ok: server reached Running"

step "console"
plugins=$(call -d '{"command":"plugins"}' "$SRV/console" | field "d['output']")
expect "LuckPerms is loaded" "$plugins" LuckPerms
expect "WorldEdit is loaded" "$plugins" WorldEdit
expect "log is streamed" "$(call -N --max-time 4 "$SRV/console" 2>/dev/null || true)" "Done ("
# Typed at the server console, a message is from the server. Over RCON the
# game would call its sender Rcon.
expect "a command can be typed at the server console" "$(call -d '{"command":"say hello from the panel","as":"server"}' "$SRV/console" | field "d['output']")" ""
sleep 2
expect "and is then from the server" "$(call -N --max-time 4 "$SRV/console" 2>/dev/null || true)" "[Server] hello from the panel"
expect "one command at a time" "$(call -o /dev/null -w '%{http_code}' -d '{"command":"say a\nsay b","as":"server"}' "$SRV/console")" 400

step "files"
echo "written by e2e" | call -X PUT --data-binary @- "$SRV/files/content?path=/e2e.txt" >/dev/null
expect "file round trip" "$(call "$SRV/files/content?path=/e2e.txt")" "written by e2e"
expect "managed server.properties" "$(call "$SRV/files/content?path=/server.properties")" "motd=e2e probe"

step "snapshots"
snap=$(call -X POST "$SRV/snapshots?wait=true")
SNAP=$(echo "$snap" | field "d['id']") || fail "no snapshot was taken: $snap"
expect "a running server is snapshotted" "$(echo "$snap" | field "(d['server'], d['trigger'], d['version'], d['loader'], d['files'] > 10)")" "('probe', 'manual', '1.21.4', 'paper', True)"
expect "the game goes back to saving" "$(call -d '{"command":"save-on"}' "$SRV/console" | field "d['output']")" "already turned on"
# Change a file, add one, and take away a plugin, then go back.
echo "changed after the snapshot" | call -X PUT --data-binary @- "$SRV/files/content?path=/e2e.txt" >/dev/null
echo "not in the snapshot" | call -X PUT --data-binary @- "$SRV/files/content?path=/stray.txt" >/dev/null
call -X PATCH -d '{"spec":{"mods":[{"source":"modrinth","kind":"plugin","project":"luckperms"}]}}' "$SRV" >/dev/null
restored=$(call -X POST "$SRV/snapshots/$SNAP/restore?wait=true")
expect "a restore is accepted" "$(echo "$restored" | field "d['id'] == '$SNAP'")" True
wait_phase Running
expect "a changed file is put back" "$(call "$SRV/files/content?path=/e2e.txt")" "written by e2e"
expect "a file added since is removed" "$(call -o /dev/null -w '%{http_code}' "$SRV/files/content?path=/stray.txt")" 404
expect "the settings come back with the files" "$(call "$SRV" | field "sorted(m['project'] for m in d['spec']['mods'])")" "['luckperms', 'worldedit']"
expect "the plugin runs again" "$(call -d '{"command":"plugins"}' "$SRV/console" | field "d['output']")" WorldEdit
listed=$(call "$SRV/snapshots")
expect "what was there before the restore is kept too" "$(echo "$listed" | field "sorted(s['trigger'] for s in d['snapshots'])")" "['manual', 'pre-restore']"
expect "the server shows its last snapshot" "$(call "$SRV" | field "d['status']['snapshots'].get('error', '') == '' and len(d['status']['snapshots']['lastID']) == 64")" True

step "ssh"
ssh-keygen -q -t ed25519 -N '' -f "$WORK/key" -C avsys-e2e
KEY_ID=$(call -d "{\"name\":\"e2e\",\"publicKey\":\"$(cat "$WORK/key.pub")\"}" "$API/me/ssh-keys" | field "d['id']")
ssh_host=$(echo "$AVSYS_URL" | sed -E 's#^[a-z]+://##; s#[:/].*$##')
out=$(ssh -i "$WORK/key" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
  -p "$SSH_PORT" "probe.$ORG@$ssh_host" 'pwd; cat /data/e2e.txt; mc run list')
expect "shell lands in the server directory" "$out" /data
expect "shell sees the same files" "$out" "written by e2e"
expect "mc helper reaches the game" "$out" "players online"

step "router"
expect "status is proxied by hostname" "$(python3 "$HERE/mcping.py" "$HOST" status "$ROUTER_HOST" "$ROUTER_PORT")" "e2e probe"
expect "unknown hostnames are answered" "$(python3 "$HERE/mcping.py" "nope.$ORG.test" status "$ROUTER_HOST" "$ROUTER_PORT")" "Unknown server"

step "stop keeps the workspace"
call -X POST "$SRV/stop" >/dev/null
wait_phase Stopped
expect "files reachable while stopped" "$(call "$SRV/files/content?path=/e2e.txt")" "written by e2e"
expect "router says offline" "$(python3 "$HERE/mcping.py" "$HOST" status "$ROUTER_HOST" "$ROUTER_PORT")" "Offline"
stopped=$(call -X POST "$SRV/snapshots?wait=true")
LAST=$(echo "$stopped" | field "d['id']") || fail "no snapshot was taken of the stopped server: $stopped"
expect "a stopped server is snapshotted" "$(echo "$stopped" | field "d['trigger']")" manual

step "scheduled snapshots"
RUN="$POLICY/run?wait=true"
outcome="[(r['server'], r['outcome'], r.get('removed', 0)) for r in d['results']]"
expect "a server nothing has happened to gets no new snapshot" "$(call -X POST "$RUN" | field "$outcome")" "[('probe', 'unchanged', 0)]"
expect "but is marked as having had its run" "$(call "$SRV" | field "'lastRun' in d['status']['snapshots']")" True
echo "something happened" | call -X PUT --data-binary @- "$SRV/files/content?path=/scheduled.txt" >/dev/null
expect "one that has changed gets one" "$(call -X POST "$RUN" | field "$outcome")" "[('probe', 'snapshot', 0)]"
# Keep a single day's worth, and take another the same day: the older one goes.
call -X PUT -d '{"enabled":false,"minute":540,"keep":{"daily":1}}' "$POLICY" >/dev/null
echo "and again" | call -X PUT --data-binary @- "$SRV/files/content?path=/scheduled.txt" >/dev/null
expect "what the policy no longer keeps is removed" "$(call -X POST "$RUN" | field "$outcome")" "[('probe', 'snapshot', 1)]"
expect "and only scheduled snapshots are" "$(call "$SRV/snapshots" | field "sorted(s['trigger'] for s in d['snapshots'])")" "['manual', 'manual', 'pre-restore', 'scheduled']"

step "a new server from a snapshot"
COPY=$API/orgs/$ORG/servers/copy
expect "a snapshot says what its server was" "$(call "$API/orgs/$ORG/snapshots/$LAST" | field "(d['server'], d['spec']['version'], d['spec']['loader'])")" "('probe', '1.21.4', 'paper')"
expect "its software is not chosen again" "$(call -o /dev/null -w '%{http_code}' -d "{\"name\":\"copy\",\"fromSnapshot\":\"$LAST\",\"spec\":{\"version\":\"1.20.1\"}}" "$API/orgs/$ORG/servers")" 400
# Answers once the files are in. It is then started, being asked for nothing else.
copied=$(call -d "{\"name\":\"copy\",\"fromSnapshot\":\"$LAST\",\"spec\":{\"expose\":{\"hostname\":\"copy.$ORG.test\"}}}" "$API/orgs/$ORG/servers?wait=true")
case "$copied" in *'"spec"'*) ;; *) fail "no server was made from the snapshot: $copied" ;; esac
expect "it has the snapshot's software and settings" "$(echo "$copied" | field "(d['spec']['version'], d['spec']['loader'], sorted(m['project'] for m in d['spec']['mods']), d['spec']['properties']['motd'], d['spec']['resources']['memory'])")" "('1.21.4', 'paper', ['luckperms', 'worldedit'], 'e2e probe', '2Gi')"
expect "and an address of its own" "$(echo "$copied" | field "(d['spec']['expose']['hostname'], d['spec']['state'])")" "('copy.$ORG.test', 'Running')"
expect "it has the snapshot's files" "$(call "$COPY/files/content?path=/e2e.txt")" "written by e2e"
wait_phase Running "$COPY"
expect "and runs on them" "$(call -d '{"command":"plugins"}' "$COPY/console" | field "d['output']")" WorldEdit
expect "players reach it by its own name" "$(python3 "$HERE/mcping.py" "copy.$ORG.test" status "$ROUTER_HOST" "$ROUTER_PORT")" "e2e probe"
expect "the server it was copied from is as it was" "$(call "$SRV" | field "(d['status']['phase'], d['spec']['expose']['hostname'])")" "('Stopped', '$HOST')"
expect "it has no history of its own yet" "$(call "$COPY/snapshots" | field "len(d['snapshots'])")" 0
call -X DELETE "$COPY" >/dev/null
# One that can never get its files, being bound for a node that does not exist.
call -d "{\"name\":\"stuck\",\"fromSnapshot\":\"$LAST\",\"spec\":{\"nodeSelector\":{\"avsys.dev/no-such-node\":\"true\"}}}" "$API/orgs/$ORG/servers" >/dev/null
expect "a server still waiting for its files is not started" "$(call -X POST "$API/orgs/$ORG/servers/stuck/start" | field "d['code']")" creating
expect "deleting it calls the copy off" "$(call -o /dev/null -w '%{http_code}' -X DELETE "$API/orgs/$ORG/servers/stuck")" 204

step "snapshots outlive the server"
call -X DELETE "$SRV" >/dev/null
for _ in $(seq 1 30); do
  [ "$(call "$API/orgs/$ORG/servers" | field 'len(d)')" = 0 ] && break
  sleep 2
done
history=$(call "$API/orgs/$ORG/snapshots")
expect "a deleted server's snapshots are still listed" "$(echo "$history" | field "sorted({s['server'] for s in d['snapshots']}), len(d['snapshots']), d['usage']['bytes'] > 0")" "(['probe'], 4, True)"
expect "an organization with snapshots is not deleted by accident" "$(call -o /dev/null -w '%{http_code}' -X DELETE "$API/orgs/$ORG")" 409
expect "a snapshot can be removed on purpose" "$(call -o /dev/null -w '%{http_code}' -X DELETE "$API/orgs/$ORG/snapshots/$SNAP")" 204
expect "and is then gone" "$(call "$API/orgs/$ORG/snapshots" | field "len(d['snapshots'])")" 3

step "a deleted server comes back"
back=$(call -d "{\"name\":\"probe\",\"fromSnapshot\":\"$LAST\",\"spec\":{\"state\":\"Stopped\",\"expose\":{\"hostname\":\"$HOST\"}}}" "$API/orgs/$ORG/servers?wait=true")
case "$back" in *'"spec"'*) ;; *) fail "the deleted server was not brought back: $back" ;; esac
expect "under its own name, left stopped as asked" "$(echo "$back" | field "(d['name'], d['spec']['state'], d['spec']['expose']['hostname'])")" "('probe', 'Stopped', '$HOST')"
expect "with its files" "$(call "$SRV/files/content?path=/e2e.txt")" "written by e2e"
expect "and its history" "$(call "$SRV/snapshots" | field "len(d['snapshots'])")" 3
# And again with no pause, while what is left of the one just deleted is still
# being cleared away. Its files must not land in a volume that is on its way out.
call -X DELETE "$SRV" >/dev/null
again=$(call -d "{\"name\":\"probe\",\"fromSnapshot\":\"$LAST\",\"spec\":{\"expose\":{\"hostname\":\"$HOST\"}}}" "$API/orgs/$ORG/servers?wait=true")
case "$again" in *'"spec"'*) ;; *) fail "the server was not brought back straight after being deleted: $again" ;; esac
wait_phase Running
expect "brought back straight after a delete, it keeps its files once it runs" "$(call "$SRV/files/content?path=/e2e.txt")" "written by e2e"
expect "and its plugins" "$(call -d '{"command":"plugins"}' "$SRV/console" | field "d['output']")" WorldEdit
