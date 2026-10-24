import type { Meta, Server } from "./api";

// Reaching a server's files over SSH, as the Connect dialog says and the
// Files tab offers.

export const sshTarget = (server: Server) => `${server.name}.${server.org}`;

export function sshCommand(server: Server, meta: Meta) {
  const port = meta.ssh.port === 22 ? "" : ` -p ${meta.ssh.port}`;
  return `ssh ${sshTarget(server)}@${meta.ssh.host}${port}`;
}

/**
 * A link that opens the server directory in VS Code over Remote SSH. The host
 * is passed as hex-encoded JSON, the form Remote SSH accepts for hosts that
 * need a user and port.
 */
export function vscodeURL(server: Server, meta: Meta) {
  const host = JSON.stringify({ hostName: meta.ssh.host, user: sshTarget(server), port: meta.ssh.port });
  const hex = Array.from(new TextEncoder().encode(host), (b) => b.toString(16).padStart(2, "0")).join("");
  return `vscode://vscode-remote/ssh-remote+${hex}/data`;
}

