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
# AVSYS_E2E_CONTAINER_LOG is a command that prints the whole log of a server's
# running game container, given the organization and the server as $1 and
# $2: what the image prints before the game starts is only there.
# AVSYS_E2E_EXITED_LOG is a command that prints the log of a server's game as
# it last exited, given the organization and the server as $1 and $2. The
# panel's console shows the game that is running, which after an exit is the
# next one; why the last one stopped is only in its own log.
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
    call -X DELETE "$API/orgs/$ORG/servers/$name?snapshot=false" >/dev/null 2>&1 || true
  done
  # The organization can only go once its servers are gone.
  for _ in $(seq 1 30); do
    [ "$(call "$API/orgs/$ORG/servers" | field 'len(d)' 2>/dev/null)" = 0 ] && break
    sleep 2
  done
  call -X DELETE "$API/orgs/$ORG?snapshots=delete" >/dev/null 2>&1 || true
  [ -n "${KEY_ID:-}" ] && call -X DELETE "$API/me/ssh-keys/$KEY_ID" >/dev/null 2>&1 || true
  rm -rf "$WORK"
  [ "${REFETCHED:-0}" -gt 0 ] && echo "note: $REFETCHED time(s) the game could not fetch what it runs and was started again"
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
# What the game image prints when it could not fetch what it runs: the jar's
# own site did not answer, or its name did not resolve. That is somebody
# else's service or the network between, not what is being tested here, and
# the cluster starts the game again by itself. Only the image's own line
# about a download counts: a timeout mentioned anywhere else in the log, by
# the game or a plugin, says nothing about why the game stopped.
FETCH_FAILED='\[init\].*\[ERROR\].*[Dd]ownload'
# How many times that happened in this run, said at the end so that it shows.
REFETCHED=0

# exited_log SERVER_URL: the log of the game that last exited there, if this
# run was told how to get at it, and nothing if not.
exited_log() {
  [ -n "${AVSYS_E2E_EXITED_LOG:-}" ] || return 0
  org=$(printf '%s' "$1" | sed -E 's|.*/orgs/([^/]+)/servers/.*|\1|')
  server=$(printf '%s' "$1" | sed -E 's|.*/servers/([^/]+).*|\1|')
  sh -c "$AVSYS_E2E_EXITED_LOG" exited-log "$org" "$server" 2>/dev/null | sed 's/^/  | /' || true
}

wait_phase() {
  target=${2:-$SRV}
  excused=0
  # One limit for all of it, six minutes of looks three seconds apart, which
  # the wait for a restart below is taken out of.
  looks=120
  while [ "$looks" -gt 0 ]; do
    looks=$((looks - 1))
    phase=$(call "$target" | field "d['status'].get('phase')")
    [ "$phase" = "$1" ] && return 0
    # The panel itself waits out a name it could not look up, as starting.
    # That is said here, so that it shows in the run.
    if [ "$phase" = Starting ]; then
      message=$(call "$target" | field "d['status'].get('message') or ''")
      case "$message" in *"could not look up"*)
        if [ "$message" != "${noted:-}" ]; then
          noted=$message
          REFETCHED=$((REFETCHED + 1))
          printf 'note: %s\n' "$message"
        fi ;;
      esac
    fi
    if [ "$phase" = Error ]; then
      message=$(call "$target" | field "d['status'].get('message')")
      # Why it stopped is in the log of the game that exited. The console
      # may by now be showing the one started after it.
      log=$(exited_log "$target")
      [ -n "$log" ] || log=$(game_log "$target")
      # A failed fetch is waited out, twice at most. Anything else that
      # stops the game fails the run at once.
      if [ "$excused" -lt 2 ] && printf '%s' "$log" | grep -Eq "$FETCH_FAILED"; then
        excused=$((excused + 1))
        REFETCHED=$((REFETCHED + 1))
        printf 'note: the game could not fetch what it runs, and is being started again:\n%s\n' "$(printf '%s' "$log" | grep -E "$FETCH_FAILED|Caused by" | tail -3)"
        # Until the game has been started again, so that the same exit is not counted twice.
        while [ "$looks" -gt 0 ] && [ "$(call "$target" | field "d['status'].get('phase')")" = Error ]; do
          looks=$((looks - 1))
          sleep 3
        done
        continue
      fi
      printf 'server went to Error: %s\nThe end of its log:\n' "$message" >&2
      printf '%s\n' "$log" >&2
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
expect "a new server has Aikar's flags" "$(echo "$created" | field "d['spec']['jvm']")" "{'aikarFlags': True}"
wait_phase Running
echo "ok: server reached Running"
if [ -n "${AVSYS_E2E_CONTAINER_LOG:-}" ]; then
  expect "and the game was started with them" "$(sh -c "$AVSYS_E2E_CONTAINER_LOG" container-log "$ORG" probe 2>/dev/null)" "Using Aikar's flags"
else
  echo "skipped: the game's own container log is not available to this run"
fi

step "console"
plugins=$(call -d '{"command":"plugins"}' "$SRV/console" | field "d['output']")
expect "LuckPerms is loaded" "$plugins" LuckPerms
expect "WorldEdit is loaded" "$plugins" WorldEdit
expect "log is streamed" "$(call -N --max-time 4 "$SRV/console" 2>/dev/null || true)" "Done ("
# Typed at the server console, a message is from the server. Over RCON the
# game would call its sender Rcon.
expect "a command typed at the server console answers with nothing, its output being in the log" "$(call -d '{"command":"say hello from the panel","as":"server"}' "$SRV/console" | field "d['output'] == ''")" True
sleep 2
expect "and is then from the server" "$(call -N --max-time 4 "$SRV/console" 2>/dev/null || true)" "[Server] hello from the panel"
expect "one command at a time" "$(call -o /dev/null -w '%{http_code}' -d '{"command":"say a\nsay b","as":"server"}' "$SRV/console")" 400

step "files"
echo "written by e2e" | call -X PUT --data-binary @- "$SRV/files/content?path=/e2e.txt" >/dev/null
expect "file round trip" "$(call "$SRV/files/content?path=/e2e.txt")" "written by e2e"
expect "managed server.properties" "$(call "$SRV/files/content?path=/server.properties")" "motd=e2e probe"

step "datapacks"
# restarted asks for a restart and waits it out: down first, then up.
restarted() {
  call -X POST "$SRV/restart" >/dev/null
  for _ in $(seq 1 90); do
    [ "$(call "$SRV" | field "d['status'].get('phase')")" != Running ] && break
    sleep 1
  done
  wait_phase Running
}
# One from a URL, which the image only ever adds: taking it off has to take
# its file away too, or the game goes on loading it.
PACK=https://cdn.modrinth.com/data/OhduvhIc/versions/8cEJouzY/Veinminer-1.2.4.zip
mods=$(call "$SRV" | field "__import__('json').dumps(d['spec']['mods'])")
with=$(python3 -c 'import json, sys; m = json.loads(sys.argv[1]); m.append({"source": "url", "kind": "datapack", "project": sys.argv[2]}); print(json.dumps({"spec": {"mods": m}}))' "$mods" "$PACK")
call -X PATCH -d "$with" "$SRV" >/dev/null
restarted
expect "a datapack from a URL is installed" "$(call -o /dev/null -w '%{http_code}' "$SRV/files/content?path=/world/datapacks/Veinminer-1.2.4.zip")" 200
expect "and enabled" "$(call -d '{"command":"datapack list enabled"}' "$SRV/console" | field "d['output']")" "Veinminer-1.2.4.zip"
call -X PATCH -d "{\"spec\":{\"mods\":$mods}}" "$SRV" >/dev/null
expect "taken off, its file is removed" "$(call -o /dev/null -w '%{http_code}' "$SRV/files/content?path=/world/datapacks/Veinminer-1.2.4.zip")" 404
restarted
expect "and the game no longer has it" "$(call -d '{"command":"datapack list"}' "$SRV/console" | field "'Veinminer' not in d['output']")" True

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
# The game started again, and packed the log of its run before.
earlier=$(call "$SRV/logs" | field "[l['name'] for l in d['logs'] if l['kind'] == 'log'][0]") || fail "no earlier run is listed"
expect "the run before a restart is listed" "$(call "$SRV/logs" | field "(d['logs'][0]['name'], d['logs'][0]['kind'])")" "('logs/latest.log', 'current')"
expect "and reads unpacked, to where it got going" "$(call "$SRV/logs/file?name=$earlier" | field "any('Done (' in l['text'] for l in d['lines'])")" True
expect "and is searched where it is" "$(call "$SRV/logs/file?name=$earlier&q=Done%20(" | field "all('Done (' in l['text'] for l in d['lines'] if l.get('match')) and len(d['lines']) > 0")" True
expect "nothing but the game's logs is read" "$(call -o /dev/null -w '%{http_code}' "$SRV/logs/file?name=logs/../server.properties")" 400
expect "the plugin runs again" "$(call -d '{"command":"plugins"}' "$SRV/console" | field "d['output']")" WorldEdit
listed=$(call "$SRV/snapshots")
expect "what was there before the restore is kept too" "$(echo "$listed" | field "sorted(s['trigger'] for s in d['snapshots'])")" "['manual', 'pre-restore']"
expect "the server shows its last snapshot" "$(call "$SRV" | field "d['status']['snapshots'].get('error', '') == '' and len(d['status']['snapshots']['lastID']) == 64")" True

step "ssh"
ssh-keygen -q -t ed25519 -N '' -f "$WORK/key" -C avsys-e2e
KEY_ID=$(call -d "{\"name\":\"e2e\",\"publicKey\":\"$(cat "$WORK/key.pub")\"}" "$API/me/ssh-keys" | field "d['id']")
ssh_host=$(echo "$AVSYS_URL" | sed -E 's#^[a-z]+://##; s#[:/].*$##')
out=$(ssh -i "$WORK/key" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
  -p "$SSH_PORT" "probe.$ORG@$ssh_host" 'pwd; cat /data/e2e.txt; mc run list; echo "say hello from a shell" | mc console 2>&1 | tail -3')
expect "shell lands in the server directory" "$out" /data
expect "shell sees the same files" "$out" "written by e2e"
expect "mc helper reaches the game" "$out" "players online"
expect "mc console types at the server console and shows the log" "$out" "[Server] hello from a shell"
# What someone installs for themselves goes in their home, which is on the
# server's storage, and is found; what would need root says what to do instead.
own=$(ssh -i "$WORK/key" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
  -p "$SSH_PORT" "probe.$ORG@$ssh_host" 'mkdir -p ~/.local/bin && printf "#!/bin/sh\necho a tool of my own\n" > ~/.local/bin/mytool && chmod +x ~/.local/bin/mytool && bash -lc mytool; python3 -c "print(6 * 7)"; sudo true 2>&1 | head -1; apt-get -o APT::Color=0 install nothing 2>&1 | head -1; echo "restic is $(command -v restic)"' 2>&1) || true
expect "a program put in ~/.local/bin is found by a login shell" "$own" "a tool of my own"
# A plain command, which is how the panel runs restic here, does not look there.
expect "the panel's own commands do not look in anyone's home" "$own" "restic is /usr/local/bin/restic"
expect "python is there" "$own" "42"
expect "sudo and apt say why not, and what to do instead" "$own" "There is no root in a server's workspace"

step "router"
expect "status is proxied by hostname" "$(python3 "$HERE/mcping.py" "$HOST" status "$ROUTER_HOST" "$ROUTER_PORT")" "e2e probe"
expect "unknown hostnames are answered" "$(python3 "$HERE/mcping.py" "nope.$ORG.test" status "$ROUTER_HOST" "$ROUTER_PORT")" "Unknown server"

step "nodes"
# The node the panel would choose, which is a ready one with room.
NODE=$(call "$API/nodes?memory=1Gi" | field "d['automatic']") || fail "no node would be chosen"
expect "the cluster's nodes are listed, with where a server would go" "$(call "$API/nodes?memory=1Gi" | field "[(n['ready'], n['free'] > 0) for n in d['nodes'] if n['name'] == d['automatic']]")" "[(True, True)]"
expect "a node that is not there cannot be chosen" "$(call -o /dev/null -w '%{http_code}' -d '{"name":"placed","node":"no-such-node","spec":{"eula":true,"loader":"vanilla","version":"1.21.4","state":"Stopped"}}' "$API/orgs/$ORG/servers")" 400
call -d "{\"name\":\"placed\",\"node\":\"$NODE\",\"spec\":{\"eula\":true,\"loader\":\"vanilla\",\"version\":\"1.21.4\",\"state\":\"Stopped\",\"resources\":{\"memory\":\"1Gi\"},\"storage\":{\"size\":\"1Gi\"}}}" "$API/orgs/$ORG/servers" >/dev/null
for _ in $(seq 1 60); do
  [ "$(call "$API/orgs/$ORG/servers/placed" | field "d['status'].get('node', '')")" = "$NODE" ] && break
  sleep 1
done
expect "a server put on a node runs there" "$(call "$API/orgs/$ORG/servers/placed" | field "d['status'].get('node')")" "$NODE"
call -X DELETE "$API/orgs/$ORG/servers/placed?snapshot=false" >/dev/null

step "stop keeps the workspace"
call -X POST "$SRV/stop" >/dev/null
wait_phase Stopped
expect "files reachable while stopped" "$(call "$SRV/files/content?path=/e2e.txt")" "written by e2e"
expect "router says offline" "$(python3 "$HERE/mcping.py" "$HOST" status "$ROUTER_HOST" "$ROUTER_PORT")" "Offline"
# Listed as a server the game will not let anyone into, not as an empty one.
expect "and lists it as closed" "$(python3 "$HERE/mcping.py" "$HOST" status "$ROUTER_HOST" "$ROUTER_PORT" | field "(d['version']['name'], d['version']['protocol'] < 0)")" "('Offline', True)"
# Its own icon is in the list while the game is not running to send it.
python3 -c '
import struct, sys, zlib
w = h = 64
rows = b"".join(b"\x00" + bytes((40, 180, 140)) * w for _ in range(h))
chunk = lambda t, d: struct.pack(">I", len(d)) + t + d + struct.pack(">I", zlib.crc32(t + d) & 0xffffffff)
sys.stdout.buffer.write(b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0)) + chunk(b"IDAT", zlib.compress(rows)) + chunk(b"IEND", b""))' > "$WORK/icon.png"
expect "a server is given an icon" "$(call -o /dev/null -w '%{http_code}' -X PUT --data-binary "@$WORK/icon.png" "$SRV/icon")" 204
expect "which is written where the game looks for it" "$(call "$SRV/files/content?path=/server-icon.png" | python3 -c 'import struct, sys; d = sys.stdin.buffer.read(); print(d[1:4].decode(), struct.unpack(">II", d[16:24]))')" "PNG (64, 64)"
shown=$(call "$SRV/icon" | python3 -c 'import hashlib, sys; print(hashlib.sha1(sys.stdin.buffer.read()).hexdigest())')
expect "and shown in the server list though the server is off" "$(python3 "$HERE/mcping.py" "$HOST" status "$ROUTER_HOST" "$ROUTER_PORT" | field "d.get('icon')")" "$shown"
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
# A server can leave its organization's schedule, and come back to it.
expect "a server can switch its own off" "$(call -X PATCH -d '{"spec":{"backups":{"enabled":false}}}' "$SRV" | field "d['spec']['backups']")" "{'enabled': False}"
expect "a run passes over it" "$(call -X POST "$RUN" | field "$outcome")" "[('probe', 'skipped', 0)]"
expect "and it can follow the organization again" "$(call -X PATCH -d '{"spec":{"backups":null}}' "$SRV" | field "'backups' in d['spec']")" False
expect "changing its schedule did not count as the server changing" "$(call -X POST "$RUN" | field "$outcome")" "[('probe', 'unchanged', 0)]"

step "offsite copy"
OFFSITE=$API/orgs/$ORG/offsite
if [ "$(call "$OFFSITE" | field "d['configured']")" = True ]; then
  # The panel also copies on its own, a little after snapshots change, so a
  # copy asked for here may find one going, or find its work already done.
  # What is checked is where things stand afterwards.
  copy_offsite() {
    for _ in $(seq 1 60); do
      out=$(call -X POST "$OFFSITE/run?wait=true")
      case "$out" in *'"busy"'*) sleep 2 ;; *) echo "$out"; return ;; esac
    done
  }
  here() { call "$API/orgs/$ORG/snapshots" | field "len(d['snapshots'])"; }
  there() { call "$OFFSITE/snapshots" | field "len(d['snapshots'])"; }
  copy_offsite >/dev/null
  expect "every snapshot is copied offsite" "$(there)" "$(here)"
  expect "and nothing is left waiting" "$(call "$OFFSITE" | field "(d['pending'], 'error' in d)")" "(False, False)"
  extra=$(call -X POST "$SRV/snapshots?wait=true" | field "d['id']") || fail "no snapshot was taken for the offsite check"
  expect "a new one is waiting to be copied" "$(call "$OFFSITE" | field "d['pending']")" True
  copy_offsite >/dev/null
  expect "and is then there too" "$(there)" "$(here)"
  expect "a snapshot can be deleted" "$(call -o /dev/null -w '%{http_code}' -X DELETE "$API/orgs/$ORG/snapshots/$extra?wait=true")" 204
  copy_offsite >/dev/null
  expect "one deleted here is removed there" "$(there)" "$(here)"
  expect "snapshots are only brought back into an organization that has none" "$(call -X POST "$OFFSITE/fetch" | field "d['code']")" has_snapshots
else
  echo "skipped: this panel copies snapshots nowhere else"
fi

step "a new server from a snapshot"
COPY=$API/orgs/$ORG/servers/copy
expect "a snapshot says what its server was" "$(call "$API/orgs/$ORG/snapshots/$LAST" | field "(d['server'], d['spec']['version'], d['spec']['loader'])")" "('probe', '1.21.4', 'paper')"
expect "its modpack is not chosen again" "$(call -o /dev/null -w '%{http_code}' -d "{\"name\":\"copy\",\"fromSnapshot\":\"$LAST\",\"spec\":{\"modpack\":{\"source\":\"modrinth\",\"project\":\"fabulously-optimized\"}}}" "$API/orgs/$ORG/servers")" 400
expect "nor is it put on a version its software has no build for" "$(call -o /dev/null -w '%{http_code}' -d "{\"name\":\"copy\",\"fromSnapshot\":\"$LAST\",\"spec\":{\"version\":\"1.2.3\"}}" "$API/orgs/$ORG/servers")" 400
# Answers once the files are in. It is then started, being asked for nothing
# else, and given one of the original's two plugins.
# And a picture of its own, in place of the one its files bring.
own=$(python3 -c '
import base64, struct, zlib
w = h = 64
rows = b"".join(b"\x00" + bytes((200, 60, 60)) * w for _ in range(h))
chunk = lambda t, d: struct.pack(">I", len(d)) + t + d + struct.pack(">I", zlib.crc32(t + d) & 0xffffffff)
print(base64.b64encode(b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0)) + chunk(b"IDAT", zlib.compress(rows)) + chunk(b"IEND", b"")).decode())')
copied=$(call -d "{\"name\":\"copy\",\"fromSnapshot\":\"$LAST\",\"picture\":\"$own\",\"spec\":{\"expose\":{\"hostname\":\"copy.$ORG.test\"},\"mods\":[{\"source\":\"modrinth\",\"kind\":\"plugin\",\"project\":\"luckperms\"}]}}" "$API/orgs/$ORG/servers?wait=true")
case "$copied" in *'"spec"'*) ;; *) fail "no server was made from the snapshot: $copied" ;; esac
expect "it has the snapshot's software and settings, and the plugins it was given" "$(echo "$copied" | field "(d['spec']['version'], d['spec']['loader'], [(m['project'], bool(m.get('version'))) for m in d['spec']['mods']], d['spec']['properties']['motd'], d['spec']['resources']['memory'])")" "('1.21.4', 'paper', [('luckperms', True)], 'e2e probe', '2Gi')"
expect "and an address of its own" "$(echo "$copied" | field "(d['spec']['expose']['hostname'], d['spec']['state'])")" "('copy.$ORG.test', 'Running')"
expect "it has the snapshot's files" "$(call "$COPY/files/content?path=/e2e.txt")" "written by e2e"
expect "but the picture it was given, not the one they brought" "$(call "$COPY/icon" | python3 -c 'import hashlib, sys; d = sys.stdin.buffer.read(); print(len(d) > 0 and hashlib.sha1(d).hexdigest() != sys.argv[1])' "$shown")" True
wait_phase Running "$COPY"
# WorldEdit came with the files, and is taken away because the copy was not given it.
expect "and runs on them, with only the plugins it was given" "$(call -d '{"command":"plugins"}' "$COPY/console" | field "('LuckPerms' in d['output'], 'WorldEdit' in d['output'])")" "(True, False)"
expect "players reach it by its own name" "$(python3 "$HERE/mcping.py" "copy.$ORG.test" status "$ROUTER_HOST" "$ROUTER_PORT")" "e2e probe"
expect "the server it was copied from is as it was" "$(call "$SRV" | field "(d['status']['phase'], d['spec']['expose']['hostname'])")" "('Stopped', '$HOST')"
expect "it has no history of its own yet" "$(call "$COPY/snapshots" | field "len(d['snapshots'])")" 0
expect "a server can be deleted with no last snapshot of it" "$(call -o /dev/null -w '%{http_code}' -X DELETE "$COPY?snapshot=false")" 204
# One that can never get its files, being bound for a node that does not exist.
call -d "{\"name\":\"stuck\",\"fromSnapshot\":\"$LAST\",\"spec\":{\"nodeSelector\":{\"avsys.dev/no-such-node\":\"true\"}}}" "$API/orgs/$ORG/servers" >/dev/null
expect "a server still waiting for its files is not started" "$(call -X POST "$API/orgs/$ORG/servers/stuck/start" | field "d['code']")" creating
expect "deleting it calls the copy off" "$(call -o /dev/null -w '%{http_code}' -X DELETE "$API/orgs/$ORG/servers/stuck")" 204

step "duplicating a server"
TWIN=$API/orgs/$ORG/servers/twin
echo "only in the original" | call -X PUT --data-binary @- "$SRV/files/content?path=/twin.txt" >/dev/null
call -d '{"name":"twin","fromServer":"probe","spec":{"state":"Stopped"}}' "$API/orgs/$ORG/servers?wait=true" > "$WORK/twin.json" &
duplicating=$!
# Its picture is asked for while its files are on their way, as a list of
# servers does: it has none yet, and that is not to be remembered.
asked=""
for _ in $(seq 1 600); do
  if [ "$(call -o /dev/null -w '%{http_code}' "$TWIN")" = 200 ]; then
    asked=$(call -o /dev/null -w '%{http_code}' "$TWIN/icon")
    break
  fi
  kill -0 "$duplicating" 2>/dev/null || break
  sleep 0.2
done
wait "$duplicating" || true
twin=$(cat "$WORK/twin.json")
case "$twin" in *'"spec"'*) ;; *) fail "the server was not duplicated: $twin" ;; esac
expect "a copy has the original's software" "$(echo "$twin" | field "(d['spec']['version'], d['spec']['loader'], sorted(m['project'] for m in d['spec']['mods']))")" "('1.21.4', 'paper', ['luckperms', 'worldedit'])"
expect "and its files as they were a moment ago" "$(call "$TWIN/files/content?path=/twin.txt")" "only in the original"
[ "$asked" = 404 ] || echo "note: the copy's picture was not asked for before its files were in (got '$asked')"
expect "and the picture that came with them, though it was asked for before" "$(call "$TWIN/icon" | python3 -c 'import hashlib, sys; print(hashlib.sha1(sys.stdin.buffer.read()).hexdigest())')" "$shown"
copies="len([s for s in d['snapshots'] if s['trigger'] == 'copy'])"
expect "it was made from a snapshot taken for it" "$(call "$SRV/snapshots" | field "$copies")" 1
# A copy that differs from its original in what the request names, and in nothing else.
second=$(call -d '{"name":"twin-2","fromServer":"probe","picture":null,"spec":{"state":"Stopped","properties":{"difficulty":"peaceful","motd":null},"autoWhitelist":"admin"}}' "$API/orgs/$ORG/servers?wait=true")
expect "a second copy with nothing changed in the original needs no new one" "$(call "$SRV/snapshots" | field "$copies")" 1
expect "a copy takes the settings it is asked to have" "$(echo "$second" | field "(d['spec']['properties'].get('difficulty'), 'motd' in d['spec']['properties'], d['spec']['autoWhitelist'])")" "('peaceful', False, 'admin')"
expect "and no picture, when asked for none" "$(call -o /dev/null -w '%{http_code}' "$API/orgs/$ORG/servers/twin-2/icon")" 404
expect "and the rest from its original" "$(echo "$second" | field "(d['spec']['version'], sorted(m['project'] for m in d['spec']['mods']))")" "('1.21.4', ['luckperms', 'worldedit'])"
expect "the original is as it was" "$(call "$SRV" | field "(d['status']['phase'], d['spec']['expose']['hostname'])")" "('Stopped', '$HOST')"
for name in twin twin-2; do
  call -o /dev/null -X DELETE "$API/orgs/$ORG/servers/$name?snapshot=false"
done
for _ in $(seq 1 30); do
  [ "$(call "$API/orgs/$ORG/servers" | field 'len(d)')" = 1 ] && break
  sleep 2
done
# Removed again, so that what follows counts the snapshots it expects.
copy=$(call "$SRV/snapshots" | field "[s['id'] for s in d['snapshots'] if s['trigger'] == 'copy'][0]")
# Several snapshots go in one request: all of them, or none if one is not there.
expect "deleting several stops at one that is unknown" "$(call -o /dev/null -w '%{http_code}' -X DELETE -d "{\"ids\":[\"$copy\",\"0000000000000000\"]}" "$API/orgs/$ORG/snapshots")" 404
expect "the snapshot it was made from is an ordinary one" "$(call -X DELETE -d "{\"ids\":[\"$copy\"]}" "$API/orgs/$ORG/snapshots" | field "d['deleted']")" 1

step "snapshots outlive the server"
# Something that is in no snapshot yet. Deleting the server must not lose it.
echo "written just before the end" | call -X PUT --data-binary @- "$SRV/files/content?path=/last.txt" >/dev/null
expect "deleting a server takes a last snapshot of it first" "$(call -o /dev/null -w '%{http_code}' -X DELETE "$SRV")" 204
for _ in $(seq 1 30); do
  [ "$(call "$API/orgs/$ORG/servers" | field 'len(d)')" = 0 ] && break
  sleep 2
done
history=$(call "$API/orgs/$ORG/snapshots")
expect "a deleted server's snapshots are still listed" "$(echo "$history" | field "sorted({s['server'] for s in d['snapshots']}), len(d['snapshots']), d['usage']['bytes'] > 0")" "(['probe'], 5, True)"
expect "an organization with snapshots is not deleted by accident" "$(call -o /dev/null -w '%{http_code}' -X DELETE "$API/orgs/$ORG")" 409
expect "a snapshot can be removed on purpose" "$(call -o /dev/null -w '%{http_code}' -X DELETE "$API/orgs/$ORG/snapshots/$SNAP")" 204
expect "and is then gone" "$(call "$API/orgs/$ORG/snapshots" | field "len(d['snapshots'])")" 4
FINAL=$(call "$API/orgs/$ORG/snapshots" | field "[s['id'] for s in d['snapshots'] if s['trigger'] == 'final'][0]") || fail "there is no final snapshot of the deleted server"

step "a deleted server comes back"
expect "a new server cannot take the name its snapshots are under" "$(call -d '{"name":"probe","spec":{"eula":true,"loader":"paper","version":"1.21.4"}}' "$API/orgs/$ORG/servers" | field "d['code']")" name_has_history
back=$(call -d "{\"name\":\"probe\",\"fromSnapshot\":\"$FINAL\",\"spec\":{\"state\":\"Stopped\",\"expose\":{\"hostname\":\"$HOST\"}}}" "$API/orgs/$ORG/servers?wait=true")
case "$back" in *'"spec"'*) ;; *) fail "the deleted server was not brought back: $back" ;; esac
expect "under its own name, left stopped as asked" "$(echo "$back" | field "(d['name'], d['spec']['state'], d['spec']['expose']['hostname'])")" "('probe', 'Stopped', '$HOST')"
expect "with its files" "$(call "$SRV/files/content?path=/e2e.txt")" "written by e2e"
expect "down to what was written just before it was deleted" "$(call "$SRV/files/content?path=/last.txt")" "written just before the end"
expect "and its history" "$(call "$SRV/snapshots" | field "len(d['snapshots'])")" 4
# And again with no pause, while what is left of the one just deleted is still
# being cleared away. Its files must not land in a volume that is on its way out.
call -X DELETE "$SRV" >/dev/null
again=$(call -d "{\"name\":\"probe\",\"fromSnapshot\":\"$FINAL\",\"spec\":{\"expose\":{\"hostname\":\"$HOST\"}}}" "$API/orgs/$ORG/servers?wait=true")
case "$again" in *'"spec"'*) ;; *) fail "the server was not brought back straight after being deleted: $again" ;; esac
wait_phase Running
expect "brought back straight after a delete, it keeps its files once it runs" "$(call "$SRV/files/content?path=/e2e.txt")" "written by e2e"
expect "and its plugins" "$(call -d '{"command":"plugins"}' "$SRV/console" | field "d['output']")" WorldEdit
