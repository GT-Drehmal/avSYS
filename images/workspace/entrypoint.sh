#!/bin/bash
set -euo pipefail

mkdir -p /tmp/avsys "$HOME"
chmod 700 /tmp/avsys

# Sessions started by sshd do not inherit the container environment.
{
  printf 'export AVSYS_SERVER=%q\n' "${AVSYS_SERVER:-}"
  printf 'export AVSYS_NAMESPACE=%q\n' "${AVSYS_NAMESPACE:-}"
  printf 'export RCON_HOST=localhost RCON_PORT=25575\n'
  printf 'export RCON_PASSWORD=%q\n' "${RCON_PASSWORD:-}"
} > /tmp/avsys/env
chmod 600 /tmp/avsys/env

ssh-keygen -q -t ed25519 -N '' -f /tmp/avsys/ssh_host_ed25519_key
exec /usr/sbin/sshd -D -e -f /etc/avsys/sshd_config
