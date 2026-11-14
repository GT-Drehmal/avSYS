package controller

// modConfig sets up the mods ports were opened for, from AVSYS_MOD_PORTS
// (see modPortsEnv), before the game starts. In each mod's own config it
// sets only what has to match the panel, and only where it differs, and
// leaves everything else in the file as it is:
//
//   - Geyser: bedrock.port, the port it listens on. Once its public port is
//     known, the port Bedrock players are told: advanced.bedrock.broadcast-port
//     (bedrock.broadcast-port in older versions), and in newer ones
//     bedrock.signaling.advertise-addresses, where they connect to.
//   - Simple Voice Chat: port, and voice_host, the address players are
//     told, once it is known.
//   - Plasmo Voice: port under [host], and, once the address is known,
//     port and ip under [host.public], what players are told: the IP when
//     the address is one, or else the one they reach the game at.
//   - BlueMap: port in webserver.conf, and accept-download in core.conf,
//     which says the Minecraft EULA is accepted, as the server's is.
//   - Dynmap: webserver-port, and publicURL once the address is known.
//   - squaremap: settings.internal-webserver.port, and settings.web-address
//     once the address is known.
//
// A file that is not there yet, before the mod's first start, is made with
// just these, and the mod fills in the rest. Dynmap's is the exception: it
// needs the whole file, which it writes on its first start with the port
// the panel opens.
const modConfig = `if [ -n "${AVSYS_MOD_PORTS:-}" ]; then
# Sets "key: value" at a path of keys in a YAML file, or one commented out
# there: in place, or under the nearest of its parents there. A value
# "- item" is a list of that one item, written under the key. It fails on
# a parent with a value of its own on its line.
yml() {
  awk -v path="$2" -v val="$3" '
  function lead(s) { match(s, /^ */); return RLENGTH }
  function pad(n,  s) { s = ""; while (n-- > 0) s = s " "; return s }
  function set(ind, key) { return pad(ind) key ":" (val ~ /^- / ? "\n" pad(ind + 2) val : " " val) }
  { line[NR] = $0 }
  END {
    n = split(path, want, ".")
    depth = 0; found = 0; cand = 0; best = 0; bestline = 0
    for (i = 1; i <= NR; i++) {
      s = line[i]
      if (s ~ /^[ \t]*$/) continue
      ind = lead(s); rest = substr(s, ind + 1)
      if (rest ~ /^#/) {
        r = rest; sub(/^#[ \t]*/, "", r)
        if (cand || !match(r, /^[A-Za-z0-9_.-]+:/)) continue
        k = substr(r, 1, RLENGTH - 1)
        d = 0; ok = 1
        for (j = 1; j <= depth; j++) if (sind[j] < ind) { d++; if (d >= n || skey[j] != want[d]) ok = 0 }
        if (ok && d == n - 1 && k == want[n]) { cand = i; candind = ind }
        continue
      }
      if (!match(rest, /^[A-Za-z0-9_.-]+:/)) continue
      k = substr(rest, 1, RLENGTH - 1)
      v = substr(rest, RLENGTH + 1); sub(/^[ \t]+/, "", v)
      while (depth > 0 && sind[depth] >= ind) depth--
      depth++; sind[depth] = ind; skey[depth] = k
      ok = depth <= n
      for (j = 1; ok && j <= depth; j++) if (skey[j] != want[j]) ok = 0
      if (!ok) continue
      if (depth == n) { if (!found) { found = i; foundind = ind }; continue }
      if (v != "" && v !~ /^#/) exit 3
      if (depth > best) { best = depth; bestline = i; bestind = ind }
    }
    if (found) {
      line[found] = set(foundind, want[n])
      # What was under it goes: a list, or keys of its own.
      for (i = found + 1; i <= NR; i++) {
        if (line[i] ~ /^[ \t]*$/) continue
        if (lead(line[i]) > foundind || (lead(line[i]) == foundind && line[i] ~ /^ *- /)) gone[i] = 1
        else break
      }
    } else if (cand) line[cand] = set(candind, want[n])
    else {
      # At the end of the nearest parent there, as indented as what is
      # under it.
      ci = 0
      if (best) {
        ci = bestind + 2
        for (i = bestline + 1; i <= NR; i++) {
          if (line[i] ~ /^[ \t]*$/ || line[i] ~ /^[ \t]*#/) continue
          if (lead(line[i]) > bestind) ci = lead(line[i])
          break
        }
        for (i = bestline + 1; i <= NR; i++) {
          if (line[i] ~ /^[ \t]*$/) continue
          if (lead(line[i]) <= bestind) break
          bestline = i
        }
      }
      add = ""
      for (j = best + 1; j < n; j++) add = add pad(ci + 2 * (j - best - 1)) want[j] ":\n"
      add = add set(ci + 2 * (n - best - 1), want[n])
    }
    for (i = 1; i <= NR; i++) {
      if (gone[i]) continue
      print line[i]
      if (!found && !cand && best && i == bestline) print add
    }
    if (!found && !cand && !best) print add
  }' "$1"
}
# Sets "key=value" in a file of Java properties.
props() {
  awk -v k="$2" -v v="$3" '
  !done && $0 ~ ("^[ \t]*" k "[ \t]*[=:]") { print k "=" v; done = 1; next }
  { print }
  END { if (!done) print k "=" v }' "$1"
}
# Sets "key = value" in a table of a TOML file, the table and key given as
# one path: at the end of the table, or of the file with the table.
toml() {
  awk -v sec="${2%.*}" -v k="${2##*.}" -v v="$3" '
  { line[NR] = $0 }
  END {
    cur = ""; at = 0; head = 0
    for (i = 1; i <= NR; i++) {
      s = line[i]
      if (s ~ /^[ \t]*\[/) {
        h = s; sub(/^[ \t]*\[[ \t]*/, "", h); sub(/[ \t]*\].*$/, "", h); cur = h
        if (cur == sec && !head) head = i
        continue
      }
      if (cur == sec && s !~ /^[ \t]*$/) last = i
      if (cur == sec && !at && s ~ ("^[ \t]*" k "[ \t]*=")) at = i
    }
    if (head && last > head) head = last
    if (at) line[at] = k " = " v
    for (i = 1; i <= NR; i++) { print line[i]; if (!at && i == head) print k " = " v }
    if (!at && !head) { if (NR) print ""; print "[" sec "]"; print k " = " v }
  }' "$1"
}
# set_in FORMAT FILE KEY VALUE [new]: sets one in a mod's file, under
# /data, where it differs. Without "new" a file not there is left so.
set_in() {
  to=/data/$2
  from=$to
  if [ ! -f "$to" ]; then
    [ "${5:-}" = new ] && mkdir -p "${to%/*}" 2>/dev/null || return 0
    from=/dev/null
  fi
  if "$1" "$from" "$3" "$4" >"$to.avsys" 2>/dev/null && ! cmp -s "$to.avsys" "$to"; then
    cat "$to.avsys" >"$to" && echo "[avsys] $title: $3 set to ${4#- } in $2"
  fi
  rm -f "$to.avsys" 2>/dev/null
}
case $kind in PAPER | FOLIA | PURPUR) plugins=yes ;; *) plugins= ;; esac
printf '%s\n' "$AVSYS_MOD_PORTS" | while read -r mod port protocol public address; do
  case $port in '' | *[!0-9]*) continue ;; esac
  case $public in '' | *[!0-9]*) public=0 ;; esac
  [ "$public" != 0 ] && [ -n "$address" ] && [ "$address" != - ] || address=
  host=${address%:*}
  case $mod/$protocol in
    geyser/UDP)
      title=Geyser
      if [ -n "$plugins" ]; then f=plugins/Geyser-Spigot/config.yml
      else case $kind in NEOFORGE) f=config/Geyser-NeoForge/config.yml ;; *) f=config/Geyser-Fabric/config.yml ;; esac; fi
      # Newer versions lay their settings out anew, with an advanced part.
      # Before its first start it is given what both read, which a newer
      # one moves where it keeps it.
      if grep -q '^advanced:' "/data/$f" 2>/dev/null; then laid=new; else laid=; fi
      [ -f "/data/$f" ] || laid=both
      set_in yml "$f" bedrock.port "$port" new
      if [ -n "$address" ]; then
        case $address in \[*) item="\"$address\"" ;; *) item=$address ;; esac
        case $laid in
          new) set_in yml "$f" advanced.bedrock.broadcast-port "$public" ;;
          *) set_in yml "$f" bedrock.broadcast-port "$public" new ;;
        esac
        [ -z "$laid" ] || set_in yml "$f" bedrock.signaling.advertise-addresses "- $item" new
      fi
      ;;
    simple-voice-chat/*)
      title="Simple Voice Chat"
      f=config/voicechat/voicechat-server.properties
      [ -z "$plugins" ] || f=plugins/voicechat/voicechat-server.properties
      set_in props "$f" port "$port" new
      [ -z "$address" ] || set_in props "$f" voice_host "$address" new
      ;;
    plasmo-voice/*)
      title="Plasmo Voice"
      f=config/plasmovoice/server/config.toml
      [ -z "$plugins" ] || f=plugins/PlasmoVoice/config.toml
      set_in toml "$f" host.port "$port" new
      if [ -n "$address" ]; then
        # An IP as it is; for a name, 0.0.0.0 is the one players reach the game at.
        ip=0.0.0.0
        case $host in *[!0-9.]* | '') ;; *) ip=$host ;; esac
        set_in toml "$f" host.public.ip "\"$ip\"" new
        set_in toml "$f" host.public.port "$public" new
      fi
      ;;
    bluemap/*)
      title=BlueMap
      d=config/bluemap
      [ -z "$plugins" ] || d=plugins/BlueMap
      set_in yml "$d/webserver.conf" port "$port" new
      [ "${EULA:-}" != true ] || set_in yml "$d/core.conf" accept-download true new
      ;;
    dynmap/*)
      title=Dynmap
      f=dynmap/configuration.txt
      [ -z "$plugins" ] || f=plugins/dynmap/configuration.txt
      set_in yml "$f" webserver-port "$port"
      [ -z "$address" ] || set_in yml "$f" publicURL "\"http://$address/\""
      ;;
    squaremap/*)
      title=squaremap
      f=squaremap/config.yml
      [ -z "$plugins" ] || f=plugins/squaremap/config.yml
      set_in yml "$f" settings.internal-webserver.port "$port" new
      [ -z "$address" ] || set_in yml "$f" settings.web-address "http://$address" new
      ;;
  esac
done
fi
`
