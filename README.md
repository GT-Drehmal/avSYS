# avSYS

A Minecraft server panel that is native to Kubernetes. Built for GT Drehmal; not affiliated with Primordial Team.

Each server is a `MinecraftServer` resource. avSYS turns it into a pod running [itzg/minecraft-server](https://github.com/itzg/docker-minecraft-server) with its own volume, and gives you a web UI, an API and SSH on top.

- **Servers in a minute.** Pick a game version and loader (Vanilla, Paper, Fabric, Forge, NeoForge, Purpur, Quilt, Folia) or a modpack from Modrinth, CurseForge or a `.mrpack`. Start from a new world, one from your world library, or one you upload. Add mods, plugins, datapacks and resource packs from Modrinth, CurseForge, a URL, or a file on your computer; each is pinned to a version that fits.
- **One form for every way in.** New, from a template, a copy of a server, a snapshot, or an archive of a server you ran somewhere else. A deleted server comes back from its snapshot with its history.
- **Your own tools.** `ssh <server>.<org>@panel` drops you in the server directory, authenticated by the SSH keys on your account. scp, sftp, rsync and VS Code Remote SSH work. There is also a terminal and a file editor in the browser.
- **Organizations.** Each has its own members, roles, servers and namespace. Sign in with a password (with two-factor), GitHub, Discord, Google or your own OpenID Connect provider; join by invite link.
- **Players.** Members are whitelisted and made operators by role, and kept that way. Saved player lists, Java and Bedrock, go on any number of servers.
- **One address, many servers.** The built-in router sends players to the right server by the hostname they connect with, and can put idle servers to sleep and wake them when someone joins.
- **Snapshots.** Save a server's files and settings with one click, while it runs, and put it back later. Every server is snapshotted daily, into one deduplicated store per organization, and can be copied offsite.
- **Live.** Status, players and the console stream to the browser; nothing polls.

## Install

```sh
helm install avsys oci://ghcr.io/gt-drehmal/charts/avsys -n avsys --create-namespace \
  --set publicURL=https://panel.example.com
```

Or `kubectl apply -f deploy/install.yaml` after setting `AVSYS_PUBLIC_URL` in it. Open the panel and create the first account: it becomes the administrator. Everything else is in [Install and operate](docs/install.md).

## Documentation

| | |
| --- | --- |
| [User guide](docs/user-guide.md) | Signing in, organizations and roles, making servers, content, players, the console and files, SSH, backups, templates and worlds |
| [Install and operate](docs/install.md) | Helm and `install.yaml`, every setting, sign-in providers, CurseForge, offsite backups, addresses and DNS, updating, troubleshooting |
| [Architecture](docs/architecture.md) | The parts, what a server is made of, its life, sleeping and waking, content, snapshots, security |
| [API reference](docs/api.md) | Every route, who may call it, and what it takes and gives |
| [Development](docs/development.md) | Building, running locally, tests, releases |
| [Long actions](docs/long-actions.md) | How the panel shows work that takes a while |

```console
$ kubectl get mcs -A
NAMESPACE       NAME       PHASE     VERSION   LOADER   PLAYERS   ADDRESS
mc-friends      creative   Running   1.21.4    paper    3         play.example.org
mc-gt-drehmal   drehmal    Sleeping  1.20.1    fabric             mc.example.com
```

## License

Apache-2.0. See [LICENSE](LICENSE).
