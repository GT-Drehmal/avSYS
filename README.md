# avSYS

A Minecraft server panel that is native to Kubernetes. Built for GT Drehmal; not affiliated with Primordial Team.

Each server is a `MinecraftServer` resource. avSYS turns it into a pod running [itzg/minecraft-server](https://github.com/itzg/docker-minecraft-server) with its own volume, and gives you a web UI, an API and SSH on top.

- **Servers in a minute.** Pick a game version and loader (Vanilla, Paper, Fabric, Forge, NeoForge, Purpur, Quilt, Folia) or a Modrinth modpack. Set the game mode, difficulty, world type and seed before the world is generated. Add mods, plugins and datapacks from Modrinth, a URL, or a file on your computer.
- **Your own tools.** `ssh <server>.<org>@panel` drops you in the server directory, authenticated by the SSH keys on your account. scp, sftp, rsync and VS Code Remote SSH work. There is also a terminal and a simple file editor in the browser.
- **Organizations.** Each has its own members, roles, servers and namespace. People can belong to several. Sign in with a password, GitHub, Discord or Google; join by invite link.
- **One address, many servers.** The built-in router sends players to the right server by the hostname they connect with, and can put idle servers to sleep and wake them when someone joins.
- **Snapshots.** Save a server's files and settings with one click, while it runs, and put it back later. Each organization's servers share one deduplicated store, so a snapshot costs only what changed. Snapshots outlive the server they were taken of, and a new server can be made from any of them: a deleted one brought back, or a copy to try something on.
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
- Each loader is asked which **game versions** it has a build for, and only those are offered. A new game version usually reaches Vanilla first and the others days or weeks later. Asking for one a loader does not have yet fails with that message instead of a broken start.
- **Snapshots and pre-releases** join the version list with the checkbox beside it: every one a server can be made of, each in its place between the releases. Versions Mojang never published a server for (everything before 1.2.5, and the alpha and beta versions) are not listed.
- A snapshot named by the week it came out in, such as `24w14a`, runs on the Java of the release it led to.
- "Latest release" is the newest version the chosen loader has a build for. It is settled to the actual version when a server is created, so a restart never upgrades a world on its own. A modpack's own game version decides its Java.
- Uploaded files are kept on the server's volume under `.avsys/content` and installed like any other entry.

Dependencies are still fetched by the server image at start and are not pinned or checked.

## Snapshots

A snapshot is the server's whole directory, minus the workspace's own files and download caches, plus a record of its settings. Take one from the server's **Backups** tab or with the API; a running game is told to write everything out and hold its saves for the moment the files are copied.

- **Storage.** Every organization has one [restic](https://restic.net) repository on the panel's backups volume. Servers that share files (the same mods, libraries, a common map) store them once, and each snapshot adds only what changed since the last.
- **Every day.** Each server is snapshotted once a day without anyone asking, at a time set on the organization's **Backups** page (05:00 US Eastern unless changed). A server whose files are exactly as its last snapshot has them gets no new one. Of these scheduled snapshots the newest of each of the last 7 days, 4 weeks, 12 months and 2 years is kept, which can be changed on the same page, or the daily snapshots switched off. Days on which nothing was saved do not count, so a server that stops changing keeps the history it has, however old. A snapshot that fails, or does not happen, shows on the server list.
- **A server's own schedule.** The organization's schedule is what a server follows unless it sets its own, on its **Backups** tab: off, for a server not worth keeping, or its own time and its own retention, for one worth keeping longer. A server can have a schedule while its organization's is off. "Run now" passes over a server that is switched off.
- **What is never removed for you.** Retention only applies to scheduled snapshots of servers that exist. Snapshots taken by hand, the ones taken before a restore or a delete, and every snapshot of a server that has been deleted stay until someone deletes them.
- **Restore.** Restoring puts a server back to one of its own snapshots: its world, configuration, game version and mods. The server is stopped while the files are put back, and what was there is snapshotted first, so a restore can be undone. Memory, storage, address and sleep settings are left as they are.
- **New server from a snapshot.** Any snapshot can be made into a new server, from the **Backups** page or with the API. It gets the snapshot's files along with the game version, loader, mods and game settings they were made with, and its own name, address, memory and storage. Made from a snapshot of a server that was deleted, under the same name, it is that server back with its history. Made from one of a server that still exists, it is a copy, and the original is not touched. Until its files are all in, the new server cannot be started or changed; deleting it calls the copy off.
- **Duplicating a server.** **Duplicate** on a server's page makes a copy of it as it is now, world included, under a name and address of its own. The original keeps running. A snapshot is taken for the copy to be made from, unless the server's newest one already has everything.
- **Deleting a server.** A last snapshot is taken first, so a deleted server can be brought back exactly as it was. The delete dialog has a switch for it, and the API takes `?snapshot=false`. If that snapshot cannot be taken, the server is not deleted.
- **Offsite copy.** Give the panel a second place and every organization's snapshots are copied there, a couple of minutes after they change and once after each start. It takes any [restic repository location](https://restic.readthedocs.io/en/stable/030_preparing_a_new_repo.html) that needs no ssh: S3 and what speaks it (Backblaze B2, Cloudflare R2, Wasabi), Azure, Google Cloud, or a [rest-server](https://github.com/restic/rest-server). See [Offsite copy](#offsite-copy) for setting it up and for getting snapshots back.
- **History is kept.** Deleting a server does not delete its snapshots; they stay on the organization's **Backups** page until someone deletes them. Deleting an organization deletes its repository, and asks first.
- **What a server can do to its own history.** Snapshots are taken from inside the server's workspace, over a channel the panel opens for the purpose. Through it a server can add to its organization's repository and read from it, but not delete or overwrite anything. Only the panel removes snapshots.
- **One organization, one trust boundary.** Servers in an organization share a repository, so someone with shell access to one of them could, with effort, read snapshots of another. Put servers that must not see each other's files in different organizations.
- **Without the panel.** A repository is an ordinary restic repository with its password in the file beside it, so snapshots can be listed and restored with plain restic:

  ```sh
  restic -r /backups/friends --password-file /backups/friends.key snapshots
  restic -r /backups/friends --password-file /backups/friends.key restore <id>:/data --target ./restored
  ```

Snapshots live on the cluster only unless it is given a second place to copy them to: see [Offsite copy](#offsite-copy).

After upgrading from a version without snapshots, a server has to be restarted once before it can be snapshotted; its page says so.

## Install

With [Helm](https://helm.sh):

```sh
helm install avsys oci://ghcr.io/gt-drehmal/charts/avsys -n avsys --create-namespace \
  --set publicURL=https://panel.example.com
```

Or with `kubectl` alone, after setting `AVSYS_PUBLIC_URL` in the file to the address you will open the panel at:

```sh
kubectl apply -f deploy/install.yaml
```

Both make the same thing: the `avsys` namespace, a service account with the permissions the panel needs, a 1Gi volume for the database, a 50Gi volume for snapshots, and a LoadBalancer service for the three ports. `deploy/install.yaml` is the chart in `deploy/chart` with its default values; anything else is a value of the chart. Size the snapshots volume for the worlds you host. The panel installs and upgrades the `MinecraftServer` CRD itself each time it starts, so upgrading the panel upgrades the CRD too (Helm installs the chart's copy only on the first install). Uninstalling keeps both volumes; delete them by hand if you mean to.

The chart is published with each [release](https://github.com/GT-Drehmal/avSYS/releases), whose notes have the commands for that version and its own `install.yaml`. To install from a checkout instead, use `./deploy/chart` in place of the `oci://` address.

Images are published to `ghcr.io/gt-drehmal/avsys` for amd64 and arm64. Releases are `:v1.2.3`, with `:latest` on the newest; `:edge` follows the main branch. A chart runs the release it was published with; set `image.tag` for another. The panel starts the workspace image of its own version, so there is only the one image to choose.

The first account you create becomes the instance administrator. After that, accounts are created through invite links unless `signups` is `true`.

### Upgrading

```sh
helm upgrade avsys oci://ghcr.io/gt-drehmal/charts/avsys -n avsys --reset-then-reuse-values --version 0.2.0
```

`--reset-then-reuse-values` keeps the values you set and takes the new release's defaults for the rest. Without Helm, apply the newer `deploy/install.yaml` with your changes carried over.

To move an install made from `deploy/install.yaml` to Helm, mark its objects as the release's and then install the chart with values that match what you changed:

```sh
for o in serviceaccount/avsys service/avsys deployment/avsys pvc/avsys-data pvc/avsys-backups clusterrole/avsys clusterrolebinding/avsys; do
  kubectl -n avsys label "$o" app.kubernetes.io/managed-by=Helm --overwrite
  kubectl -n avsys annotate "$o" meta.helm.sh/release-name=avsys meta.helm.sh/release-namespace=avsys --overwrite
done
```

### Configuration

The chart's values are in [`deploy/chart/values.yaml`](deploy/chart/values.yaml), each with what it does. The ones most installs set:

| Value | Purpose |
| --- | --- |
| `publicURL` | Address people open the panel at. Sign-in callbacks are built from it. |
| `expose.mode` | How the three ports are reached: `LoadBalancer` (the default), `NodePort`, or `hostPort` on one node. See [Clusters without a load balancer](#clusters-without-a-load-balancer). |
| `router.domain` | A domain with a wildcard DNS record pointing at the router. New servers default to `<server>.<org>.<domain>`. |
| `extraPorts.range`, `extraPorts.annotations` | Public ports, as `25600-25699`, for servers' extra ports (voice chat, Geyser, maps) on the router's address, and how the load balancer is told to share it. |
| `signIn.github.clientId`, `signIn.github.clientSecret` | Enable GitHub sign-in. Likewise `discord` and `google`. Secrets can instead come from a Secret of your own, `signIn.existingSecret`. The callback URL to register is `<publicURL>/api/v1/auth/<provider>/callback`. |
| `signups` | `true` lets anyone create an account. |
| `persistence.data`, `persistence.backups` | Size and storage class of the database and snapshot volumes, or a claim of your own. |
| `offsite.repository`, `offsite.existingSecret` | A second place every organization's snapshots are copied to, and the Secret with the password that opens them there. See [Offsite copy](#offsite-copy). |
| `sshPublicHost` | Hostname shown in SSH instructions, if not the panel's. |
| `curseforge.apiKey` | Passed to servers for CurseForge downloads. |

The panel itself takes flags, each of which can also be set as an environment variable: `--public-url` is `AVSYS_PUBLIC_URL`. Run `avsys --help` for the full list; the chart sets them from its values, and `extraEnv` passes any other.

### Offsite copy

Snapshots live on the cluster's own storage. To keep a copy somewhere else, give the panel a place and a password:

```sh
kubectl -n avsys create secret generic avsys-offsite --from-literal=password=... \
  --from-literal=access-key-id=... --from-literal=secret-access-key=...
```

```yaml
offsite:
  repository: s3:https://s3.us-west-004.backblazeb2.com/my-bucket/avsys
  existingSecret: avsys-offsite
  secretEnv:
    AWS_ACCESS_KEY_ID: access-key-id
    AWS_SECRET_ACCESS_KEY: secret-access-key
```

- **The place** is a restic repository location. Each organization gets its own repository under it, named after the organization; write `{org}` where the name should go if the end is not the right spot, as in `b2:my-bucket:avsys/{org}`. The credentials are the environment variables restic itself reads for that kind of place. The bucket has to exist already. Locations that go over ssh (`sftp:`) do not work: the panel's image has no ssh client.
- **The password** is yours to choose and to keep. It is not the one the panel keeps beside its own repositories, because that one is lost with them. Without it the copy cannot be opened by anyone, so put it in a password manager as well as in the Secret.
- **What is copied.** Everything: scheduled snapshots, ones taken by hand, the ones taken before a restore or a delete, and those of deleted servers. A snapshot removed here, by retention or by someone, is removed there with the next copy. Nothing is removed there merely because it is missing here, so a cluster that loses its storage, or a fresh install, leaves the copy whole.
- **How it stands** is on each organization's **Backups** page: up to date, waiting, or failing and why. A failed copy is tried again every hour and whenever there is something new.
- **A deleted organization** keeps its copy. The panel removes its own repository and leaves the offsite one for you to remove.

**Getting snapshots back.** On a new install, or after the backups volume was lost: set the same place and password, create the organization under the same short name, and open its **Backups** page as an instance administrator. It shows what the copy holds, with a button to bring it all back. From there any snapshot can be [made into a server](#snapshots) again. The copy is also an ordinary restic repository, so `restic -r <place>/<organization> snapshots` and `restic restore` work on it without the panel.

### Which node a server runs on

A game server's speed is mostly the speed of one CPU core, which Kubernetes does not know. So avSYS places servers itself: a new server goes to the **highest-scoring node that has enough unreserved memory**, and falls down the ranking only when the better nodes are full. The choice is recorded, shown on the server page, and permanent on node-local storage, where the world lives on that node's disk.

```sh
./hack/rank-nodes.sh       # measures every node and sets the avsys.dev/score label
kubectl label node pi4-1 avsys.dev/game-servers=false   # optional: never host here
```

Nodes without a score can still host, after every node that has one. "Room" means memory not yet requested by other pods; a server reserves its whole limit while it runs and releases it while stopped or asleep. A server with its own `nodeSelector` is left to the scheduler.

### Clusters without a load balancer

The panel's ports are exposed by a `LoadBalancer` service. If your cluster has none (k3s started with ServiceLB disabled and no MetalLB, for example), that service never gets an address. Pin the panel to one node and open its ports there instead:

```yaml
expose:
  mode: hostPort
  node: my-node        # its kubernetes.io/hostname label
  hostPorts:
    http: 8080
    ssh: 2222
    minecraft: 25565
```

Or open them on every node with `expose.mode: NodePort` and `expose.nodePorts`.

### Addresses

For the shared router, point each server's hostname (an A or CNAME record) at the avSYS service's external IP. Servers that need their own IP can use exposure type LoadBalancer instead; that works with k3s ServiceLB and MetalLB.

## Roles

| Role | Can |
| --- | --- |
| Viewer | See servers, status, console output and snapshots |
| Operator | Also start and stop servers, run commands, SSH in, use the terminal and files, take snapshots |
| Admin | Also create, configure and delete servers; restore and delete snapshots, and make servers from them; set the snapshot schedule; manage members and invites |
| Owner | Also manage other owners and delete the organization |

Instance administrators act as owner in every organization and are the only ones who can create organizations.

### Who can join, and who is an operator

A new server made in the panel is for its organization: the whitelist is on, and every member who has linked a Minecraft account under **Account** is on it. Choose "Anyone with the address" on the form for a server with no whitelist.

Members are also made operators in game, from the operator role up. Neither needs doing by hand, and both keep up: when a server starts, and within seconds of someone linking an account, joining, leaving, or changing role.

- **Each server sets both**, under **Settings → Players**: who is whitelisted by membership (every member, a role and above, or nobody) and who is an operator by role (operators and above by default).
- **Someone taken off the whitelist is put out**, not only kept out: servers made this way also set the game's `enforce-whitelist`.
- **Players you add yourself stay.** Friends who are not in the panel go in a server's Whitelisted players list, and extra operators in its Operators list. The panel only ever takes off players it put on.
- To keep someone an operator whatever their role, list them under Operators: opping in game a player the panel already made one changes nothing it can see.
- **A linked account is not proof of owning it.** For operators that does not matter: a member with the operator role can already op anyone from the console. For the whitelist it means each member has one place to give, to the one account they link, which is usually their own and need not be.
- Which players the panel put on is kept with a server's snapshots, so restoring an old one does not hand a place or operator back to someone who has since left.
- A linked account is followed by its UUID: if it is renamed, the panel picks up the new name within a few hours.
- Servers made through the API are as before unless asked: no whitelist, and `autoWhitelist` off.
## Pictures

- **A server's icon** is the one the game shows players in their server list: `server-icon.png` in the server's files. Upload any picture under the server's **Settings** and the panel crops it square and writes it at the 64 pixels the game wants. One put there by hand shows in the panel too. Players see a new icon after the server's next restart.
- **An organization's picture** is set in its settings by an owner, and **your own** under Account. A picture that came with a GitHub, Discord or Google sign-in is kept aside while one you uploaded stands in for it, and comes back if you remove yours.
- Phone photos come out the right way up.
- Where there is none, a tile with the first letter stands in.
- Whatever is uploaded is redrawn by the panel as a small PNG before it is stored or served, so what a browser is given is never the file someone sent. PNG, JPEG and GIF up to 4 MB are accepted.

## API

The web UI is a client of the REST API, so anything it does can be scripted. Create a token under **Account → API tokens**:

```sh
export AVSYS=https://panel.example.com/api/v1
export TOKEN=avsys_...

curl -H "Authorization: Bearer $TOKEN" $AVSYS/orgs/friends/servers
curl -H "Authorization: Bearer $TOKEN" -X POST $AVSYS/orgs/friends/servers/creative/restart
# Run a command and get what it printed. Add "as":"server" to type it at the
# server console instead: players then see it come from [Server] rather than
# [Rcon], and what it prints goes to the log.
curl -H "Authorization: Bearer $TOKEN" -d '{"command":"list"}' $AVSYS/orgs/friends/servers/creative/console
curl -H "Authorization: Bearer $TOKEN" -d '{"command":"say hello","as":"server"}' $AVSYS/orgs/friends/servers/creative/console
curl -H "Authorization: Bearer $TOKEN" -X PATCH -d '{"spec":{"resources":{"memory":"6Gi"}}}' $AVSYS/orgs/friends/servers/creative

# Take a snapshot and wait for it; then put the server back to it.
curl -H "Authorization: Bearer $TOKEN" -X POST "$AVSYS/orgs/friends/servers/creative/snapshots?wait=true"
curl -H "Authorization: Bearer $TOKEN" -X POST "$AVSYS/orgs/friends/servers/creative/snapshots/<id>/restore?wait=true"
curl -H "Authorization: Bearer $TOKEN" $AVSYS/orgs/friends/snapshots
# One snapshot: the server as it was (version, mods, settings), and whether the schedule keeps it and why.
curl -H "Authorization: Bearer $TOKEN" $AVSYS/orgs/friends/snapshots/<id>
# Delete one snapshot, or several in one pass: all of them, or none if any is unknown or in use.
curl -H "Authorization: Bearer $TOKEN" -X DELETE $AVSYS/orgs/friends/snapshots/<id>
curl -H "Authorization: Bearer $TOKEN" -X DELETE -d '{"ids":["<id>","<id>"]}' $AVSYS/orgs/friends/snapshots

# The organization's daily snapshots: read the policy, change it, or run it now.
# "minute" is the time of day they begin, in minutes after midnight UTC.
curl -H "Authorization: Bearer $TOKEN" $AVSYS/orgs/friends/backup-policy
curl -H "Authorization: Bearer $TOKEN" -X PUT -d '{"enabled":true,"minute":540,"keep":{"daily":7,"weekly":4,"monthly":12,"yearly":2}}' $AVSYS/orgs/friends/backup-policy
curl -H "Authorization: Bearer $TOKEN" -X POST "$AVSYS/orgs/friends/backup-policy/run?wait=true"
# The offsite copy: how it stands, and copy now.
curl -H "Authorization: Bearer $TOKEN" $AVSYS/orgs/friends/offsite
curl -H "Authorization: Bearer $TOKEN" -X POST "$AVSYS/orgs/friends/offsite/run?wait=true"
# One server's own: any of enabled, minute and keep; what is left out follows the organization. null goes back to it.
curl -H "Authorization: Bearer $TOKEN" -X PATCH -d '{"spec":{"backups":{"keep":{"daily":30,"weekly":8,"monthly":24,"yearly":5}}}}' $AVSYS/orgs/friends/servers/survival
curl -H "Authorization: Bearer $TOKEN" -X PATCH -d '{"spec":{"backups":null}}' $AVSYS/orgs/friends/servers/survival

# Make a new server out of a snapshot. Whatever the spec leaves out is as the snapshot has it.
curl -H "Authorization: Bearer $TOKEN" -d '{"name":"creative-copy","fromSnapshot":"<id>","spec":{"state":"Stopped"}}' "$AVSYS/orgs/friends/servers?wait=true"
# A copy of a server as it is now.
curl -H "Authorization: Bearer $TOKEN" -d '{"name":"creative-2","fromServer":"creative"}' "$AVSYS/orgs/friends/servers?wait=true"
# A copy that differs: on another game version, with game settings laid over the original's
# (null stops the panel setting one, which leaves it as the original's server.properties has it).
# Mods and the modpack always come from the original.
curl -H "Authorization: Bearer $TOKEN" -d '{"name":"creative-next","fromServer":"creative","spec":{"version":"1.21.5","properties":{"difficulty":"peaceful","pvp":null},"autoWhitelist":"off"}}' "$AVSYS/orgs/friends/servers?wait=true"
```

Without `?wait=true` these answer at once and the work continues in the background; progress arrives on the event stream.

A server's `spec` is the same in the API and in the Kubernetes resource, so GitOps tools can manage servers directly as `MinecraftServer` manifests.

## Inside a server

```console
$ ssh creative.friends@panel.example.com -p 2222
steve@creative:/data$ mc console     # the server console: its log, and a line to type on
steve@creative:/data$ mc run list    # one command, with its answer
There are 3 of a max of 20 players online: ...
steve@creative:/data$ mc restart     # restart the game, keeping this shell
```

To move an existing world in, stop the server and copy over it:

```sh
rsync -av -e 'ssh -p 2222' ./world/ creative.friends@panel.example.com:world/
```

## Development

Needs Go, [Bun](https://bun.sh), Docker, [restic](https://restic.net) and a cluster in your kubectl context (OrbStack's works well). Without restic the panel runs, with snapshots switched off.

```sh
make dev        # builds everything and runs the panel on http://localhost:8080
make test       # unit tests, static checks, type checks and lint
make test-snapshots  # the snapshot engine against a real workspace container
make test-browser    # the web UI in Chromium and WebKit, against a panel with nothing behind it
make e2e        # creates a real server through a running panel and checks it end to end
make e2e-cluster  # the same from nothing: an empty k3s in Docker, installed with the chart
make chart      # after changing deploy/chart: deploy/install.yaml and the chart's test renders
make generate   # after changing api/
```

`make e2e` needs `AVSYS_URL` and `AVSYS_TOKEN` (an instance admin's API token). It makes a throwaway organization and a Paper server with plugins, checks the console, files, snapshots and restore, SSH and the router against it, and deletes both. Unit tests cover the logic that needs no cluster; this covers what only a running server can show.

`make test-browser` needs neither a cluster nor the internet. It starts `cmd/avsys-uitest`, which is the real API and web UI with stand-ins for everything they reach out to: servers are remembered but never run, and what the panel looks up at Modrinth, Mojang and the build sites is answered from `web/tests/upstream`. The tests in `web/tests` open every page, use the dialogs and forms, and check what each role is shown, in both engines, and fail on any error thrown in the page. Fetch the browsers once with `cd web && bunx playwright install`. A change to the web UI should come with a test here.

`make e2e-cluster` needs only Docker. It starts an empty single-node k3s in a container, builds both images, installs the chart, creates the first account and then runs the same check. CI runs it on every push, and images are published only if it passes.

The API tests in `internal/api` run the panel in-process and check every route against every role. A new route under an organization has to be added to that table, or the tests fail.

To cut a release, push a version tag on main: `git tag v0.1.0 origin/main && git push origin v0.1.0`. CI tests it, publishes both images under that tag and the chart as version `0.1.0`, and makes a GitHub release with notes from the pull requests merged since the last one, the install and upgrade commands, and that version's `install.yaml`. A tag such as `v0.2.0-rc.1` makes a prerelease.

`make dev` runs the panel on your machine against the current kubectl context, reaching pods through the API server, and keeps its database in `.dev/`. For UI work, also run `bun run dev` in `web/` and open the address Vite prints; it proxies API calls to the panel.

| Path | What is there |
| --- | --- |
| `api/v1alpha1` | The `MinecraftServer` resource |
| `internal/controller` | Reconciler, pod rendering, player monitor |
| `internal/api` | HTTP API, live events, console, terminal, files |
| `internal/sshgw` | SSH gateway |
| `internal/router` | Hostname router |
| `internal/store` | Users, organizations, invites (SQLite) |
| `internal/backup` | Snapshot repositories, snapshot and restore |
| `images/workspace` | The SSH sidecar image |
| `web` | The React UI |
| `deploy` | CRD, Helm chart and install manifest |

Earlier planning notes: <https://docs.google.com/document/d/1D_mDTFApseszykIvamCIFB8B9JzJdk8vqC-D3iDWjWQ/edit?usp=sharing>

## License

Apache-2.0. See [LICENSE](LICENSE).
