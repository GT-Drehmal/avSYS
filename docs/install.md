# Install and operate

How to put avSYS on a cluster, set it up, keep it current, and fix it when
something is wrong. For what the panel does once it runs, see the
[user guide](user-guide.md); for how it is built, [architecture](architecture.md).

- [What you need](#what-you-need)
- [Install with Helm](#install-with-helm)
- [Install with kubectl alone](#install-with-kubectl-alone)
- [First sign-in](#first-sign-in)
- [Reaching the panel, SSH and the router](#reaching-the-panel-ssh-and-the-router)
- [Sign-in providers](#sign-in-providers)
- [Passwords off](#passwords-off)
- [CurseForge](#curseforge)
- [Snapshots and the offsite copy](#snapshots-and-the-offsite-copy)
- [Which node a server runs on](#which-node-a-server-runs-on)
- [Updating](#updating)
- [Every setting](#every-setting)
- [Where data lives, and backing up the panel](#where-data-lives-and-backing-up-the-panel)
- [Troubleshooting](#troubleshooting)

## What you need

- A Kubernetes cluster. k3s works well; so does any other with a default
  storage class.
- A way to give the panel's Service an address: a load balancer (k3s
  ServiceLB, MetalLB, a cloud's), or node ports, or one node's own ports.
  See [Reaching the panel](#reaching-the-panel-ssh-and-the-router).
- Optionally, the [metrics API](https://github.com/kubernetes-sigs/metrics-server)
  (k3s has it), for the memory and CPU graphs on server pages and the Nodes
  page. Without it the graphs are empty and everything else works.
- Optionally, a domain whose wildcard record you can point at the router,
  so new servers get an address of their own without anyone touching DNS.

Images are published to `ghcr.io/gt-drehmal/avsys` and
`ghcr.io/gt-drehmal/avsys-workspace` for amd64 and arm64. Releases are
`:v1.2.3`, with `:latest` on the newest; `:edge` follows the main branch.
The panel starts the workspace image of its own version next to each
server, so there is only one image to choose.

## Install with Helm

```sh
helm install avsys oci://ghcr.io/gt-drehmal/charts/avsys -n avsys --create-namespace \
  --set publicURL=https://panel.example.com
```

`publicURL` is the address people open the panel at. Sign-in callbacks,
invite and reset links, and the host shown in SSH instructions are built
from it, so set it to the real address before anyone signs in. When it
starts with `https://`, the session cookie is marked secure.

This makes:

- the `avsys` namespace, with one Deployment running the panel
  (`replicas: 1`, `Recreate`: the database is a single file);
- a service account, a ClusterRole for what the panel manages, and a Role
  that lets it change its own Deployment and nothing else of that kind (see
  [Self-update](#updating-from-the-panel));
- a 1Gi volume for the database (`avsys-data`) and a 50Gi volume for
  snapshots (`avsys-backups`). Size the second for the worlds you host;
- one Service for the three ports: http (the web UI and API), ssh (the SSH
  gateway) and minecraft (the router). A `LoadBalancer` unless you choose
  otherwise.

The chart is published with each
[release](https://github.com/GT-Drehmal/avSYS/releases), whose notes have
the commands for that version and its own `install.yaml`. A chart runs the
release it was published with; set `image.tag` for another. To install
from a checkout, use `./deploy/chart` in place of the `oci://` address.

The panel installs and upgrades the `MinecraftServer` CRD itself each time
it starts (`installCRDs`), so upgrading the panel upgrades the CRD. Helm
installs the chart's copy of the CRD only on the first install.

Uninstalling keeps both volumes, so removing the chart never removes the
worlds' snapshots. Delete them by hand if you mean to.

## Install with kubectl alone

`deploy/install.yaml` is the chart rendered with its default values. Set
`AVSYS_PUBLIC_URL` in it to the address you will open the panel at, then:

```sh
kubectl apply -f deploy/install.yaml
```

It makes the same objects as the chart. To change anything else, edit the
Deployment's environment (every setting is an `AVSYS_` variable: see
[Every setting](#every-setting)), or move to Helm:

```sh
for o in serviceaccount/avsys service/avsys deployment/avsys pvc/avsys-data pvc/avsys-backups clusterrole/avsys clusterrolebinding/avsys; do
  kubectl -n avsys label "$o" app.kubernetes.io/managed-by=Helm --overwrite
  kubectl -n avsys annotate "$o" meta.helm.sh/release-name=avsys meta.helm.sh/release-namespace=avsys --overwrite
done
helm install avsys oci://ghcr.io/gt-drehmal/charts/avsys -n avsys -f my-values.yaml
```

with values that match what you changed.

## First sign-in

Open `publicURL` and create an account. The first account becomes the
**instance administrator**: it can make organizations, see every one as an
owner, and use the **Nodes** and **Instance settings** pages. After that, accounts are made from
invite links, unless `signups` is `true`, which lets anyone make one.

The administrator sets up two-factor first, before anything else (an API
token included): a new panel asks it of admins and owners, and always of
its administrator. To install with another level, set `signIn.twoFactor`
(`nobody`, `admins`, `operators` or `everyone`; `AVSYS_TWO_FACTOR` without
the chart). It is used until the first account is made; from then on, the
choice in **Instance settings** is the one that counts, and changing the
value later does nothing. A script that installs a panel and makes its
first account and API token at once, as a test cluster does, installs with
`signIn.twoFactor=nobody`.

With [passwords off](#passwords-off) there is no form to fill in. The
panel's log has a setup link instead:

```sh
kubectl -n avsys logs deploy/avsys | grep setup=
```

Whoever opens it and signs in through a provider becomes the administrator.

## Reaching the panel, SSH and the router

The panel listens on three ports:

| Port in the pod | What | Who needs it |
| --- | --- | --- |
| 8080 (`http`) | Web UI and API | Members, in a browser. Players' games too, for resource packs uploaded to servers, unless those are served from a [listener of their own](#resource-packs-on-a-separate-address). |
| 2222 (`ssh`) | SSH gateway: SSH, SFTP, scp, rsync, VS Code Remote | Members with the operator role and above |
| 25565 (`minecraft`) | The router, which sends players to the server their address names | Players |

Nothing else needs to be reachable from outside. Game servers, their
workspaces and RCON are reached by the panel inside the cluster.

The panel speaks plain HTTP. For HTTPS, put the http port behind an
ingress or a reverse proxy (or `tailscale serve`, Caddy, and the like), set
`publicURL` to the `https://` address, and list the proxy in
`trustedProxies`: see [Behind a reverse proxy](#behind-a-reverse-proxy).
SSH and the router are plain TCP and go around the proxy.

### Behind a reverse proxy

Behind a proxy, every request reaches the panel from the proxy's address.
`trustedProxies` (`--trusted-proxies`, `AVSYS_TRUSTED_PROXIES`, addresses
and ranges separated by commas) names the proxies. A request from one of
them is taken to come from the address in its `X-Forwarded-For`: the
right-most entry that is not itself a trusted proxy, or `X-Real-IP` when
there is no `X-Forwarded-For`. That address is what the limits on wrong
passwords count, and what **Account → Sessions** shows. Headers from any
other address are ignored; with `trustedProxies` empty, the default, none
are read. List only proxies you run: whoever connects from a listed address
can claim any address.

List the address the panel sees the proxy connect from. Until
`trustedProxies` is set, **Account → Sessions** shows it.

- `tailscale serve` on the panel's machine, to `http://127.0.0.1:8080`:
  `127.0.0.1`. With the chart's `hostPort` on k3s, such a connection can
  arrive from the node's pod network gateway instead (as `10.42.0.1`).
- A proxy on another machine: that machine's address as the panel sees it,
  such as its Tailscale address (`tailscale ip -4` there).

For example, Caddy on a public machine, reaching the panel over Tailscale:

```text
avsys.example.com {
	# The panel's machine, by its Tailscale name or address.
	reverse_proxy http://panel-host:8080 {
		header_up Host {host}
	}
}
```

```yaml
publicURL: https://avsys.example.com
# Caddy's Tailscale address, and 127.0.0.1 if tailscale serve stays too.
trustedProxies: [100.101.102.103, 127.0.0.1]
```

Caddy puts the client's address in `X-Forwarded-For` and drops what the
client sent there. Point it at the panel's port itself, not at a
`tailscale serve` address: `tailscale serve` replaces `X-Forwarded-For`
with the address of whoever reached it, Caddy's, and the client's is lost.
The proxy must pass the `Host` the browser sent (Caddy does;
`header_up Host {host}` says so): the terminal checks it against the page's
address. Whether the session cookie is `Secure` follows `publicURL`, not
the proxy's headers.

SSH (2222) and the router (25565) are plain TCP, not HTTP. Forward them
with a TCP proxy of their own (Caddy's layer4 module, nginx `stream`, or a
port forward) and set `sshPublicHost` to match, or leave SSH reachable only
on the tailnet.

A panel open to the internet should keep **Require two-factor for** at
**Admins and owners** (the default) or higher, in **Instance settings**.
It holds for every way of signing in, providers too.

### `expose.mode`

**LoadBalancer** (the default): one Service with an address of its own,
from the cluster's load balancer. Its ports are `expose.ports` (http 80,
ssh 2222, minecraft 25565). Find the address with
`kubectl -n avsys get service avsys`. `expose.annotations`,
`expose.loadBalancerIP`, `expose.loadBalancerClass` and
`expose.externalTrafficPolicy` are passed to the Service
(`externalTrafficPolicy: Local` keeps players' own addresses visible).

**NodePort**: the three ports on every node, at `expose.nodePorts`.
`ssh` and `minecraft` must be given, since they are shown in addresses.

**hostPort**: the three ports opened on one node, which the panel is pinned
to. For a cluster with no load balancer (k3s started with ServiceLB
disabled and no MetalLB, for example), where a `LoadBalancer` Service never
gets an address:

```yaml
expose:
  mode: hostPort
  node: my-node        # its kubernetes.io/hostname label
  hostPorts:
    http: 8080
    ssh: 2222
    minecraft: 25565
```

The SSH and game ports shown in the panel follow the mode: the node port
or host port where there is one, the Service's port otherwise.

### Game addresses and DNS

Every server that uses the shared router has a hostname. Players connect
to that hostname, on the router's port, and the router sends them to the
server whose hostname it is. Point each hostname (an A record, or a CNAME)
at the router's address.

Easier: set `router.domain` to a domain with a wildcard record pointing at
the router, such as `*.mc.example.org`. New servers then default to
`<server>.<org>.mc.example.org` and need no DNS of their own.

Where several organizations share an install, the instance administrator
can give an organization domains of its own on its settings page. A
hostname equal to or under one of them can then only be used by that
organization's servers. A hostname under no given domain is anyone's, so
an install with one organization needs none of this.

A server can instead have its own address: exposure **LoadBalancer** gives
it a Service of its own (works with k3s ServiceLB and MetalLB), and
**NodePort** a node port.

### Extra ports

Some mods and plugins need ports of their own: voice chat, Geyser for
Bedrock players, web maps. A server that shares the router's address gets
these as ports on the same address, from a range you set aside:

```yaml
extraPorts:
  range: 25600-25699
  annotations:
    metallb.universe.tf/allow-shared-ip: avsys
expose:
  annotations:
    metallb.universe.tf/allow-shared-ip: avsys
```

Each such server gets a `LoadBalancer` Service of its own for its extra
ports, taking ports from the range. `extraPorts.annotations` go on those
Services: they are how the load balancer is told to put them on the
router's address (MetalLB's `allow-shared-ip` above, with the same key on
the panel's own Service). k3s ServiceLB shares an address by itself, as
long as the ports do not collide. The range must not include the router's
own port. Without a range, the cluster gives each extra port a node port.

While a server's game is not running, the panel answers on its extra ports
itself: a sleeping server wakes when one of them is reached, as it does
when a player joins through the router.

### Resource packs on a separate address

Resource packs uploaded to servers are served to players' games from the
panel's own address, at `/packs/<sha1>.zip`, with no sign-in. If players
cannot reach the panel (it is on a private network, say), serve the packs
from a listener of their own:

```yaml
packs:
  listener:
    enabled: true
    port: 8081         # the Service's port
  url: http://packs.example.org:8081
```

That listener serves nothing but packs. `packs.cacheMiB` (default 512) is
the most room kept packs may take in the data volume; past it the least
recently served are let go of and read again from their servers' files
when asked for.

## Sign-in providers

People can sign in with GitHub, Discord, Google, or an OpenID Connect
provider of your own, beside or instead of passwords. For each one,
register an application with the provider, with this callback address:

```text
<publicURL>/api/v1/auth/<provider>/callback
```

where `<provider>` is `github`, `discord`, `google` or `oidc`. Then give
the panel the client ID and secret:

```yaml
signIn:
  github:
    clientId: Iv1.0123456789abcdef
    clientSecret: ...
```

Likewise `discord` and `google`. Instead of putting secrets in values, make
a Secret with the keys `github-client-secret`, `discord-client-secret`,
`google-client-secret` and `oidc-client-secret` (only those you use) and
name it in `signIn.existingSecret`; the client IDs stay in values.

### Your own OpenID Connect provider

Authentik, Keycloak, Authelia, Google Workspace, Entra ID and others:

```yaml
signIn:
  oidc:
    issuer: https://auth.example.org/application/o/avsys/
    clientId: avsys
    clientSecret: ...
    name: Club login          # the button's label (default SSO)
    scopes: openid profile email
    allowedGroups: [minecraft]
    groupsClaim: groups
```

- Give the issuer exactly as the provider names it.
- `allowedGroups`: a person must be in one of these to sign in, read from
  the claim `groupsClaim` names (default `groups`) in the ID token or user
  info. Empty lets in anyone the provider signs in.
- Accounts are linked by the provider's subject, never by email.
- The panel looks the provider up when it starts. If it cannot be reached
  then, the log says so and it is tried again at the next sign-in.

Someone who already has an account links a provider from their
**Account** page; after that they can sign in either way.

## Passwords off

```yaml
passwords: false
```

Everyone signs in through a provider, and no password can be used or set.
Two-factor still works: a code after the provider, set up with a recent
sign-in in place of a password, and **Require two-factor for** in
**Instance settings** holds as with passwords on. Reset links only turn
two-factor off; one that would set a password is refused with
`passwords_off`. Passwords already set are kept, for if it is turned back
on.

- The chart refuses `passwords: false` with no provider in `signIn`.
- When the panel starts, it warns in its log about each administrator who
  has no linked account at a configured provider, and it does not start if
  no administrator could sign in.
- With no account yet, the log has the [setup link](#first-sign-in).

## CurseForge

Mods, plugins, datapacks, resource packs and modpacks from CurseForge need
a CurseForge API key (get one at
[console.curseforge.com](https://console.curseforge.com)). Without one,
CurseForge's tab says how it is turned on.

The simplest way: the instance administrator enters it in **Instance
settings**, under **CurseForge**. The panel checks it with CurseForge and keeps
it in its database; it is never shown again, only that one is set, with
**Replace** and **Remove**.

Or give it with the install, as the key `api-key` of a Secret:

```sh
kubectl -n avsys create secret generic avsys-curseforge --from-literal=api-key=...
```

The chart reads the Secret `avsys-curseforge` by default if it is there
(from the panel's next start). `curseforge.existingSecret` names another
Secret with the key `api-key`, and `curseforge.apiKey` puts the key in the
chart's own Secret. Under the hood it is `AVSYS_CURSEFORGE_API_KEY`. A key
entered in the panel goes before one from the install.

Servers that install from CurseForge read the key from a Secret
`avsys-curseforge` in their organization's namespace, which the panel
writes when a server needs it and whenever the key changes.

## Snapshots and the offsite copy

Snapshots need nothing set up. Each organization has a
[restic](https://restic.net) repository on the backups volume
(`/backups/<org>`, with its password in `/backups/<org>.key`), and every
server is snapshotted once a day, at a time and with a retention each
organization can change. See [Backups](user-guide.md#backups-and-snapshots)
in the user guide.

The backups volume is the only copy until you give the panel a second place.

### Recently deleted

A snapshot someone deletes is kept, hidden, in **Recently deleted** for 7
days before it is removed for good, so that a mistaken or malicious delete
can be put back. Set how long in **Instance settings → Deleted snapshots**
(1 to 90 days, or **Not kept** to remove them at once, as before). A change
applies to what is there already, counted from when each was deleted. Only
the panel's administrator can remove them sooner, so keep two-factor on for
that account. Retention does not go through it: what the schedule lets go
of is removed at once.

Which snapshots are in it is noted in `/backups/<org>.bin`, beside the
repository. They are still in the repository, so plain restic lists them
as any other, and they use their storage until they are gone.

### Offsite copy

Any [restic repository location](https://restic.readthedocs.io/en/stable/030_preparing_a_new_repo.html)
that needs no ssh works: S3 and what speaks it (Backblaze B2, Cloudflare
R2, Wasabi), Azure, Google Cloud, or a
[rest-server](https://github.com/restic/rest-server).

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

- **The place.** Each organization gets its own repository under it, named
  after the organization. Write `{org}` where the name should go if the end
  is not the right spot, as in `b2:my-bucket:avsys/{org}`. The bucket has to
  exist already. `sftp:` does not work: the panel's image has no ssh
  client.
- **Credentials** are the environment variables restic itself reads for
  that kind of place, taken from keys of `existingSecret` through
  `secretEnv`.
- **The password** is yours to choose and to keep: the key `password` of
  `existingSecret`, or `offsite.password` in values. It is not the one the
  panel keeps beside its own repositories, because that one is lost with
  them. Put it in a password manager as well as the Secret. The panel does
  not start with a repository and no password.
- **What is copied**: everything, a couple of minutes after it changes and
  once after each start. A snapshot in
  [Recently deleted](#recently-deleted) is still copied and kept there, so
  that it can be put back: the offsite copy never removes a deleted
  snapshot sooner than Recently deleted does. A snapshot removed here, by
  retention or once it has left Recently deleted, is removed there with the
  next copy. Nothing is removed there
  merely because it is missing here, so a cluster that loses its storage,
  or a fresh install, leaves the copy whole. A failed copy is tried again
  every hour and whenever there is something new.
- **How it stands** is on each organization's **Backups** page. A deleted
  organization's copy is left for you to remove.

**Getting snapshots back**, on a new install or after the backups volume
was lost: set the same place and password, create the organization under
the same short name, and open its **Backups** page as the instance
administrator. It shows what the copy holds, with a button to bring it all
back. The copy is also an ordinary restic repository:

```sh
restic -r s3:https://.../avsys/friends snapshots
restic -r s3:https://.../avsys/friends restore <id>:/data --target ./restored
```

The panel's own repositories can be read the same way from the backups
volume:

```sh
restic -r /backups/friends --password-file /backups/friends.key snapshots
```

## Which node a server runs on

A game server's speed is mostly the speed of one CPU core, which Kubernetes
does not know. So avSYS places servers itself: a new server goes to the
highest-scoring node that has enough memory not yet requested by other pods,
and falls down the ranking only when the better nodes are full. The choice
is recorded and shown on the server's page. On node-local storage it is
permanent, since the world lives on that node's disk, until the server is
[moved](user-guide.md#nodes-and-moving-servers).

The score is the node label `avsys.dev/score`. The instance administrator
sets it for every node with **Measure speed** on the **Nodes** page;
`./hack/rank-nodes.sh` does the same from a shell, and the label can be set
by hand. A node whose **New servers on \<node\>** switch is off takes new
servers only when someone picks it; that is the label
`avsys.dev/game-servers=false`
(`kubectl label node pi4-1 avsys.dev/game-servers=false`). Nodes without a
score still host, after every node that has one. A server reserves its
whole memory limit while it runs, and frees it while stopped or asleep. A
server with a `nodeSelector` of its own is left to the scheduler.

## Updating

### With Helm

```sh
helm upgrade avsys oci://ghcr.io/gt-drehmal/charts/avsys -n avsys --reset-then-reuse-values --version 0.2.0
```

`--reset-then-reuse-values` keeps the values you set and takes the new
release's defaults for the rest. Without Helm, apply the newer release's
`install.yaml` with your changes carried over.

### Updating from the panel

When a newer release is out, **Instance settings** says so to the instance
administrator, with what changed and an **Update** button (the **Nodes**
page points there). Pressing it has
the panel set its own Deployment to the new release's image. The cluster
restarts the panel on it: players connected through the router drop for
that moment, servers keep running, and the page reloads once the new
version answers.

- **Install fixes by itself**, in Instance settings too, has the panel move to a
  newer patch release of its version (`v0.2.1` from `v0.2.0`) by itself,
  once nobody is playing. Minor and major releases wait for the button.
- The panel asks GitHub for the newest release every six hours. Set
  `updateCheck: false` for a panel that should not reach the internet;
  that also stops fixes being installed by themselves. Builds of a commit
  (`:edge`, `make dev`) never ask.
- Where the panel cannot change its Deployment, the page shows the command
  that upgrades it as it was installed.

**RBAC.** `selfUpdate: true` (the default) gives the panel's service
account a Role in its own namespace that may `get` and `patch` the one
Deployment named after the release, and nothing else of its kind. Set
`selfUpdate: false` to update only with `helm upgrade`.

**After updating from the panel, a later `helm upgrade` must name the
release the panel now runs** with `--version`, or it moves the panel back
to the chart's own release. With Helm 4's server-side apply it also needs
`--force-conflicts`, since the panel was the last to set the image:

```sh
helm upgrade avsys oci://ghcr.io/gt-drehmal/charts/avsys -n avsys --reset-then-reuse-values \
  --version 0.2.1 --force-conflicts
```

Likewise, a later `kubectl apply` should use that release's `install.yaml`.

### What an upgrade does

The panel's pod is replaced (the database is on a ReadWriteOnce volume, so
the old pod stops before the new one starts). The panel upgrades the CRD
as it starts. Game servers keep running; only connections through the
router and the SSH gateway drop for the moment the panel is away. After
upgrading from a version without snapshots, a server has to be restarted
once before it can be snapshotted; its page says so.

## Every setting

The panel takes flags, and each flag can also be given as an environment
variable: `--public-url` is `AVSYS_PUBLIC_URL`. The chart sets them from
its values; `extraEnv` passes any other. `avsys --help` lists them.

### Chart values

| Value | Default | What it does |
| --- | --- | --- |
| `publicURL` | `https://panel.example.com` | Address people open the panel at. |
| `image.repository` | `ghcr.io/gt-drehmal/avsys` | The panel's image. |
| `image.tag` | the chart's release | Another release to run. |
| `image.pullPolicy` | `Always` for `:edge` and `:latest`, `IfNotPresent` for a release | |
| `imagePullSecrets` | `[]` | For a registry that needs signing in to. |
| `workspaceImage` | `ghcr.io/gt-drehmal/avsys-workspace` of the panel's version | The workspace started next to each server. |
| `expose.mode` | `LoadBalancer` | `LoadBalancer`, `NodePort` or `hostPort`. See [`expose.mode`](#exposemode). |
| `expose.ports.http` / `.ssh` / `.minecraft` | `80` / `2222` / `25565` | The Service's ports; with `LoadBalancer`, the public ones. |
| `expose.nodePorts.http` / `.ssh` / `.minecraft` | none | Node ports, with `NodePort`. ssh and minecraft are required. |
| `expose.node` | none | The node to open ports on, with `hostPort`. |
| `expose.hostPorts.http` / `.ssh` / `.minecraft` | `8080` / `2222` / `25565` | Ports opened on that node. |
| `expose.annotations` | `{}` | Annotations of the `LoadBalancer` Service. |
| `expose.loadBalancerIP`, `expose.loadBalancerClass` | none | Passed to the Service. |
| `expose.externalTrafficPolicy` | none | `Local` keeps players' addresses visible. |
| `trustedProxies` | `[]` | Reverse proxies in front of the panel, as `[127.0.0.1, 100.64.0.0/10]`. See [Behind a reverse proxy](#behind-a-reverse-proxy). |
| `sshPublicHost` | host of `publicURL` | Hostname shown in SSH instructions. |
| `router.domain` | none | Wildcard domain for new servers' addresses. |
| `extraPorts.range` | none | Public ports for servers' [extra ports](#extra-ports), as `25600-25699`. |
| `extraPorts.annotations` | `{}` | Put on the extra ports' Services. |
| `packs.listener.enabled` | `false` | Serve resource packs from a listener of their own. Needs `packs.url`. |
| `packs.listener.port` / `.nodePort` / `.hostPort` | `8081` / none / `8081` | That listener's Service, node and host port. |
| `packs.url` | `publicURL` | Address players' games fetch packs from. |
| `packs.cacheMiB` | `512` | Most MiB kept packs may take; `0` for no limit. |
| `signIn.twoFactor` | `admins` | Who has to use two-factor, until the first account is made: `nobody`, `admins`, `operators` or `everyone`. See [First sign-in](#first-sign-in). |
| `signIn.existingSecret` | none | Secret with the providers' client secrets. |
| `signIn.github` / `.discord` / `.google` `.clientId`, `.clientSecret` | none | [Sign-in providers](#sign-in-providers). |
| `signIn.oidc.issuer`, `.clientId`, `.clientSecret` | none | [Your own OpenID Connect provider](#your-own-openid-connect-provider). |
| `signIn.oidc.name` | `SSO` | The sign-in button's label. |
| `signIn.oidc.scopes` | `openid profile email` | Scopes asked for. |
| `signIn.oidc.allowedGroups` | `[]` | Groups one of which a person must be in. |
| `signIn.oidc.groupsClaim` | `groups` | Claim that lists a person's groups. |
| `signups` | `false` | `true` lets anyone make an account. |
| `passwords` | `true` | `false` turns password sign-in off. See [Passwords off](#passwords-off). |
| `curseforge.apiKey`, `curseforge.existingSecret` | none | [CurseForge](#curseforge) key, or a Secret with it as `api-key`. With neither, the Secret `avsys-curseforge` if there is one. |
| `persistence.data.size` / `.storageClass` / `.existingClaim` | `1Gi` / default class / none | The database volume. |
| `persistence.backups.size` / `.storageClass` / `.existingClaim` | `50Gi` / default class / none | The snapshots volume. |
| `offsite.repository` | none | Where snapshots are copied. See [Offsite copy](#offsite-copy). |
| `offsite.existingSecret` | none | Secret with the password as `password`, and credentials. |
| `offsite.password` | none | Or the password itself, kept in the chart's Secret. |
| `offsite.secretEnv` | `{}` | Environment variables for restic, from keys of `existingSecret`. |
| `namespacePrefix` | `mc-` | Prefix of each organization's namespace. |
| `installCRDs` | `true` | Install and upgrade the CRD at each start. Off only if you manage it yourself. |
| `updateCheck` | `true` | Ask GitHub every few hours for a newer release. |
| `selfUpdate` | `true` | Let the panel change its own Deployment's image. |
| `extraEnv` | `[]` | More environment variables for the panel. |
| `resources` | requests 50m CPU, 128Mi; limit 1Gi memory | Pruning a large snapshot repository is what needs the memory. |
| `nodeSelector`, `tolerations`, `affinity` | none | Where the panel's pod may run. |
| `podAnnotations`, `podLabels` | none | Added to the panel's pod. |
| `serviceAccount.create` / `.name` / `.annotations` | `true` / the release's name / none | |
| `rbac.create` | `true` | The ClusterRole and its binding. |
| `nameOverride`, `fullnameOverride` | none | Names of the chart's objects. |

The chart refuses values that cannot work: an unknown `expose.mode`;
`NodePort` without the ssh and minecraft node ports; `hostPort` without
a node; a provider with a secret and no client ID, or a client ID and no
secret; an OIDC issuer without a client ID or the reverse; `passwords:
false` with no provider; an offsite repository with no password; and
`packs.listener` without `packs.url`.

### Flags and environment variables

| Flag | Environment | Default | What it does |
| --- | --- | --- | --- |
| `--public-url` | `AVSYS_PUBLIC_URL` | `http://localhost:8080` | Address people open the panel at. |
| `--trusted-proxies` | `AVSYS_TRUSTED_PROXIES` | none | Reverse proxies in front of the panel: addresses and ranges, separated by commas. See [Behind a reverse proxy](#behind-a-reverse-proxy). |
| `--data-dir` | `AVSYS_DATA_DIR` | `./data` (`/data` in the image) | The database, kept resource packs, and by default snapshots. |
| `--backup-dir` | `AVSYS_BACKUP_DIR` | `<data-dir>/backups` (`/backups` in the chart) | Organizations' snapshot repositories. |
| `--restic` | `AVSYS_RESTIC` | `restic` | restic binary. Without one, snapshots are off. |
| `--offsite-repository` | `AVSYS_OFFSITE_REPOSITORY` | none | Where every organization's snapshots are copied. |
| `--offsite-password-file` | `AVSYS_OFFSITE_PASSWORD_FILE` | none | File with the offsite password. Or set `AVSYS_OFFSITE_PASSWORD`. |
| `--http-addr` | `AVSYS_HTTP_ADDR` | `:8080` | Where the web UI and API listen. |
| `--ssh-addr` | `AVSYS_SSH_ADDR` | `:2222` | Where the SSH gateway listens. Empty turns it off. |
| `--router-addr` | `AVSYS_ROUTER_ADDR` | `:25565` | Where the router listens. Empty turns it off. |
| `--router-public-port` | `AVSYS_ROUTER_PUBLIC_PORT` | `25565` | Port players reach the router on. |
| `--router-domain` | `AVSYS_ROUTER_DOMAIN` | none | Wildcard domain for new servers' addresses. |
| `--ssh-public-host` | `AVSYS_SSH_PUBLIC_HOST` | host of `--public-url` | Hostname shown in SSH instructions. |
| `--ssh-public-port` | `AVSYS_SSH_PUBLIC_PORT` | port of `--ssh-addr` | Port shown in SSH instructions. |
| `--extra-ports` | `AVSYS_EXTRA_PORTS` | none | Range of public ports for servers' extra ports. |
| `--extra-ports-annotations` | `AVSYS_EXTRA_PORTS_ANNOTATIONS` | none | `key=value,...` put on the extra ports' Services. |
| `--pod-ip` | `AVSYS_POD_IP` | none | The panel's own address in the cluster. Extra ports are pointed here while a game is not running; empty, nothing answers on them then. The chart sets it. |
| `--pack-addr` | `AVSYS_PACK_ADDR` | none | A listener that serves only resource packs. |
| `--pack-url` | `AVSYS_PACK_URL` | `--public-url` | Address players fetch packs from. |
| `--pack-cache` | `AVSYS_PACK_CACHE` | `512` | Most MiB kept packs may take; `0` for no limit. |
| `--namespace-prefix` | `AVSYS_NAMESPACE_PREFIX` | `mc-` | Prefix of each organization's namespace. |
| `--workspace-image` | `AVSYS_WORKSPACE_IMAGE` | `ghcr.io/gt-drehmal/avsys-workspace:<version>` | Workspace image. |
| `--workspace-image-pull-policy` | `AVSYS_WORKSPACE_IMAGE_PULL_POLICY` | Kubernetes' own | |
| `--web-dir` | `AVSYS_WEB_DIR` | none | Serve the web UI from a directory, for development. |
| `--pod-dial` | `AVSYS_POD_DIAL` | `auto` | How to reach pods: `direct`, `forward` (through the API server), or `auto` (direct inside a cluster). |
| `--allow-signups` | `AVSYS_ALLOW_SIGNUPS` | `false` | `true` lets anyone make an account. |
| `--passwords` | `AVSYS_PASSWORDS` | `true` | `false` turns password sign-in off. |
| `--two-factor` | `AVSYS_TWO_FACTOR` | `admins` | Who has to use two-factor, until the first account is made: `nobody`, `admins`, `operators` or `everyone`. |
| `--install-crds` | `AVSYS_INSTALL_CRDS` | `true` | Install and upgrade the CRD at start. |
| `--update-check` | `AVSYS_UPDATE_CHECK` | `true` | Ask GitHub for newer releases, and allow fixes to install by themselves. |
| `--oidc-issuer` | `AVSYS_OIDC_ISSUER` | none | OpenID Connect issuer. |
| `--oidc-client-id` | `AVSYS_OIDC_CLIENT_ID` | none | |
| `--oidc-client-secret-file` | `AVSYS_OIDC_CLIENT_SECRET_FILE` | none | File with the client secret. Or set `AVSYS_OIDC_CLIENT_SECRET`. |
| `--oidc-name` | `AVSYS_OIDC_NAME` | `SSO` | The sign-in button's label. |
| `--oidc-scopes` | `AVSYS_OIDC_SCOPES` | `openid profile email` | |
| `--oidc-allowed-groups` | `AVSYS_OIDC_ALLOWED_GROUPS` | none | Comma-separated. |
| `--oidc-groups-claim` | `AVSYS_OIDC_GROUPS_CLAIM` | `groups` | |

For the yes-or-no ones given as environment variables: `AVSYS_ALLOW_SIGNUPS`
is on only for `true`; `AVSYS_INSTALL_CRDS` and `AVSYS_UPDATE_CHECK` are off
for anything but `true`; `AVSYS_PASSWORDS` is off only for `false`.

Environment variables with no flag:

| Environment | What it does |
| --- | --- |
| `AVSYS_GITHUB_CLIENT_ID`, `AVSYS_GITHUB_CLIENT_SECRET` | GitHub sign-in. Both or neither. |
| `AVSYS_DISCORD_CLIENT_ID`, `AVSYS_DISCORD_CLIENT_SECRET` | Discord sign-in. |
| `AVSYS_GOOGLE_CLIENT_ID`, `AVSYS_GOOGLE_CLIENT_SECRET` | Google sign-in. |
| `AVSYS_OIDC_CLIENT_SECRET` | OpenID Connect client secret, where no file is given. |
| `AVSYS_OFFSITE_PASSWORD` | Offsite password, where no file is given. |
| `AVSYS_CURSEFORGE_API_KEY` | CurseForge API key. One entered in the panel goes first. |
| `AVSYS_ROUTER_SERVICE` | Name of the panel's own Service, in its namespace (default `avsys`). The chart sets it. |
| `AVSYS_DEPLOYMENT` | Name of the panel's own Deployment, for self-update (default: the Service's name). |

## Where data lives, and backing up the panel

| What | Where |
| --- | --- |
| Servers: their settings | `MinecraftServer` resources in each organization's namespace (`mc-<org>`) |
| Servers: their files | A volume per server in that namespace |
| People, organizations, roles, invites, sessions, API tokens, SSH keys, linked Minecraft accounts, saved player lists, templates and the world library's records, domains, panel settings (such as the CurseForge key) | The SQLite database, `avsys.db` on the data volume |
| Uploaded resource packs being served | `packs/` on the data volume (they can be read again from servers' files) |
| Snapshots | One restic repository per organization on the backups volume |
| The SSH host key and the key the gateway uses to reach workspaces | The database |

Snapshots cover servers. The database is not in them, and the offsite copy
does not hold it. To keep the panel itself, back up the data volume with
whatever backs up your cluster's volumes, or copy the database out by hand.
The panel's image has no shell, so stop the panel and read the volume from
a pod of your own:

```sh
kubectl -n avsys scale deploy/avsys --replicas=0
kubectl -n avsys run copy-db --image=busybox --restart=Never \
  --overrides='{"spec":{"containers":[{"name":"copy-db","image":"busybox","command":["sleep","600"],"volumeMounts":[{"name":"d","mountPath":"/data"}]}],"volumes":[{"name":"d","persistentVolumeClaim":{"claimName":"avsys-data"}}]}}'
kubectl -n avsys wait --for=condition=Ready pod/copy-db
kubectl -n avsys cp copy-db:/data/avsys.db ./avsys.db
kubectl -n avsys delete pod copy-db
kubectl -n avsys scale deploy/avsys --replicas=1
```

Game servers keep running while the panel is stopped; players already in a
game through the router are disconnected, and sleeping servers cannot be
woken until the panel is back.

The SSH host key is in the database, so a panel restored from it keeps the
fingerprint people's SSH clients know.

## Troubleshooting

**The panel's pod does not start.** Read its log:
`kubectl -n avsys logs deploy/avsys`. Settings that are wrong say so there
and stop the panel: an offsite repository without a password, a provider
with only one of its ID and secret, an OIDC client without an issuer, an
`--extra-ports` range that includes the router's port, or passwords off
with no administrator who could sign in.

**The Service has no external address.** The cluster has no load balancer.
Use `expose.mode: hostPort` or `NodePort`.

**Sign-in through a provider fails.** The callback registered with the
provider must be exactly `<publicURL>/api/v1/auth/<provider>/callback`, and
`publicURL` must be the address in the browser. For OIDC, the issuer must
be exactly as the provider names it, and someone outside `allowedGroups` is
turned away.

**"Too many attempts."** Eight wrong passwords for one username from one
address within ten minutes hold that pair off until the oldest is ten
minutes old; two-factor codes are held the same way per account. The
address is the connection's own: behind a reverse proxy every attempt comes
from the proxy, so the limit counts per username for everyone, unless the
proxy is listed in `trustedProxies` (see
[Behind a reverse proxy](#behind-a-reverse-proxy)).

**Someone forgot their password.** The instance administrator makes a
one-time reset link for them from an organization's **Members** page. It
works once, for a day, and can also turn their two-factor off. With
passwords off there is no password to reset: people sign in through their
provider.

**Someone lost their two-factor device.** They sign in with one of their
recovery codes. Without those, the instance administrator makes them a
reset link that turns two-factor off; for an account with no password, or
with passwords off, that is all the link does.

**A player cannot join.** Check that the server's hostname resolves to the
router's address and that the player uses the router's port. A player whose
address names no server is told "Unknown server: Nothing is hosted at" that
address, which means the hostname they typed is not any server's. A server set to "Members of the organization"
only lets in players on its whitelist: see
[Players](user-guide.md#players).

**A server will not start.** Its page shows the reason and the log. The
workspace keeps running while the game is stopped, so the files can still
be fixed from **Files**, the terminal or SSH.

**Snapshots say they are unavailable.** The panel did not find restic. The
published image has it; a panel run another way needs it on the `PATH` or
named with `--restic`.

**SSH says the host key changed** after reinstalling without the old
database. The panel made a new host key. Remove the old one from
`~/.ssh/known_hosts`.

**A server's graphs are empty.** The cluster has no metrics API.
