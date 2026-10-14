. /tmp/avsys/env 2>/dev/null
# What someone puts in their home here is found before what the image has.
# Home is on the server's storage, so it survives restarts; it is left out
# of snapshots.
case ":$PATH:" in *":$HOME/.local/bin:"*) ;; *) PATH="$HOME/.local/bin:$PATH" ;; esac
export PATH
# Land in the server directory rather than the workspace's own home.
if [ "$PWD" = "$HOME" ] && [ -d /data ]; then cd /data; fi
