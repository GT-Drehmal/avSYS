. /tmp/avsys/env 2>/dev/null
# Land in the server directory rather than the workspace's own home.
if [ "$PWD" = "$HOME" ] && [ -d /data ]; then cd /data; fi
