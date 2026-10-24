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
# set_spec BODY [SERVER]: changes a server's settings, failing with the panel's
# answer if it refused. Its answer is not otherwise needed, and a change
# that was refused would show only later, as something else.
set_spec() {
  out=$(call -X PATCH -d "$1" "${2:-$SRV}")
  case "$out" in *'"spec"'*) ;; *) fail "a change of settings was refused: $out" ;; esac
}

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
FETCH_FAILED='\[init\].*\[ERROR\].*[Dd]ownload|Failed to download mojang_[^ ]*\.jar'
# Paper's own launcher says it its own way, as it fetches the game's jar from
# Mojang.
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

# game_clean WHAT [SERVER]: the game's own log of its last run says nothing
# went wrong: no error, no exception, no setting it could not read, no
# command that failed. What the panel writes for the game, and how it stops
# it, shows up there and nowhere else. A run that stopped is still in it.
game_clean() {
  log=$(call -f "${2:-$SRV}/files/content?path=/logs/latest.log") || fail "$1: the game's log could not be read"
  [ -n "$log" ] || fail "$1: the game's log is empty"
  logged_clean "$1" "$log"
  echo "ok: $1"
}

# logged_clean WHAT LOG: fails if a log has something gone wrong in it.
logged_clean() {
  bad=$(printf '%s\n' "$2" | grep -E '[ /](ERROR|FATAL)\]|Exception|Failed to parse|An unexpected error occurred' | head -10)
  [ -z "$bad" ] || fail "$1: the game logged:
$bad"
}

# earlier_clean WHAT [SERVER]: the run before the current one, which the game
# packed away as it started again, is clean too.
earlier_clean() {
  name=$(call -f "${2:-$SRV}/logs" | field "[l['name'] for l in d['logs'] if l['kind'] == 'log'][0]") || fail "$1: no earlier run is listed"
  log=$(call -f "${2:-$SRV}/logs/file?name=$name" | field "'\\n'.join(l['text'] for l in d['lines'])") || fail "$1: $name could not be read"
  logged_clean "$1" "$log"
  echo "ok: $1"
}

# stopped_clean WHAT [SERVER]: the game's last run ended as a game that was
# told to stop does, saying so, rather than being cut off.
stopped_clean() {
  game_clean "$1" "${2:-}"
  case "$(call -f "${2:-$SRV}/files/content?path=/logs/latest.log")" in
    *"Stopping server"*) ;;
    *) fail "$1: the game's log does not end with it stopping" ;;
  esac
}

# act ACTION [SERVER]: asks a server to start, stop or restart, failing if
# that is refused.
act() {
  out=$(call -X POST "${2:-$SRV}/$1")
  case "$out" in *'"spec"'*) ;; *) fail "asking it to $1 was refused: $out" ;; esac
}

# phase_now [SERVER]: what the server is doing, as the panel says.
phase_now() { call "${1:-$SRV}" | field "d['status'].get('phase')"; }

# settled [SERVER]: waits until the status is of the server's settings as
# they are now. Straight after a change it may still say what the server was
# doing before, which is not what the change did.
settled() {
  for _ in $(seq 1 60); do
    [ "$(call "${1:-$SRV}" | field "d['status'].get('observedGeneration', 0) >= d['generation']")" = True ] && return 0
    sleep 1
  done
  fail "the status never caught up with the settings"
}

# wait_until PHASE [SERVER]: as wait_phase, for a phase the server passes
# through on the way, which it is caught in or has gone by.
wait_until() {
  for _ in $(seq 1 120); do
    [ "$(phase_now "${2:-$SRV}")" = "$1" ] && return 0
    sleep 1
  done
  fail "it was never $1 (it is $(phase_now "${2:-$SRV}"))"
}

# stays PHASE SECONDS [SERVER]: nothing changes it from PHASE for a while.
stays() {
  for _ in $(seq 1 "$2"); do
    now=$(phase_now "${3:-$SRV}")
    [ "$now" = "$1" ] || fail "it did not stay $1: it went $now"
    sleep 1
  done
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
game_clean "and its log is clean"
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

step "members picked for the whitelist"
# The player this user says they are: the one linked already, or one linked
# for the run and taken off after.
ME=$(call "$API/me" | field "d['user']['id']")
PLAYER=$(call "$API/me" | field "(d.get('minecraft') or {}).get('name', '')")
linked_here=""
if [ -z "$PLAYER" ]; then
  PLAYER=$(call -X PUT -d '{"name":"Notch"}' "$API/me/minecraft" | field "d['name']")
  linked_here=1
fi
whitelisted() { call -d '{"command":"whitelist list"}' "$SRV/console" | field "d['output']"; }
set_spec '{"spec":{"autoWhitelist":"off","whitelistMembers":{"also":["'"$ME"'"]}}}'
for _ in $(seq 1 20); do whitelisted | grep -q "$PLAYER" && break; sleep 1; done
expect "a member picked is put on the whitelist, though no role is" "$(whitelisted)" "$PLAYER"
set_spec '{"spec":{"autoWhitelist":"viewer","whitelistMembers":{"also":[],"except":["'"$ME"'"]}}}'
for _ in $(seq 1 20); do whitelisted | grep -q "$PLAYER" || break; sleep 1; done
case "$(whitelisted)" in *"$PLAYER"*) fail "a member left out stays off the whitelist, though every member is on it" ;; *) echo "ok: a member left out stays off the whitelist, though every member is on it" ;; esac
# Every account a member plays as is on it, their alts too, and an alt
# unlinked is taken off.
set_spec '{"spec":{"autoWhitelist":"viewer","whitelistMembers":null}}'
ALT=$(call -d '{"name":"jeb_"}' "$API/me/minecraft-accounts" | field "[a['uuid'] for a in d if a['name'].lower() == 'jeb_'][0]") || fail "an alt could not be linked"
for _ in $(seq 1 20); do whitelisted | grep -qi "jeb_" && break; sleep 1; done
expect "a member's alt is on the whitelist with them" "$(whitelisted)" "jeb_"
expect "and so is their main one" "$(whitelisted)" "$PLAYER"
call -X DELETE "$API/me/minecraft-accounts/$ALT" >/dev/null
for _ in $(seq 1 20); do whitelisted | grep -qi "jeb_" || break; sleep 1; done
case "$(whitelisted)" in *jeb_*) fail "an alt unlinked stays on the whitelist" ;; *) echo "ok: an alt unlinked is taken off" ;; esac
set_spec '{"spec":{"autoWhitelist":"off","whitelistMembers":null}}'
# One typed in its settings is put on at once, and taken off when it is
# taken out: the image only ever adds to the list.
set_spec '{"spec":{"whitelist":["Dinnerbone"]}}'
for _ in $(seq 1 20); do whitelisted | grep -qi "Dinnerbone" && break; sleep 1; done
expect "a player typed in the settings is put on the whitelist" "$(whitelisted)" "Dinnerbone"
set_spec '{"spec":{"whitelist":null}}'
for _ in $(seq 1 20); do whitelisted | grep -qi "Dinnerbone" || break; sleep 1; done
case "$(whitelisted)" in *Dinnerbone*) fail "a player taken out of the settings stays on the whitelist" ;; *) echo "ok: and taken off when taken out" ;; esac
if [ -n "$linked_here" ]; then call -X DELETE "$API/me/minecraft" >/dev/null; fi

step "files"
echo "written by e2e" | call -X PUT --data-binary @- "$SRV/files/content?path=/e2e.txt" >/dev/null
expect "file round trip" "$(call "$SRV/files/content?path=/e2e.txt")" "written by e2e"
expect "managed server.properties" "$(call "$SRV/files/content?path=/server.properties")" "motd=e2e probe"
# A save from the editor is written only over the file it opened. A file
# of its own: e2e.txt is looked for as it is later.
echo "opened in the editor" | call -X PUT --data-binary @- "$SRV/files/content?path=/e2e-editor.txt" >/dev/null
tag=$(call -D - -o /dev/null "$SRV/files/content?path=/e2e-editor.txt" | tr -d '\r' | sed -n 's/^[Ee][Tt][Aa][Gg]: //p')
[ -n "$tag" ] || fail "a file that can be edited has no tag"
saved() { echo "$1" | call -o /dev/null -w '%{http_code}' -X PUT -H "If-Match: $tag" --data-binary @- "$SRV/files/content?path=/e2e-editor.txt"; }
expect "a save over the file it opened" "$(saved "saved from the editor")" 204
expect "a save over a file changed since it was opened" "$(saved "saved over a change")" 412
expect "the change is kept" "$(call "$SRV/files/content?path=/e2e-editor.txt")" "saved from the editor"
tag=$(call -D - -o /dev/null "$SRV/files/content?path=/e2e-editor.txt" | tr -d '\r' | sed -n 's/^[Ee][Tt][Aa][Gg]: //p')
big=$(head -c 2200000 /dev/zero | tr '\0' x | call -o /dev/null -w '%{http_code}' -X PUT -H "If-Match: $tag" --data-binary @- "$SRV/files/content?path=/e2e-editor.txt")
expect "a save from the editor over 2 MB is refused" "$big" 413
expect "and the file is as it was" "$(call "$SRV/files/content?path=/e2e-editor.txt")" "saved from the editor"

step "archives"
# A zip with all in one folder, as a world is, and one with what a Mac adds:
# opened where it is, into that folder.
zipped() { python3 -c '
import io, sys, zipfile
b = io.BytesIO()
with zipfile.ZipFile(b, "w") as z:
    for name in sys.argv[1:]:
        z.writestr(name, "x" if not name.endswith("/") else "")
sys.stdout.buffer.write(b.getvalue())' "$@"; }
zipped e2e-world/ e2e-world/level.dat e2e-world/region/r.0.0.mca __MACOSX/e2e-world/._level.dat | call -X PUT --data-binary @- "$SRV/files/content?path=/e2e-world.zip" >/dev/null
expect "an archive in one folder is opened into it" "$(call -X POST "$SRV/files/extract?path=/e2e-world.zip")" '"/e2e-world"'
expect "what was in it is there" "$(call "$SRV/files?path=/e2e-world/region")" '"r.0.0.mca"'
case "$(call "$SRV/files?path=/")" in *__MACOSX*) fail "what a Mac adds was opened too" ;; esac
expect "opened again, it is not written over" "$(call -o /dev/null -w '%{http_code}' -X POST "$SRV/files/extract?path=/e2e-world.zip")" 409
zipped a.yml b.yml | call -X PUT --data-binary @- "$SRV/files/content?path=/e2e-configs.zip" >/dev/null
expect "loose files are opened into a folder named after it" "$(call -X POST "$SRV/files/extract?path=/e2e-configs.zip")" '"/e2e-configs"'
expect "into it" "$(call "$SRV/files?path=/e2e-configs")" '"b.yml"'
# A folder, downloaded as a zip.
call "$SRV/files/archive?path=/e2e-world" > "$WORK/folder.zip"
python3 -c 'import sys, zipfile; n = zipfile.ZipFile(sys.argv[1]).namelist(); assert "e2e-world/region/r.0.0.mca" in n, n' "$WORK/folder.zip" || fail "a folder downloaded is not a zip of it"
echo "ok: a folder downloads as a zip of it"
# One whose name bsdtar would read otherwise: as another archive, "@name".
call -X POST "$SRV/files/mkdir?path=/@e2e-at" >/dev/null
echo at | call -X PUT --data-binary @- "$SRV/files/content?path=/@e2e-at/x.txt" >/dev/null
call "$SRV/files/archive?path=/@e2e-at" > "$WORK/at.zip"
python3 -c 'import sys, zipfile; n = zipfile.ZipFile(sys.argv[1]).namelist(); assert "@e2e-at/x.txt" in n, n' "$WORK/at.zip" || fail "a folder named @… is not zipped as itself"
echo "ok: a folder named @… is zipped as itself"
call -X DELETE "$SRV/files?path=/@e2e-at" >/dev/null
# Moved into another folder, and not into itself.
expect "a folder is moved into another" "$(call -o /dev/null -w '%{http_code}' -X POST "$SRV/files/rename?path=/e2e-configs&to=/e2e-world/e2e-configs")" 204
expect "with what is in it" "$(call "$SRV/files?path=/e2e-world/e2e-configs")" '"a.yml"'
expect "a folder is not moved into itself" "$(call -X POST "$SRV/files/rename?path=/e2e-world&to=/e2e-world/e2e-configs/e2e-world")" "into itself"
expect "nor into a folder that is not there" "$(call -X POST "$SRV/files/rename?path=/e2e-world.zip&to=/no-such/e2e-world.zip")" "There is no folder"
for p in /e2e-world /e2e-world.zip /e2e-configs.zip; do call -X DELETE "$SRV/files?path=$p" >/dev/null; done

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
set_spec "$with"
restarted
expect "a datapack from a URL is installed" "$(call -o /dev/null -w '%{http_code}' "$SRV/files/content?path=/world/datapacks/Veinminer-1.2.4.zip")" 200
expect "and enabled" "$(call -d '{"command":"datapack list enabled"}' "$SRV/console" | field "d['output']")" "Veinminer-1.2.4.zip"
set_spec "{\"spec\":{\"mods\":$mods}}"
expect "taken off, its file is removed" "$(call -o /dev/null -w '%{http_code}' "$SRV/files/content?path=/world/datapacks/Veinminer-1.2.4.zip")" 404
restarted
expect "and the game no longer has it" "$(call -d '{"command":"datapack list"}' "$SRV/console" | field "'Veinminer' not in d['output']")" True

step "resource pack"
# A pack uploaded to the panel is kept in the server's files and served to
# players' games, which fetch it with no session, at its hash.
python3 - "$WORK/pack.zip" "$WORK/nested.zip" <<'PY'
import sys, zipfile
with zipfile.ZipFile(sys.argv[1], "w") as z:
    z.writestr("pack.mcmeta", '{"pack":{"pack_format":46,"description":"e2e"}}')
    z.writestr("assets/minecraft/lang/en_us.json", '{"menu.singleplayer":"e2e"}')
with zipfile.ZipFile(sys.argv[2], "w") as z:
    z.writestr("E2E Pack/pack.mcmeta", '{"pack":{"pack_format":46,"description":"e2e"}}')
PY
packed=$(call -X PUT --data-binary @"$WORK/pack.zip" "$SRV/resource-pack?name=e2e-pack.zip")
PACK_URL=$(echo "$packed" | field "d['spec']['resourcePack']['url']") || fail "no pack was taken: $packed"
PACK_SHA=$(python3 -c 'import hashlib,sys; print(hashlib.sha1(open(sys.argv[1],"rb").read()).hexdigest())' "$WORK/pack.zip")
expect "an uploaded pack is the server's, at its hash" "$(echo "$packed" | field "(d['spec']['resourcePack']['sha1'], d['spec']['resourcePack']['uploaded']['name'])")" "('$PACK_SHA', 'e2e-pack.zip')"
expect "and is made for the formats its pack.mcmeta says" "$(echo "$packed" | field "d['spec']['resourcePack'].get('formats')")" "{'min': 46, 'max': 46}"
expect "and served to anyone, as a game fetches it" "$(curl -sS "$PACK_URL" | python3 -c 'import hashlib,sys; print(hashlib.sha1(sys.stdin.buffer.read()).hexdigest())')" "$PACK_SHA"
expect "it is kept in the server's files" "$(call "$SRV/files/content?path=/.avsys/resource-pack-$PACK_SHA.zip" | python3 -c 'import hashlib,sys; print(hashlib.sha1(sys.stdin.buffer.read()).hexdigest())')" "$PACK_SHA"
expect "a pack a folder down is refused, saying so" "$(call -X PUT --data-binary @"$WORK/nested.zip" "$SRV/resource-pack?name=nested.zip" | field "d['message']")" "inside the folder E2E Pack"
restarted
expect "the game offers it to players" "$(call "$SRV/files/content?path=/server.properties" | python3 -c 'import sys; p = dict(l.split("=", 1) for l in sys.stdin.read().replace("\\:", ":").splitlines() if "=" in l and not l.startswith("#")); print(p.get("resource-pack"), p.get("resource-pack-sha1"))')" "$PACK_URL $PACK_SHA"
game_clean "and reads all that was written for the pack, its prompt too"
expect "and it is taken away again" "$(call -X DELETE "$SRV/resource-pack" | field "d['spec'].get('resourcePack')")" None
expect "with its file" "$(call -o /dev/null -w '%{http_code}' "$SRV/files/content?path=/.avsys/resource-pack-$PACK_SHA.zip")" 404

step "resource packs joined into one"
# Several packs, in order, the top one first: the panel joins them into one,
# which it serves, and joins them again when the order changes.
python3 - "$WORK/pack2.zip" <<'PY'
import sys, zipfile
with zipfile.ZipFile(sys.argv[1], "w") as z:
    z.writestr("pack.mcmeta", '{"pack":{"pack_format":46,"description":"e2e two"}}')
    z.writestr("assets/minecraft/lang/en_us.json", '{"menu.singleplayer":"two","menu.multiplayer":"two"}')
PY
PACK2_SHA=$(python3 -c 'import hashlib,sys; print(hashlib.sha1(open(sys.argv[1],"rb").read()).hexdigest())' "$WORK/pack2.zip")
call -X PUT --data-binary @"$WORK/pack.zip" "$SRV/resource-pack?name=e2e-pack.zip" >/dev/null
stack=$(call -X POST --data-binary @"$WORK/pack2.zip" "$SRV/resource-packs?name=two.zip")
expect "a second pack goes on top of the one it had" "$(echo "$stack" | field "[p['sha1'] for p in d['spec']['resourcePack']['stack']]")" "['$PACK2_SHA', '$PACK_SHA']"
JOINED_URL=$(echo "$stack" | field "d['spec']['resourcePack']['url']")
JOINED_SHA=$(echo "$stack" | field "d['spec']['resourcePack']['sha1']")
expect "the pack joined is served at its hash" "$(curl -sS "$JOINED_URL" | python3 -c 'import hashlib,sys; print(hashlib.sha1(sys.stdin.buffer.read()).hexdigest())')" "$JOINED_SHA"
joined_lang() { curl -sS "$1" | python3 -c 'import io,json,sys,zipfile; d=json.load(zipfile.ZipFile(io.BytesIO(sys.stdin.buffer.read())).open("assets/minecraft/lang/en_us.json")); print(d["menu.singleplayer"], d["menu.multiplayer"])'; }
expect "with what both say, the top one's where both say it" "$(joined_lang "$JOINED_URL")" "two two"
expect "and every pack's file is kept in the server's files" "$(call -o /dev/null -w '%{http_code}' "$SRV/files/content?path=/.avsys/resource-pack-$PACK_SHA.zip") $(call -o /dev/null -w '%{http_code}' "$SRV/files/content?path=/.avsys/resource-pack-$JOINED_SHA.zip")" "200 200"
turned=$(call -X PUT -d '{"order":["'"$PACK_SHA"'","'"$PACK2_SHA"'"]}' "$SRV/resource-packs")
TURNED_URL=$(echo "$turned" | field "d['spec']['resourcePack']['url']")
expect "put the other way up, it is joined again" "$(joined_lang "$TURNED_URL")" "e2e two"
expect "and the pack joined before is gone from its files" "$(call -o /dev/null -w '%{http_code}' "$SRV/files/content?path=/.avsys/resource-pack-$JOINED_SHA.zip")" 404
expect "with none left, it has none" "$(call -X PUT -d '{"order":[]}' "$SRV/resource-packs" | field "d['spec'].get('resourcePack')")" None
expect "and none of their files" "$(call -o /dev/null -w '%{http_code}' "$SRV/files/content?path=/.avsys/resource-pack-$PACK2_SHA.zip")" 404
# One picked from Modrinth says which project and version it is, and stops
# saying so once it is given another address.
picked=$(call -X PATCH -d '{"spec":{"resourcePack":{"url":"https://cdn.modrinth.com/data/e2e/pack.zip","sha1":"'"$PACK_SHA"'","project":{"source":"modrinth","id":"e2e-pack","version":"v1","versionName":"1.0","title":"E2E Pack"}}}}' "$SRV")
expect "a pack picked from Modrinth says which" "$(echo "$picked" | field "(d['spec']['resourcePack']['project']['id'], d['spec']['resourcePack']['project']['versionName'])")" "('e2e-pack', '1.0')"
expect "given another address, it is no longer the project's" "$(call -X PATCH -d '{"spec":{"resourcePack":{"url":"https://example.org/pack.zip"}}}' "$SRV" | field "d['spec']['resourcePack'].get('project')")" None
call -X DELETE "$SRV/resource-pack" >/dev/null

step "a stop while the game loads"
# Restarted, and stopped while it is coming up again. Told to stop while it
# loads, the game would fail to, and be killed a minute later: it is let
# finish loading and then stopped, saying so meanwhile, and nothing starts
# it after.
act restart
for _ in $(seq 1 60); do
  [ "$(call "$SRV" | field "d['status'].get('phase')")" = Starting ] && break
  sleep 1
done
# Into its loading: the container started again, which takes a moment.
sleep 4
asked=$(date +%s)
expect "a stop is taken while it starts" "$(call -X POST "$SRV/stop" | field "d['spec']['state']")" Stopped
# While it waits for the game to load it says so. Once loaded, the game
# saves and stops as it always does, in a few seconds: one told to stop
# while it loaded would fail to, and be killed a minute later.
said=""
loaded=$asked
for _ in $(seq 1 300); do
  now=$(call "$SRV" | field "(d['status'].get('phase'), d['status'].get('message') or '')")
  case "$now" in
    *Stopped*) break ;;
    *"still loading"*) said=1; loaded=$(date +%s) ;;
  esac
  sleep 1
done
wait_phase Stopped
took=$(($(date +%s) - loaded))
[ "$took" -lt 40 ] || fail "once loaded, the game took ${took}s to stop, as long as one that is killed"
echo "ok: and it is stopped once loaded, ${took}s after"
# Caught loading, as it is a few seconds into a start, it said so.
[ -n "$said" ] || [ $(($(date +%s) - asked)) -lt 8 ] || fail "a stop that waited never said why"
echo "ok: and says what it waits for"
sleep 15
expect "and it stays stopped" "$(call "$SRV" | field "(d['spec']['state'], d['status'].get('phase'))")" "('Stopped', 'Stopped')"
stopped_clean "and the game it stopped saved and stopped cleanly"
act start
wait_phase Running
echo "ok: and starts again when asked"

step "actions one after another"
# What people do: a button pressed again before the last one took, the
# opposite asked for at once, settings changed while it starts. Each ends
# as the last thing asked, stays so, and the game is never killed or left
# with something it could not read.
act restart
wait_until Starting
act restart
wait_phase Running
stays Running 10
expect "restarted again while it starts, it ends running, and answers" "$(call -d '{"command":"list"}' "$SRV/console" | field "d['output']")" "players online"
game_clean "with nothing in its log"
earlier_clean "nor in the log of the run the second restart cut short"

act stop
act start
settled
wait_phase Running
stays Running 10
echo "ok: stopped and started again at once, it ends running"

act stop
wait_phase Stopped
act start
act stop
settled
wait_phase Stopped
stays Stopped 15
echo "ok: started and stopped again at once, it ends stopped, and stays so"
stopped_clean "and the game it stopped saved and stopped cleanly"

act start
wait_until Starting
set_spec '{"spec":{"properties":{"motd":"changed while starting"}}}'
settled
wait_phase Running
# Changed before its game began, it starts with them; after, they are for
# its next start, and say so.
if [ "$(call "$SRV" | field "d['status'].get('pendingRestart', False)")" = True ]; then
  echo "ok: settings changed once its game had begun are for its next start"
  act restart
  for _ in $(seq 1 90); do [ "$(phase_now)" != Running ] && break; sleep 1; done
  wait_phase Running
fi
expect "settings changed while it starts are in place once it runs" "$(call "$SRV/files/content?path=/server.properties")" "motd=changed while starting"
set_spec '{"spec":{"properties":{"motd":"e2e probe"}}}'
act restart
for _ in $(seq 1 90); do [ "$(phase_now)" != Running ] && break; sleep 1; done
wait_phase Running
game_clean "and the game read them"

step "snapshots"
snap=$(call -X POST "$SRV/snapshots?wait=true")
SNAP=$(echo "$snap" | field "d['id']") || fail "no snapshot was taken: $snap"
expect "a running server is snapshotted" "$(echo "$snap" | field "(d['server'], d['trigger'], d['version'], d['loader'], d['files'] > 10)")" "('probe', 'manual', '1.21.4', 'paper', True)"
expect "the game goes back to saving" "$(call -d '{"command":"save-on"}' "$SRV/console" | field "d['output']")" "already turned on"
# Change a file, add one, and take away a plugin, then go back.
echo "changed after the snapshot" | call -X PUT --data-binary @- "$SRV/files/content?path=/e2e.txt" >/dev/null
echo "not in the snapshot" | call -X PUT --data-binary @- "$SRV/files/content?path=/stray.txt" >/dev/null
set_spec '{"spec":{"mods":[{"source":"modrinth","kind":"plugin","project":"luckperms"}]}}'
# A shell open to its files while they are put back could write among the
# restored ones: the restore ends it, however it was opened. On a server
# that is stopped already, which stopping it again does not do.
call -X POST "$SRV/stop" >/dev/null
wait_phase Stopped
ssh-keygen -q -t ed25519 -N '' -f "$WORK/restore-key" -C avsys-e2e-restore
RESTORE_KEY=$(call -d "{\"name\":\"e2e-restore\",\"publicKey\":\"$(cat "$WORK/restore-key.pub")\"}" "$API/me/ssh-keys" | field "d['id']")
ssh -n -i "$WORK/restore-key" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
  -p "$SSH_PORT" "probe.$ORG@$(echo "$AVSYS_URL" | sed -E 's#^[a-z]+://##; s#[:/].*$##')" 'sleep 900' >/dev/null 2>&1 &
held=$!
sleep 5
kill -0 "$held" 2>/dev/null || fail "a shell could not be opened before the restore"
restored=$(call -X POST "$SRV/snapshots/$SNAP/restore?wait=true")
expect "a restore is accepted" "$(echo "$restored" | field "d['id'] == '$SNAP'")" True
for _ in $(seq 1 20); do kill -0 "$held" 2>/dev/null || break; sleep 1; done
if kill -0 "$held" 2>/dev/null; then
  kill "$held"
  fail "a shell open before the restore was still open after it"
fi
echo "ok: a shell open to its files is ended by the restore"
call -X DELETE "$API/me/ssh-keys/$RESTORE_KEY" >/dev/null
expect "a server stopped before the restore stays stopped" "$(call "$SRV" | field "d['spec']['state']")" Stopped
call -X POST "$SRV/start" >/dev/null
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
told=$(ssh -n -i "$WORK/key" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
  -p "$SSH_PORT" "probe.$ORG@$ssh_host" 'sudo true' 2>&1) || true
expect "and point to tools" "$told" "tools install <name>"
# A program installed into one's home without root, found by a login shell,
# and taken out again with what was downloaded for it.
installed=$(ssh -n -i "$WORK/key" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
  -p "$SSH_PORT" "probe.$ORG@$ssh_host" 'tools install ripgrep >/tmp/tools.log 2>&1; bash -lc "rg --version" | head -1; tools remove ripgrep >/dev/null 2>&1; bash -lc "command -v rg || echo gone"; du -s ~/.local/mamba 2>/dev/null | cut -f1; tail -3 /tmp/tools.log' 2>&1) || true
case "$installed" in
  *ripgrep*gone*) echo "ok: tools installs a program into one's home, and takes it out again" ;;
  *[Dd]ownload*|*resolve*|*[Cc]onnection*) echo "note: conda-forge could not be reached, so tools was not tried: $installed" ;;
  *) fail "tools did not install and remove a program: $installed" ;;
esac
# What a command says on stderr reaches whoever ran it, though nothing came
# on stdout before: the gateway once ended the output first, losing it.
for _ in 1 2 3 4 5; do
  said=$(ssh -n -i "$WORK/key" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
    -p "$SSH_PORT" "probe.$ORG@$ssh_host" 'ls /no-such-dir' 2>&1) || true
  case "$said" in *"No such file or directory"*) ;; *) fail "a command's error did not come through the gateway: '$said'" ;; esac
done
echo "ok: a command's errors come through, every time"

step "import"
# An existing server's folder, packed on another host, is unpacked into a new
# server made stopped: kept as it was but for what belonged to the old host.
python3 - "$WORK" <<'PY'
import gzip, io, sys, tarfile
def add(t, name, data=b"x"):
    i = tarfile.TarInfo(name); i.size = len(data); t.addfile(i, io.BytesIO(data))
with tarfile.open(sys.argv[1] + "/import.tar.gz", "w:gz") as t:
    add(t, "Old/server.properties", b"motd=From elsewhere\nlevel-name=Drehmal\nserver-port=25599\nserver-ip=10.0.0.5\nenable-rcon=true\n")
    add(t, "Old/Drehmal/level.dat"); add(t, "Old/Drehmal/session.lock"); add(t, "Old/Drehmal_nether/level.dat")
    add(t, "Old/plugins/Foo.jar"); add(t, "Old/logs/2026-01-02-1.log.gz", gzip.compress(b"[12:00:00] [Server thread/INFO]: Done (1.0s)!\n")); add(t, "Old/crash-reports/crash-2026-01-02_12.00.00-server.txt", b"---- Minecraft Crash Report ----\n"); add(t, "Old/start.sh")
    add(t, "Old/ops.json", b'[{"uuid":"853c80ef-3c37-49fd-aa49-938b674adae6","name":"jeb_","level":2,"bypassesPlayerLimit":false}]')
    add(t, "Old/whitelist.json", b'[{"uuid":"069a79f4-44e9-4726-a5be-fca90e38aaf5","name":"Notch"}]')
with tarfile.open(sys.argv[1] + "/escape.tar", "w") as t:
    add(t, "Old/server.properties"); add(t, "../../escaped.txt")
with tarfile.open(sys.argv[1] + "/save.tar.gz", "w:gz") as t:
    add(t, "saves/My World/level.dat"); add(t, "saves/My World/session.lock")
    add(t, "saves/My World/region/r.0.0.mca"); add(t, "saves/My World/DIM-1/region/r.0.0.mca")
with tarfile.open(sys.argv[1] + "/other.tar.gz", "w:gz") as t:
    add(t, "Other/level.dat"); add(t, "Other/region/r.1.1.mca")
PY
IMPORTED=$API/orgs/$ORG/servers/imported
call -d '{"name":"imported","spec":{"eula":true,"loader":"paper","version":"1.21.4","state":"Stopped","ops":["jeb_"],"resources":{"memory":"1Gi"},"storage":{"size":"1Gi"}}}' "$API/orgs/$ORG/servers" >/dev/null
refused=$(call -X PUT --data-binary "@$WORK/escape.tar" "$IMPORTED/import?root=Old&wait=true")
expect "an archive with a path out of its folder is refused" "$(echo "$refused" | field "d['message']")" "Could not unpack the archive"
expect "and nothing of it is kept" "$(call -o /dev/null -w '%{http_code}' "$IMPORTED/files/content?path=/server.properties")" 404
expect "nor is the game started on what an import left" "$(call -X POST "$IMPORTED/start" | field "d['code']")" import_incomplete
imported=$(call -X PUT --data-binary "@$WORK/import.tar.gz" "$IMPORTED/import?root=Old&players=listed&wait=true")
expect "a server's folder is imported" "$(echo "$imported" | field "d['name']")" imported
listing=$(call "$IMPORTED/files?path=/" | field "sorted(f['name'] for f in d['entries'])")
expect "its world is the one the game opens" "$listing" "'world', 'world_nether'"
expect "its plugins come with it" "$(call -o /dev/null -w '%{http_code}' "$IMPORTED/files/content?path=/plugins/Foo.jar")" 200
oldlogs=$(call "$IMPORTED/logs" | field "sorted((l['name'], l['kind']) for l in d['logs'])")
expect "its earlier logs and crash reports are read in the console" "$oldlogs" "[('crash-reports/crash-2026-01-02_12.00.00-server.txt', 'crash'), ('logs/2026-01-02-1.log.gz', 'log')]"
expect "and read unpacked" "$(call "$IMPORTED/logs/file?name=logs/2026-01-02-1.log.gz" | field "d['lines'][0]['text']")" "Done (1.0s)!"
case "$listing" in *start.sh*|*Drehmal*|*.avsys/import*) fail "what belonged to the old host was kept: $listing" ;; esac
# Its operators and whitelist were read into its settings, and changed
# there: one kept keeps their level, one taken out is gone from the files.
expect "an operator kept keeps their level" "$(call "$IMPORTED/files/content?path=/ops.json" | tr -d ' \n')" '"level":2'
case "$(call "$IMPORTED/files/content?path=/whitelist.json")" in *Notch*) fail "a player taken out of the whitelist was kept in its file" ;; *) echo "ok: a player taken out is gone from its file" ;; esac
expect "a world left open there is not locked here" "$(call -o /dev/null -w '%{http_code}' "$IMPORTED/files/content?path=/world/session.lock")" 404
props=$(call "$IMPORTED/files/content?path=/server.properties")
expect "its settings are kept" "$props" "motd=From elsewhere"
case "$props" in *server-port*|*server-ip*|*enable-rcon*|*level-name*) fail "the old host's settings were kept: $props" ;; esac
expect "and it is let start" "$(call "$IMPORTED" | field "d['status'].get('message', '')")" ""
expect "a server with files is not imported into" "$(call -X PUT --data-binary "@$WORK/import.tar.gz" "$IMPORTED/import?root=Old" | field "d['code']")" has_files
call -X DELETE "$IMPORTED?snapshot=false" >/dev/null
# A world of one's own, a single-player save here, becomes a new server's:
# with its Nether beside it, where Paper keeps one.
OWN=$API/orgs/$ORG/servers/own-world
call -d '{"name":"own-world","spec":{"eula":true,"loader":"paper","version":"1.21.4","state":"Stopped","resources":{"memory":"1Gi"},"storage":{"size":"1Gi"}}}' "$API/orgs/$ORG/servers" >/dev/null
call -X PUT --data-binary "@$WORK/save.tar.gz" "$OWN/import?root=saves%2FMy%20World&world=true&wait=true" >/dev/null
expect "a world of one's own is the server's" "$(call -o /dev/null -w '%{http_code}' "$OWN/files/content?path=/world/level.dat")" 200
expect "with its Nether where Paper keeps it" "$(call -o /dev/null -w '%{http_code}' "$OWN/files/content?path=/world_nether/DIM-1/region/r.0.0.mca")" 200
expect "and not locked" "$(call -o /dev/null -w '%{http_code}' "$OWN/files/content?path=/world/session.lock")" 404
# Its world replaced by another: a snapshot is taken first, only the world's
# folders are replaced, and the snapshot puts the old one back.
echo "kept" | call -X PUT --data-binary @- "$OWN/files/content?path=/kept.txt" >/dev/null
expect "only a world replaces a world" "$(call -X PUT --data-binary "@$WORK/other.tar.gz" "$OWN/import?root=Other&replace=true" | field "d['code']")" invalid
# Answered once the archive is in; the panel sees the rest through.
expect "a world sent to replace another is answered once it is in" "$(call -o /dev/null -w '%{http_code}' -X PUT --data-binary "@$WORK/other.tar.gz" "$OWN/import?root=Other&world=true&replace=true")" 202
for _ in $(seq 1 120); do [ "$(call "$OWN/operation")" = null ] && break; sleep 1; done
expect "and then replaced" "$(call "$OWN/operation")" null
expect "a world is replaced" "$(call -o /dev/null -w '%{http_code}' "$OWN/files/content?path=/world/region/r.1.1.mca") $(call -o /dev/null -w '%{http_code}' "$OWN/files/content?path=/world/region/r.0.0.mca") $(call -o /dev/null -w '%{http_code}' "$OWN/files/content?path=/world_nether/DIM-1/region/r.0.0.mca")" "200 404 404"
expect "and the server's other files are kept" "$(call "$OWN/files/content?path=/kept.txt")" kept
expect "and the server can start" "$(call "$OWN" | field "d['status'].get('message', '')")" ""
before=$(call "$OWN/snapshots" | field "[s['id'] for s in d['snapshots'] if s['trigger'] == 'pre-replace'][0]") || fail "no snapshot was taken before the world was replaced"
call -X POST "$OWN/snapshots/$before/restore?wait=true" >/dev/null
expect "the snapshot before it puts the old world back" "$(call -o /dev/null -w '%{http_code}' "$OWN/files/content?path=/world/region/r.0.0.mca") $(call -o /dev/null -w '%{http_code}' "$OWN/files/content?path=/world/region/r.1.1.mca")" "200 404"
ids=$(call "$OWN/snapshots" | field "__import__('json').dumps([s['id'] for s in d['snapshots']])")
call -X DELETE -d "{\"ids\":$ids}" "$API/orgs/$ORG/snapshots" >/dev/null
call -X DELETE "$OWN?snapshot=false" >/dev/null

step "extra ports"
# A port a mod listens on beside the game is opened to players: here a web
# server in the workspace, which shares the game's pod, stands in for one.
ported=$(call -X PATCH -d '{"spec":{"ports":[{"name":"web","port":8100,"protocol":"TCP"}]}}' "$SRV")
expect "a server is given an extra port" "$(echo "$ported" | field "d['spec']['ports']")" "'name': 'web'"
for _ in $(seq 1 60); do
  PUBLIC=$(call "$SRV" | field "next((p.get('publicPort', 0) for p in d['status'].get('ports', [])), 0)")
  [ "$PUBLIC" != 0 ] && break
  sleep 2
done
[ "$PUBLIC" != 0 ] || fail "the extra port was not opened: $(call "$SRV" | field "d['status'].get('ports')")"
echo "ok: it is opened on public port $PUBLIC"
if [ -n "${AVSYS_E2E_PORT_RANGE:-}" ]; then
  first=${AVSYS_E2E_PORT_RANGE%-*}; last=${AVSYS_E2E_PORT_RANGE#*-}
  expect "from the range the install gives out" "$([ "$PUBLIC" -ge "$first" ] && [ "$PUBLIC" -le "$last" ] && echo yes)" yes
  expect "and players are told where" "$(call "$SRV" | field "d['status']['ports'][0].get('address', '')")" ":$PUBLIC"
fi
expect "the game's own port cannot be one" "$(call -X PATCH -d '{"spec":{"ports":[{"name":"game","port":25565}]}}' "$SRV" | field "d['message']")" "the game's own"
if [ -n "${AVSYS_E2E_NODE_IP:-}" ]; then
  # Served for as long as a session of its own lasts, which is ended once
  # it has been reached, or after two minutes.
  ssh -n -i "$WORK/key" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -p "$SSH_PORT" "probe.$ORG@$ssh_host" \
    'echo reached through its port > /tmp/port.txt; cd /tmp && exec timeout 120 python3 -m http.server 8100' >/dev/null 2>&1 &
  serving=$!
  reached=""
  for _ in $(seq 1 30); do
    # From where the install's players would be: beside the node, or on it
    # when nothing else can reach its address.
    reached=$(${AVSYS_E2E_ON_NODE:-} wget -q -T 3 -O - "http://$AVSYS_E2E_NODE_IP:$PUBLIC/port.txt" 2>/dev/null) && break
    sleep 2
  done
  expect "something listening on it is reached from outside" "$reached" "reached through its port"
  kill "$serving" 2>/dev/null || true
else
  echo "skip: reaching an extra port needs a node's address"
fi
expect "and the port is closed again" "$(call -X PATCH -d '{"spec":{"ports":[]}}' "$SRV" | field "d['spec'].get('ports') or []")" "[]"

step "router"
expect "status is proxied by hostname" "$(python3 "$HERE/mcping.py" "$HOST" status "$ROUTER_HOST" "$ROUTER_PORT")" "e2e probe"
expect "unknown hostnames are answered" "$(python3 "$HERE/mcping.py" "nope.$ORG.test" status "$ROUTER_HOST" "$ROUTER_PORT")" "Unknown server"

step "nodes"
# Measuring every node's speed goes on while the rest of this step runs.
expect "the nodes' speed is measured from the panel" "$(call -o /dev/null -w '%{http_code}' -X POST "$API/nodes/measure")" 202
expect "one measuring at a time" "$(call -X POST "$API/nodes/measure" | field "d['code']")" busy
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
# With another node, the server is moved there, its files with it.
OTHER_NODE=$(call "$API/nodes?memory=1Gi" | field "next((n['name'] for n in d['nodes'] if n['ready'] and n['name'] != '$NODE'), '')")
if [ -n "$OTHER_NODE" ]; then
  PLACED="$API/orgs/$ORG/servers/placed"
  for _ in $(seq 1 90); do
    echo "moved with it" | call -f -X PUT --data-binary @- "$PLACED/files/content?path=/moved.txt" >/dev/null 2>&1 && break
    sleep 2
  done
  # Its workspace home too, where people keep what they install, which an
  # ordinary snapshot leaves out.
  echo "home moved with it" | call -X PUT --data-binary @- "$PLACED/files/content?path=/.workspace/moved-home.txt" >/dev/null
  placement=$(call "$PLACED/placement")
  expect "its files are on its node's own disk" "$(echo "$placement" | field "(d.get('node'), d.get('local'))" 2>/dev/null) $placement" "('$NODE', True)"
  moved=$(call -X POST -d "{\"node\":\"$OTHER_NODE\"}" "$PLACED/move?wait=true")
  case "$moved" in *'"code"'*) fail "moving the server: $moved" ;; esac
  expect "a server is moved to another node" "$(call "$PLACED" | field "d['status'].get('node')")" "$OTHER_NODE"
  expect "with its files" "$(call "$PLACED/files/content?path=/moved.txt")" "moved with it"
  expect "and its workspace home" "$(call "$PLACED/files/content?path=/.workspace/moved-home.txt")" "home moved with it"
  expect "and is as it was, stopped" "$(call "$PLACED" | field "(d['spec']['state'], d['status']['phase'])")" "('Stopped', 'Stopped')"
  expect "the snapshot that carried them is kept" "$(call "$PLACED/snapshots" | field "[s['trigger'] for s in d['snapshots']]")" "'move'"
  expect "and it cannot be moved to where it is" "$(call -X POST -d "{\"node\":\"$OTHER_NODE\"}" "$PLACED/move" | field "d['message']")" "on $OTHER_NODE already"
  # Its snapshot is let go of, so that the organization's are the probe's.
  ids=$(call "$PLACED/snapshots" | field "__import__('json').dumps([s['id'] for s in d['snapshots']])")
  call -X DELETE -d "{\"ids\":$ids}" "$API/orgs/$ORG/snapshots" >/dev/null
else
  echo "skip: moving a server needs a second node"
fi
call -X DELETE "$API/orgs/$ORG/servers/placed?snapshot=false" >/dev/null
# A node is kept out of automatic placement, and let back in.
expect "a node can be kept from taking new servers" "$(call -o /dev/null -w '%{http_code}' -X PATCH -d '{"automatic":false}' "$API/nodes/$NODE")" 204
expect "and says so" "$(call "$API/nodes" | field "[n['automatic'] for n in d['nodes'] if n['name'] == '$NODE']")" "[False]"
call -X PATCH -d '{"automatic":true}' "$API/nodes/$NODE" >/dev/null
expect "and let take them again" "$(call "$API/nodes" | field "[n['automatic'] for n in d['nodes'] if n['name'] == '$NODE']")" "[True]"
# As long as the panel lets a measuring take, and a little more.
for _ in $(seq 1 220); do
  [ "$(call "$API/nodes" | field "d['measuring']")" = False ] && break
  sleep 3
done
measured=$(call "$API/nodes")
expect "every node that is up is measured" "$(echo "$measured" | field "(d['measuring'], d.get('measured', {}).get('failed', []), d.get('measured', {}).get('error', ''))")" "(False, [], '')"
expect "and has a speed" "$(echo "$measured" | field "all(n['score'] > 0 for n in d['nodes'] if n['ready'])")" True

step "domains"
# The organization claims its domain; another cannot use a name under it.
expect "an organization is given its domain" "$(call -X PUT -d "{\"domains\":[\"$ORG.test\"]}" "$API/orgs/$ORG/domains" | field "d['domains']")" "['$ORG.test']"
OTHER=$ORG-other
call -d "{\"name\":\"Other $ORG\",\"slug\":\"$OTHER\"}" "$API/orgs" >/dev/null
expect "another organization's server cannot take a name under it" "$(call -d "{\"name\":\"squat\",\"spec\":{\"eula\":true,\"loader\":\"vanilla\",\"version\":\"1.21.4\",\"state\":\"Stopped\",\"expose\":{\"hostname\":\"squat.$ORG.test\"}}}" "$API/orgs/$OTHER/servers" | field "d['code']")" domain_claimed
expect "nor claim one overlapping it" "$(call -X PUT -d "{\"domains\":[\"x.$ORG.test\"]}" "$API/orgs/$OTHER/domains" | field "d['code']")" domain_claimed
expect "and its own server keeps its name" "$(call -X PATCH -d '{"spec":{"displayName":"Probe"}}' "$SRV" | field "d['spec']['expose']['hostname']")" "$HOST"
call -X DELETE "$API/orgs/$OTHER" >/dev/null

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
# A ~/.bashrc of someone's own is read by their commands, never the panel's.
echo 'echo read >> ~/bashrc-read' | call -X PUT --data-binary @- "$SRV/files/content?path=/.workspace/.bashrc" >/dev/null
stopped=$(call -X POST "$SRV/snapshots?wait=true")
LAST=$(echo "$stopped" | field "d['id']") || fail "no snapshot was taken of the stopped server: $stopped"
expect "a stopped server is snapshotted" "$(echo "$stopped" | field "d['trigger']")" manual
expect "the panel's own commands do not read anyone's ~/.bashrc" "$(call -o /dev/null -w '%{http_code}' "$SRV/files/content?path=/.workspace/bashrc-read")" 404
ssh -n -i "$WORK/key" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
  -p "$SSH_PORT" "probe.$ORG@$ssh_host" true >/dev/null 2>&1 || true
expect "a person's commands over ssh do" "$(call "$SRV/files/content?path=/.workspace/bashrc-read")" read
ssh -n -i "$WORK/key" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
  -p "$SSH_PORT" "probe.$ORG@$ssh_host" 'rm -f ~/.bashrc ~/bashrc-read' >/dev/null 2>&1 || true

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
# Answered at once, before its snapshot: the panel sees the rest through.
second=$(call -d '{"name":"twin-2","fromServer":"probe","picture":null,"spec":{"state":"Stopped","properties":{"difficulty":"peaceful","motd":null},"autoWhitelist":"admin"}}' "$API/orgs/$ORG/servers")
expect "a copy is answered with before its files are in" "$(call "$API/orgs/$ORG/servers/twin-2/operation" | field "d and d['kind']")" create
for _ in $(seq 1 300); do [ "$(call "$API/orgs/$ORG/servers/twin-2/operation")" = null ] && break; sleep 1; done
expect "and is then made" "$(call "$API/orgs/$ORG/servers/twin-2" | field "(d['status'].get('snapshots') or {}).get('error', '')")" ""
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

step "a new server given files from this computer"
# As the new-server form does it: made stopped, its files sent straight
# after, before its volume is there, which is waited for; then started.
FRESH=$API/orgs/$ORG/servers/fresh
made=$(call -d '{"name":"fresh","spec":{"eula":true,"loader":"paper","version":"1.21.4","state":"Stopped","resources":{"memory":"2Gi"},"storage":{"size":"2Gi"}}}' "$API/orgs/$ORG/servers")
case "$made" in *'"spec"'*) ;; *) fail "the server was not made: $made" ;; esac
curl -sSfL -o "$WORK/veinminer.zip" "$PACK" || fail "could not fetch the datapack to upload"
sent=$(call -X PUT --data-binary @"$WORK/veinminer.zip" "$FRESH/content?kind=datapack&name=Veinminer-1.2.4.zip&wait=true")
expect "a file sent before its volume is there waits for it, and is listed" "$(echo "$sent" | field "[m['project'] for m in d['spec']['mods'] if m['source'] == 'upload']")" "['Veinminer-1.2.4.zip']"
packed=$(call -X PUT --data-binary @"$WORK/pack.zip" "$FRESH/resource-pack?name=e2e-pack.zip&wait=true")
expect "and so does a resource pack" "$(echo "$packed" | field "d['spec']['resourcePack']['sha1']")" "$PACK_SHA"
call -X POST "$FRESH/start" >/dev/null
wait_phase Running "$FRESH"
expect "started, the game has the datapack" "$(call -d '{"command":"datapack list enabled"}' "$FRESH/console" | field "d['output']")" "Veinminer-1.2.4.zip"
expect "and offers the pack" "$(call "$FRESH/files/content?path=/server.properties" | python3 -c 'import sys; p = dict(l.split("=", 1) for l in sys.stdin.read().splitlines() if "=" in l and not l.startswith("#")); print(p.get("resource-pack-sha1"))')" "$PACK_SHA"
call -o /dev/null -X DELETE "$FRESH?snapshot=false"
for _ in $(seq 1 30); do
  [ "$(call "$API/orgs/$ORG/servers" | field 'len(d)')" = 1 ] && break
  sleep 2
done

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
