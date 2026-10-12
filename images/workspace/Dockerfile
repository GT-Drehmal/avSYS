# The workspace runs next to every Minecraft server with its /data volume
# mounted. The avSYS gateway proxies SSH, the web terminal and the file browser
# to the sshd in here.
#
# Built from the repository root, so that the license can be included:
#   docker build -f images/workspace/Dockerfile .
FROM debian:bookworm-slim

LABEL org.opencontainers.image.source="https://github.com/GT-Drehmal/avSYS" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.description="avSYS workspace: the SSH sidecar next to each Minecraft server"

ARG TARGETARCH
ARG RCON_CLI_VERSION=1.7.7

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      openssh-server ca-certificates tini \
      bash-completion curl wget git rsync tar gzip unzip zip \
      nano vim-tiny less htop jq procps tmux \
 && rm -rf /var/lib/apt/lists/* /etc/ssh/ssh_host_* \
 && curl -fsSL "https://github.com/itzg/rcon-cli/releases/download/${RCON_CLI_VERSION}/rcon-cli_${RCON_CLI_VERSION}_linux_${TARGETARCH}.tar.gz" \
      | tar -xz -C /usr/local/bin rcon-cli \
 # Same uid/gid the game runs as, so files are editable from both containers.
 && groupadd -g 1000 mc \
 && useradd -u 1000 -g 1000 -d /data/.workspace -s /bin/bash mc

COPY images/workspace/sshd_config /etc/avsys/sshd_config
COPY images/workspace/profile.sh /etc/profile.d/avsys.sh
COPY images/workspace/bashrc /etc/bash.bashrc
COPY images/workspace/entrypoint.sh /usr/local/bin/avsys-workspace
COPY images/workspace/mc /usr/local/bin/mc
COPY LICENSE /licenses/LICENSE

USER 1000:1000
WORKDIR /data
EXPOSE 2222
ENTRYPOINT ["tini", "--", "avsys-workspace"]
