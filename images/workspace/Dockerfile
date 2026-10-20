# The workspace runs next to every Minecraft server with its /data volume
# mounted. The avSYS gateway proxies SSH, the web terminal and the file browser
# to the sshd in here.
#
# Built from the repository root, so that the license can be included:
#   docker build -f images/workspace/Dockerfile .

# Snapshots are taken from in here, where the server's files are.
FROM restic/restic:0.19.1 AS restic

FROM debian:bookworm-slim

LABEL org.opencontainers.image.source="https://github.com/GT-Drehmal/avSYS" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.description="avSYS workspace: the SSH sidecar next to each Minecraft server"

ARG TARGETARCH
ARG RCON_CLI_VERSION=1.7.7
ARG MICROMAMBA_VERSION=2.3.2-0

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      openssh-server ca-certificates tini \
      bash-completion curl wget git rsync tar gzip unzip zip \
      nano vim-tiny less htop jq procps tmux file tree \
      python3 python3-venv \
 && rm -rf /var/lib/apt/lists/* /etc/ssh/ssh_host_* \
 && curl -fsSL "https://github.com/itzg/rcon-cli/releases/download/${RCON_CLI_VERSION}/rcon-cli_${RCON_CLI_VERSION}_linux_${TARGETARCH}.tar.gz" \
      | tar -xz -C /usr/local/bin rcon-cli \
 # What `tools` installs programs into a home with, without root.
 && curl -fsSL -o /usr/local/bin/micromamba "https://github.com/mamba-org/micromamba-releases/releases/download/${MICROMAMBA_VERSION}/micromamba-linux-$([ "$TARGETARCH" = arm64 ] && echo aarch64 || echo 64)" \
 && chmod +x /usr/local/bin/micromamba \
 # Same uid/gid the game runs as, so files are editable from both containers.
 && groupadd -g 1000 mc \
 && useradd -u 1000 -g 1000 -d /data/.workspace -s /usr/local/bin/avsys-shell mc

COPY --from=restic /usr/bin/restic /usr/local/bin/restic
COPY images/workspace/sshd_config /etc/avsys/sshd_config
COPY images/workspace/profile.sh /etc/profile.d/avsys.sh
COPY images/workspace/bashrc /etc/bash.bashrc
COPY images/workspace/entrypoint.sh /usr/local/bin/avsys-workspace
COPY images/workspace/mc /usr/local/bin/mc
COPY images/workspace/tools /usr/local/bin/tools
COPY images/workspace/shell /usr/local/bin/avsys-shell
# Found before the real ones, to say why they will not do and what will.
COPY images/workspace/no-root /usr/local/bin/sudo
RUN for name in su doas apt apt-get; do ln -s sudo /usr/local/bin/$name; done
COPY LICENSE /licenses/LICENSE

USER 1000:1000
WORKDIR /data
EXPOSE 2222
ENTRYPOINT ["tini", "--", "avsys-workspace"]
