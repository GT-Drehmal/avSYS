[ -r /tmp/avsys/env ] && . /tmp/avsys/env
# A login shell is a person's, or one they asked for (ssh host bash -lc ...):
# what they have put in their home is found before what the image has. Home
# is on the server's storage, so it survives restarts; it is left out of
# snapshots. The panel's own commands do not come through here.
tools="$HOME/.local/tools/$(uname -m)/bin"
case ":$PATH:" in *":$tools:"*) ;; *) PATH="$tools:$PATH" ;; esac
unset tools
case ":$PATH:" in *":$HOME/.local/bin:"*) ;; *) PATH="$HOME/.local/bin:$PATH" ;; esac
export PATH
# Land in the server directory rather than the workspace's own home.
if [ "$PWD" = "$HOME" ] && [ -d /data ]; then cd /data; fi
