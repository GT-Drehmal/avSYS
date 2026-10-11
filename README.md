# avSYS

A Minecraft server panel that is native to Kubernetes. Built for GT Drehmal; not affiliated with Primordial Team.

Each server is a `MinecraftServer` resource. avSYS turns it into a pod running [itzg/minecraft-server](https://github.com/itzg/docker-minecraft-server) with its own volume, and gives you a web UI, an API and SSH on top.

- **Servers in a minute.** Pick a game version and loader (Vanilla, Paper, Fabric, Forge, NeoForge, Purpur, Quilt, Folia) or a Modrinth modpack. Add mods, plugins and datapacks from Modrinth, a URL, or a file on your computer.
- **Your own tools.** `ssh <server>.<org>@panel` drops you in the server directory, authenticated by the SSH keys on your account. scp, sftp, rsync and VS Code Remote SSH work. There is also a terminal and a simple file editor in the browser.
- **Organizations.** Each has its own members, roles, servers and namespace. People can belong to several. Sign in with a password, GitHub, Discord or Google; join by invite link.
- **One address, many servers.** The built-in router sends players to the right server by the hostname they connect with, and can put idle servers to sleep and wake them when someone joins.
- **Live.** Status, players and the console stream to the browser; nothing polls.

## How it fits together

One binary, `avsys`, runs four things:

| Part | Port | What it does |
| --- | --- | --- |
| Controller | – | Reconciles `MinecraftServer` resources into a pod, volume, service and secrets |
| Web UI and API | 8080 | The panel, and the REST API under `/api/v1` that it is built on |
| SSH gateway | 2222 | Authenticates people and relays them to a server's workspace |
| Router | 25565 | Routes players by hostname; wakes sleeping servers |

Every server pod has a small **workspace** container next to the game with the same `/data` volume. That is what SSH, the web terminal and the file browser connect to, and it keeps running while the game is stopped, so a server that will not start can still be fixed.

Servers live in Kubernetes; people (users, organizations, roles, keys, invites) live in a SQLite database on the panel's volume. An organization called `friends` gets the namespace `mc-friends`.

```console
$ kubectl get mcs -A
NAMESPACE       NAME       PHASE     VERSION   LOADER   PLAYERS   ADDRESS
mc-friends      creative   Running   1.21.4    paper    3         play.example.org
mc-gt-drehmal   drehmal    Sleeping  1.20.1    fabric             mc.example.com
```

## Mods, plugins and Java

Content is resolved when you add it, not when the server starts:

- Each Modrinth entry is **pinned** to one version. Restarts never change what is installed; you change a version from the server's Mods tab.
- The panel reads each file to see which **Java** it was compiled for, and picks the newest release that runs on the Java the game version ships with. A plugin can list a game version as supported and still need a newer Java; such a build downloads fine and then fails to load.
- If nothing fits, or you pin a newer build yourself, the server's Java is raised to match. The server page shows the Java it runs on.
- If a project has no build for the server's loader and game version, adding it fails with that message instead of a broken start.
- "Latest release" is settled to the actual version when a server is created, so a restart never upgrades a world on its own. A modpack's own game version decides its Java.
- Uploaded files are kept on the server's volume under `.avsys/content` and installed like any other entry.

Dependencies are still fetched by the server image at start and are not pinned or checked.

## Install

```sh
kubectl apply -f deploy/install.yaml
```

Edit `AVSYS_PUBLIC_URL` in that file first. It creates the `avsys` namespace, a service account with the permissions the panel needs, a 1Gi volume for the database, and a LoadBalancer service for the three ports. The panel installs and upgrades the `MinecraftServer` CRD itself.

Images are published to `ghcr.io/gt-drehmal/avsys` for amd64 and arm64. `:edge` follows the main branch; releases are `:v1.2.3`, with `:latest` on the newest. The panel starts the workspace image of its own version, so there is only the one image to choose.

The first account you create becomes the instance administrator. After that, accounts are created through invite links unless `AVSYS_ALLOW_SIGNUPS=true`.

### Configuration

Every flag can also be set as an environment variable: `--public-url` is `AVSYS_PUBLIC_URL`. Run `avsys --help` for the full list.

| Variable | Purpose |
| --- | --- |
| `AVSYS_PUBLIC_URL` | Address people open the panel at. OAuth callbacks are built from it. |
| `AVSYS_SSH_PUBLIC_HOST`, `AVSYS_SSH_PUBLIC_PORT` | Where the SSH gateway is reachable, if not the panel's host and port 2222. |
| `AVSYS_ROUTER_DOMAIN` | A domain with a wildcard DNS record pointing at the router. New servers default to `<server>.<org>.<domain>`. |
| `AVSYS_GITHUB_CLIENT_ID`, `AVSYS_GITHUB_CLIENT_SECRET` | Enable GitHub sign-in. Likewise `DISCORD` and `GOOGLE`. The callback URL to register is `<public-url>/api/v1/auth/<provider>/callback`. |
| `AVSYS_ROUTER_PUBLIC_PORT` | The port players reach the router on, if not 25565. Shown in server addresses. |
| `AVSYS_CURSEFORGE_API_KEY` | Passed to servers for CurseForge downloads. |
| `AVSYS_ALLOW_SIGNUPS` | `true` lets anyone create an account. |

### Which node a server runs on

A game server's speed is mostly the speed of one CPU core, which Kubernetes does not know. So avSYS places servers itself: a new server goes to the **highest-scoring node that has enough unreserved memory**, and falls down the ranking only when the better nodes are full. The choice is recorded, shown on the server page, and permanent on node-local storage, where the world lives on that node's disk.

```sh
./hack/rank-nodes.sh       # measures every node and sets the avsys.dev/score label
kubectl label node pi4-1 avsys.dev/game-servers=false   # optional: never host here
```

Nodes without a score can still host, after every node that has one. "Room" means memory not yet requested by other pods; a server reserves its whole limit while it runs and releases it while stopped or asleep. A server with its own `nodeSelector` is left to the scheduler.

### Clusters without a load balancer

The manifest exposes the panel with a `LoadBalancer` service. If your cluster has none (k3s started with ServiceLB disabled and no MetalLB, for example), that service never gets an address. Pin the panel to one node and publish its ports on that node instead: add a `nodeSelector` and a `hostPort` beside each `containerPort` in the Deployment, and change the Service to `type: ClusterIP`.

```yaml
      nodeSelector:
        kubernetes.io/hostname: my-node
      containers:
        - name: avsys
          ports:
            - { name: http, containerPort: 8080, hostPort: 8080 }
            - { name: ssh, containerPort: 2222, hostPort: 2222 }
            - { name: minecraft, containerPort: 25565, hostPort: 25565 }
```

### Addresses

For the shared router, point each server's hostname (an A or CNAME record) at the avSYS service's external IP. Servers that need their own IP can use exposure type LoadBalancer instead; that works with k3s ServiceLB and MetalLB.

## Roles

| Role | Can |
| --- | --- |
| Viewer | See servers, status and console output |
| Operator | Also start and stop servers, run commands, SSH in, use the terminal and files |
| Admin | Also create, configure and delete servers; manage members and invites |
| Owner | Also manage other owners and delete the organization |

Instance administrators act as owner in every organization and are the only ones who can create organizations.

## API

The web UI is a client of the REST API, so anything it does can be scripted. Create a token under **Account → API tokens**:

```sh
export AVSYS=https://panel.example.com/api/v1
export TOKEN=avsys_...

curl -H "Authorization: Bearer $TOKEN" $AVSYS/orgs/friends/servers
curl -H "Authorization: Bearer $TOKEN" -X POST $AVSYS/orgs/friends/servers/creative/restart
curl -H "Authorization: Bearer $TOKEN" -d '{"command":"say hello"}' $AVSYS/orgs/friends/servers/creative/console
curl -H "Authorization: Bearer $TOKEN" -X PATCH -d '{"spec":{"resources":{"memory":"6Gi"}}}' $AVSYS/orgs/friends/servers/creative
```

A server's `spec` is the same in the API and in the Kubernetes resource, so GitOps tools can manage servers directly as `MinecraftServer` manifests.

## Inside a server

```console
$ ssh creative.friends@panel.example.com -p 2222
steve@creative:/data$ mc run list
There are 3 of a max of 20 players online: ...
steve@creative:/data$ mc logs        # follow the log
steve@creative:/data$ mc restart     # restart the game, keeping this shell
```

To move an existing world in, stop the server and copy over it:

```sh
rsync -av -e 'ssh -p 2222' ./world/ creative.friends@panel.example.com:world/
```

## Development

Needs Go, [Bun](https://bun.sh), Docker and a cluster in your kubectl context (OrbStack's works well).

```sh
make dev        # builds everything and runs the panel on http://localhost:8080
make test       # unit tests and type checks
make e2e        # creates a real server through a running panel and checks it end to end
make generate   # after changing api/
```

`make e2e` needs `AVSYS_URL` and `AVSYS_TOKEN` (an instance admin's API token). It makes a throwaway organization and a Paper server with plugins, checks the console, files, SSH and the router against it, and deletes both. Unit tests cover the logic that needs no cluster; this covers what only a running server can show.

To cut a release, push a version tag: `git tag v0.1.0 && git push origin v0.1.0`. CI tests it and publishes both images under that tag.

`make dev` runs the panel on your machine against the current kubectl context, reaching pods through the API server, and keeps its database in `.dev/`. For UI work, also run `bun run dev` in `web/` and open the address Vite prints; it proxies API calls to the panel.

| Path | What is there |
| --- | --- |
| `api/v1alpha1` | The `MinecraftServer` resource |
| `internal/controller` | Reconciler, pod rendering, player monitor |
| `internal/api` | HTTP API, live events, console, terminal, files |
| `internal/sshgw` | SSH gateway |
| `internal/router` | Hostname router |
| `internal/store` | Users, organizations, invites (SQLite) |
| `images/workspace` | The SSH sidecar image |
| `web` | The React UI |
| `deploy` | CRD and install manifest |

Earlier planning notes: <https://docs.google.com/document/d/1D_mDTFApseszykIvamCIFB8B9JzJdk8vqC-D3iDWjWQ/edit?usp=sharing>
