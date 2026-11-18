# API reference

The web UI is a client of this API, so anything it does can be scripted.
This page lists every route the panel serves. A test
(`internal/api/docs_test.go`) fails when a route is served without a
heading here.

- [Conventions](#conventions)
- [Sign-in and the public routes](#sign-in-and-the-public-routes)
- [Your account](#your-account)
- [The instance](#the-instance)
- [Organizations](#organizations)
- [Snapshots and backups](#snapshots-and-backups)
- [Templates](#templates)
- [Uploads](#uploads)
- [Saved player lists](#saved-player-lists)
- [World library](#world-library)
- [Servers](#servers)
- [A server's content](#a-servers-content)
- [A server's console, logs and players](#a-servers-console-logs-and-players)
- [A server's snapshots and operations](#a-servers-snapshots-and-operations)
- [A server's terminal, picture and resource pack](#a-servers-terminal-picture-and-resource-pack)
- [A server's files](#a-servers-files)
- [Live events](#live-events)
- [Catalog](#catalog)
- [Outside the API](#outside-the-api)

## Conventions

### Base address

Every route below is under `/api/v1`: `GET /me` is
`https://panel.example.com/api/v1/me`.

### Signing in

Two ways, both as the person they belong to:

- **An API token**, for scripts. Make one under **Account → API tokens**
  (or with [`POST /me/tokens`](#post-metokens)) and send it as
  `Authorization: Bearer avsys_...`. A token carries everything its owner
  may do. The panel keeps only a hash of it; it is shown once.
- **A session cookie**, `avsys_session`, which the browser gets from
  signing in. It lasts 30 days from sign-in, is `HttpOnly` and
  `SameSite=Lax`, and is `Secure` when the panel's address is `https://`.

A request with a token is never also read as a cookie request.

### The CSRF header

A request that is not `GET` or `HEAD` and has no API token must carry the
header `X-Avsys` (any value). Without it the answer is
`403 {"code":"csrf"}`. A browser cannot add a custom header to a request
from another site without asking first, which the panel never allows, so
this keeps other sites from acting with someone's cookie. Requests with a
token do not need it.

### Who may call a route

Each route says who may call it:

- **Public**: anyone, signed in or not.
- **Signed in**: any account.
- **Instance admin**: only the instance administrator.
- **Viewer**, **Operator**, **Admin**, **Owner**: that role or above in the
  organization in the path. The instance administrator counts as an owner
  in organizations they are not a member of. Someone outside the organization gets `404`, never `403`, so
  they cannot tell it exists. A member below the role gets
  `403 {"code":"forbidden"}`.

A route that needs a sign-in answers `401 {"code":"unauthorized"}` without
one. An instance administrator signed in with a password, when two-factor
is required of administrators and they have not set it up, gets
`403 {"code":"two_factor_required"}` from everything but `GET /me` and the
two routes that set it up.

### Requests and answers

- Request bodies are JSON unless a route says it takes a file, in which
  case the body is the file's bytes. JSON bodies are at most 1 MiB.
- Answers are JSON unless said otherwise.
- A failure is `{"code": "...", "message": "..."}` with a matching status.
  `message` is a sentence meant for a person. Common codes: `invalid`
  (400), `unauthorized` (401), `forbidden` (403), `not_found` (404),
  `conflict` (409), `rate_limited` (429), `internal` (500). Routes list
  others where they matter.
- A server, in answers and in [events](#live-events), is
  `{org, name, createdAt, revision, generation, java: {game, effective}, spec, status, unfinished?}`.
  `spec` is the `MinecraftServer` resource's spec, the same as in
  Kubernetes, so GitOps tools can manage servers as manifests. See
  [The server resource](#the-server-resource).

### Long work: `?wait=true`

Snapshots, restores, copies, moves, deletes and imports run in the
background. The request that starts one answers at once, and progress
arrives on the [event stream](#live-events) as `operation.progress` and
`operation.done`, and in [`GET .../operation`](#get-orgsorgserversserveroperation).
Routes that say so take `?wait=true`, which holds the answer until the work
is done and answers with how it ended.

```sh
export AVSYS=https://panel.example.com/api/v1
export TOKEN=avsys_...
curl -H "Authorization: Bearer $TOKEN" -X POST "$AVSYS/orgs/friends/servers/creative/snapshots?wait=true"
```

### Rate limits

Wrong passwords are counted per address and username: eight within ten
minutes hold that pair off (`429 rate_limited`) until the oldest is ten
minutes old. Two-factor codes are counted the same way per account, from
anywhere. The address is the connection's own. Only from a proxy the panel
is set to trust (see
[Behind a reverse proxy](install.md#behind-a-reverse-proxy)) is it read
from `X-Forwarded-For` instead; forwarded-for headers from anyone else are
not read.

## Sign-in and the public routes

### `GET /meta`

What the sign-in page needs. **Public.**

Returns `{version, setupRequired, allowSignups, providers: [{id, name}], passwords, ssh: {host, port}, snapshots, curseforge, accent}`.
`setupRequired` is true while there is no account; `passwords` false when
password sign-in is off; `snapshots` whether restic is there; `curseforge`
whether a CurseForge key is set; `accent` the panel's accent, for anyone
who has not picked one.

### `POST /auth/register`

Make an account with a password, and sign in. **Public**, and allowed only
for the first account (which becomes the instance administrator), with a
usable invite, or when sign-ups are open.

Body: `{username, password, displayName?, invite?}`. `username` is 2–32 of
`a-z 0-9 _ -`, starting with a letter or digit; `password` at least 8
characters.

Returns `201` with the user `{id, username, displayName, email, avatarUrl, isAdmin, createdAt}` and sets the session cookie.
Errors: `passwords_off` (403), `signup_closed` (403), `invite_unusable`
(410), `conflict` (409, the name is taken).

### `POST /auth/login`

Sign in with a password. **Public.**

Body: `{username, password}`. Returns `200` with the user and sets the
session cookie. If the account has two-factor, it returns
`{"twoFactor": true}` instead, with a short-lived challenge cookie, and the
sign-in finishes at [`POST /auth/login/code`](#post-authlogincode).
Errors: `bad_credentials` (401), `rate_limited` (429), `passwords_off` (403).

### `POST /auth/logout`

Sign this browser out. **Public.** Returns `204`.

### `GET /auth/{provider}/start`

Begin signing in through a provider: `github`, `discord`, `google` or
`oidc`. **Public.** A browser route: it redirects to the provider.

Query: `next` (a path on the panel to return to), `invite` (an invite
token to accept on the way), `setup` (the setup code from the panel's log,
when passwords are off and there is no account yet). An unknown provider is
`404`; other failures redirect to `/login?error=...`.

### `GET /auth/{provider}/callback`

Where the provider sends the browser back. **Public.** Register
`<publicURL>/api/v1/auth/<provider>/callback` with the provider.

- An account already linked to this provider account is signed in.
- Someone signed in already has the provider account linked to theirs.
- Otherwise a new account is made, under the same rules as
  `POST /auth/register`.

It redirects to `next`, or to `/login?error=...` on failure. Accounts are
matched by the provider's own ID for the person, never by email.

### `POST /auth/login/code`

The second step of a sign-in with two-factor. **Public**, with the challenge
cookie from `POST /auth/login`.

Body: `{code}`, a code from the authenticator app or a recovery code.
Returns `200` with the user and sets the session cookie. Errors:
`wrong_code` (400), `sign_in_again` (401: the challenge ran out after five
minutes or five wrong codes), `rate_limited` (429), `passwords_off` (403).

### `GET /reset-links/{token}`

Whose a reset link is. **Public.** Returns `{username, displayName}`, or
`404` for a link that was used or ran out. `passwords_off` (403) with
passwords off.

### `POST /reset-links/{token}`

Set a new password with a reset link. **Public.** Body: `{password}`.
Signs the account out everywhere and, if the link says so, turns its
two-factor off. Returns `{username}`.

### `GET /invites/{token}`

What an invite is for. **Public.** Returns `{org: {name, slug}, role, usable}`.

### `POST /invites/{token}/accept`

Join an organization with an invite. **Signed in.** Returns the
organization. `invite_unusable` (410) for one that was used up or ran out.

## Your account

All of these are **signed in**, and act on the caller's own account.

### `GET /me`

Who you are. Returns `{user, orgs, identities, hasPassword, look, twoFactor, recoveryCodes, mustSetUpTwoFactor, adminTwoFactor, minecraftAccounts, minecraft?}`.
`orgs` is `[{id, slug, name, createdAt, hasIcon, role}]` (every
organization, as owner, for the instance administrator); `identities` the
linked providers `[{provider, login}]`; `look` is `{theme, accent}` (see
[`PATCH /me/look`](#patch-melook)); `recoveryCodes` how many are left;
`minecraftAccounts` `[{uuid, name, linkedAt, main, verified}]`.

### `PATCH /me`

Body: `{displayName}` (up to 64 characters). Returns the user.

### `PATCH /me/look`

How the panel looks to you. Body: any of `{theme, accent}`. `theme` is
`auto` (as the system), `light` or `dark`; `accent` is `amethyst`, `gold`,
`teal`, `blue`, `rose`, or `""` for the panel's. Only what is given
changes. Returns `{theme, accent}`. An unknown value is a `400`.

### `POST /me/password`

Set or change your password. Body: `{current, new}`; `current` is not
asked of an account with no password yet. Signs out your other sessions.
Returns `204`. Errors: `wrong_password` (400), `rate_limited`,
`passwords_off`.

### `PUT /me/minecraft`

Link a Minecraft account by name (looked up at Mojang). Body: `{name}`.
Returns `{uuid, name, linkedAt, main, verified}`. Errors:
`no_such_player` (404), `upstream` (502), `invalid` (more than 10 linked).

### `DELETE /me/minecraft`

Unlink your Minecraft account. Returns `204`.

### `POST /me/minecraft-accounts`

Link another Minecraft account. Body: `{name, main}`. Returns all your
linked accounts.

### `DELETE /me/minecraft-accounts/{uuid}`

Unlink one. Returns the accounts left.

### `POST /me/minecraft-accounts/{uuid}/main`

Make one your main account. Returns your accounts.

### `POST /me/minecraft-accounts/{uuid}/verify`

Prove a linked account is yours. With no body, opens a check and returns
`{name, until, address}`: join `address` in the game as that account before
`until` to get a code. Then send `{code}` to finish; returns
`{name, verified: true}`. Errors: `no_check` (409), `wrong_code` (400).

### `PUT /me/avatar`

Set your picture. Body: a PNG, JPEG or GIF of up to 4 MB. Returns `204`.
`too_large` (413).

### `DELETE /me/avatar`

Remove your picture; one from a sign-in provider comes back. Returns `204`.

### `GET /users/{user}/avatar`

Someone's uploaded picture, as PNG. **Signed in.** `404` if they have none.

### `GET /me/ssh-keys`

Your SSH keys: `[{id, name, publicKey, fingerprint, createdAt, lastUsedAt}]`.

### `POST /me/ssh-keys`

Add one. Body: `{name, publicKey}` (one `authorized_keys` line). Returns
`201` with the key. `conflict` (409) if the key is on an account already.

### `POST /me/ssh-keys/import`

Add the keys a GitHub account publishes. Body: `{githubUsername}`. Returns
`{added, keys}`.

### `DELETE /me/ssh-keys/{id}`

Remove one. Returns `204`.

### `GET /me/sessions`

Where you are signed in: `[{id, userAgent, address, method, createdAt, lastUsedAt, expiresAt, current}]`.
`method` is `password` or `provider`.

### `DELETE /me/sessions`

Sign out everywhere else. With an API token, which is no session, that is
every session. Returns `204`.

### `DELETE /me/sessions/{id}`

Sign one session out. Returns `204`.

### `POST /me/two-factor/setup`

Begin setting up two-factor. Body: `{password}`. Returns
`{secret, url, qr}` for the authenticator app. Errors: `no_password`,
`wrong_password` (400), `conflict` (409, already on), `passwords_off`.

### `POST /me/two-factor`

Finish setting it up. Body: `{code}` from the app. Returns
`{recoveryCodes}`: ten one-time codes, shown this once. Errors:
`no_setup` (409), `wrong_code` (400).

### `POST /me/two-factor/off`

Turn it off. Body: `{password}`. Returns `204`. `required` (409) for an
administrator while two-factor is required of administrators.

### `POST /me/two-factor/recovery-codes`

Replace your recovery codes. Body: `{password}`. Returns `{recoveryCodes}`.

### `GET /me/tokens`

Your API tokens: `[{id, name, createdAt, lastUsedAt}]`.

### `POST /me/tokens`

Make one. Body: `{name}` (1–64 characters). Returns `201`
`{token: {id, name, createdAt, lastUsedAt}, secret}`. The secret is shown
only here.

### `DELETE /me/tokens/{id}`

Revoke one. Returns `204`.

## The instance

### `PUT /settings/admin-two-factor`

Require two-factor of administrators who sign in with a password.
**Instance admin.** Body: `{required}`. Returns `204`. `passwords_off`
with passwords off.

### `POST /users/{user}/reset-link`

Make a one-time link for someone to set a new password with.
**Instance admin.** Body: `{twoFactorOff}`, true to also turn their
two-factor off. Returns `201 {url, expiresAt}`. The link works once, for a
day, and replaces any earlier one. `passwords_off` with passwords off.

### `GET /nodes`

The cluster's nodes. **Signed in**; the instance administrator sees more.

Query: `memory` (as `4Gi`), to have each node say whether a server of that
size would be placed there automatically.

Returns `{nodes: [{name, ready, cordoned?, automatic, hosts, score, memory, free, cores, inUse?}]}`.
`automatic` is whether the node takes new servers. For the instance
administrator, each node also has `players` and
`servers: [{org, name, memory, phase?, players?}]`, and the answer has
`measuring` and `measured`.

### `GET /panel`

The panel itself, for Instance settings. **Instance admin.** Returns
`{panel: {version, canUpdate, autoUpdate, updating?, curseforge, accent}, update?}`,
where `update` is a newer release: `{version, notes, command?, manifest?}`.

### `GET /nodes/usage`

Recent memory and CPU of each node. **Instance admin.** Returns
`{available, every, nodes: {<node>: [samples]}}`.

### `PATCH /nodes/{node}`

Whether a node takes new servers. **Instance admin.** Body: `{automatic}`.
Returns `204`.

### `POST /nodes/measure`

Measure every node's speed and set its score. **Instance admin.** Returns
`202 {measuring: true}`; the result shows in `GET /nodes`. `busy` (409)
while a measurement runs.

### `PATCH /panel`

The panel's own settings. **Instance admin.**

Body: any of `{autoUpdate, curseforgeKey, accent}`. `autoUpdate` has the
panel install fix releases by itself. `curseforgeKey` sets the CurseForge
API key (checked with CurseForge first), and `""` removes the one entered
in the panel. `accent` is the accent of everyone who has not picked their
own: `amethyst` (the default), `gold`, `teal`, `blue` or `rose`. Returns
`{version, canUpdate, autoUpdate, updating?, curseforge, accent}`, where
`curseforge` is `panel`, `install` or `""` for where the key comes from.
The key itself is never returned. An empty body is a `400`.

### `POST /panel/update`

Move the panel to the newest release. **Instance admin.** Body:
`{version?}`, which if given must be the newest release. Returns `202`
with the panel as above. Errors: `no_update` (409), `cannot_update` (409:
the panel may not change its Deployment).

### `POST /orgs`

Make an organization. **Instance admin.** Body: `{name, slug}`; `slug` is
the short name in addresses and the namespace, up to 32 of `a-z 0-9 -`.
Returns `201` with the organization. `conflict` (409) for a slug in use.

## Organizations

An organization is named in paths by its short name (`slug`), as
`/orgs/friends`.

### `GET /orgs/{org}`

**Viewer.** Returns `{id, slug, name, createdAt, hasIcon, role}`, `role`
being yours.

### `PATCH /orgs/{org}`

Rename it. **Owner.** Body: `{name}` (1–64 characters). Returns the
organization.

### `DELETE /orgs/{org}`

Delete it, with its namespace and its snapshots. **Owner.** It must have no
servers left (`409 not_empty`). If it has snapshots, add
`?snapshots=delete` to say they go too (`409 has_snapshots` without it).
An offsite copy is left alone. Returns `204`.

### `GET /orgs/{org}/members`

**Viewer.** Returns
`[{userId, username, displayName, avatarUrl, role, joinedAt, minecraftName?, minecraftAccounts}]`.

### `PATCH /orgs/{org}/members/{user}`

Change someone's role. **Admin**; only an owner can make an owner or change
one, and the last owner cannot step down (`409 last_owner`). Body:
`{role}`: `viewer`, `operator`, `admin` or `owner`. Returns `204`.

### `DELETE /orgs/{org}/members/{user}`

Remove someone. **Admin**, or anyone for themselves (leaving). Only an
owner can remove an owner, and the last owner cannot leave. Returns `204`.

### `GET /orgs/{org}/invites`

**Admin.** Returns `[{token, role, createdBy, createdAt, expiresAt, maxUses, uses}]`;
`maxUses` 0 is no limit.

### `POST /orgs/{org}/invites`

Make an invite link. **Admin**, for a role no higher than their own. Body:
`{role, expiresInHours?, maxUses?}` (0 for none). Returns `201` with the
invite; the link is `<publicURL>/invite/<token>`.

### `DELETE /orgs/{org}/invites/{token}`

Revoke one. **Admin.** Returns `204`.

### `GET /orgs/{org}/icon`

The organization's picture, as a 128-pixel PNG. **Viewer.** `404` if none.

### `PUT /orgs/{org}/icon`

Set it. **Owner.** Body: a PNG, JPEG or GIF of up to 4 MB. Returns `204`.

### `DELETE /orgs/{org}/icon`

Remove it. **Owner.** Returns `204`.

### `GET /orgs/{org}/domains`

Domains given to the organization: hostnames at or under them are its
servers' alone. **Viewer.** Returns `{domains, editable}`.

### `PUT /orgs/{org}/domains`

Set them. **Instance admin.** Body: `{domains: [...]}`; `*.example.org` is
taken as `example.org`. Returns `{domains, editable, inUse}`, where `inUse`
lists other organizations' servers already under one (they keep working).
`domain_claimed` (409) for a domain another organization has.

## Snapshots and backups

A snapshot is `{id, time, server, trigger, by?, version?, loader?, size, added, files, offsite?}`.
`trigger` is how it came to be: `manual`, `scheduled`, `pre-restore`,
`pre-replace`, `final` (before a delete), `copy`, `move` or `template`.
Where an `id` is taken, an unambiguous prefix of at least 8 characters
will do. All of these answer `503 backups_unavailable` where the panel has
no restic.

### `GET /orgs/{org}/backup-policy`

The organization's daily snapshots. **Viewer.** Returns
`{enabled, minute, keep: {daily, weekly, monthly, yearly}, next?}`.
`minute` is when they begin, in minutes after midnight UTC. The default is
on, at 540 (09:00 UTC, 05:00 US Eastern in summer), keeping 7 daily, 4
weekly, 12 monthly and 2 yearly.

### `PUT /orgs/{org}/backup-policy`

Change it. **Admin.** Body: the whole policy, as above. `keep.daily` is
1–90, `weekly` 0–104, `monthly` 0–120, `yearly` 0–20. Returns the policy.

### `POST /orgs/{org}/backup-policy/run`

Take the scheduled snapshots now, even with the schedule off; servers that
switched their own off are passed over. **Admin.** Returns
`202 {servers}`; with `?wait=true`,
`{results: [{server, outcome, snapshot?, reason?, removed?}]}`, where
`outcome` is `snapshot`, `unchanged`, `skipped` or `failed`. `busy` (409)
while a run is going.

### `GET /orgs/{org}/offsite`

How the offsite copy stands. **Viewer.** Returns
`{configured, lastAttempt?, lastSuccess?, error?, snapshots, pending, running}`.

### `POST /orgs/{org}/offsite/run`

Copy now. **Admin.** Returns `202` with the status; with `?wait=true`,
`{copied, removed, snapshots}`, or `502 offsite_failed`. `no_offsite`
(503) where no offsite place is set.

### `GET /orgs/{org}/offsite/snapshots`

What the offsite copy holds that could be brought back. **Instance admin.**
Returns `{snapshots}`.

### `POST /orgs/{org}/offsite/fetch`

Bring the offsite copy back, into an organization that has no snapshots
here. **Instance admin.** Holds the answer until done. Returns
`{fetched}`. `has_snapshots` (409) if it has some;
`name_has_history` (409) if a server here now was made after the copy's
newest snapshot of that name.

### `GET /orgs/{org}/snapshots`

Every snapshot of the organization's servers, including deleted ones.
**Viewer.** Returns `{available, snapshots, operations, usage?: {bytes, snapshots}}`;
`operations` are those running now. Template and world library snapshots
are not listed.

### `GET /orgs/{org}/snapshots/{id}`

One snapshot, with the server as it was: the snapshot's fields plus `spec`
and `retention: {scheduled, kept?, paused?}`, which says whether the
schedule keeps it and why. **Viewer**; below admin the spec's `image`,
`env` and JVM flags are left out. `no_record` (422) for a snapshot with no
record of its settings.

### `GET /orgs/{org}/snapshots/{id}/icon`

The server's icon in that snapshot. **Viewer.**

### `DELETE /orgs/{org}/snapshots`

Delete several. **Admin.** Body: `{ids: [...]}` (1–500). All of them, or
none if any is unknown or in use. Returns `{deleted}`. `in_use` (409) for
one being restored or still needed by a server being made; `template`
(409) for one belonging to a template or the world library.

### `DELETE /orgs/{org}/snapshots/{id}`

Delete one. **Admin.** Returns `204`.

## Templates

A template is a server kept to make others from: its files and settings,
in versions, each a snapshot. A **settings-only** template has settings and
no files. A template can also have its own world, given to the servers made
from it.

A template is `{id, name, description, createdBy, createdAt, updatedAt, current, versions, settingsOnly, spec?, world?, ownWorld?, extraFlags?}`.
`current` is the version new servers get (`null`: always the newest);
`world` is `new` (servers made from it start a new world) or `own` (it has
its own world, described in `ownWorld`).

### `GET /orgs/{org}/templates`

**Viewer.** Returns the templates. Below admin, the spec and JVM flags are
left out.

### `POST /orgs/{org}/templates`

Make one. **Admin.** Body: `{name, description?}` and one source:

- `server`: save a server as it is now (a snapshot is taken). Returns
  `202` with the template and an `operation`; with `?wait=true`, `201`
  once it is saved.
- `snapshot` or `template`: from a snapshot, or a copy of another
  template's version. Returns `201`.
- `spec`: a settings-only template. Returns `201`.

With `server`, `snapshot` or `template`, `changes` is a partial spec laid
over what was saved. `conflict` (409) for a name in use.

### `GET /orgs/{org}/templates/{template}`

**Viewer.** Returns the template, with `spec` as its servers would get it.

### `PATCH /orgs/{org}/templates/{template}`

**Admin.** Body: any of `{name, description, current, spec, changes}`.
`current` is a version's ID (the newest means "always the newest"); `spec`
is for settings-only templates; `changes` for the others. Returns the
template.

### `DELETE /orgs/{org}/templates/{template}`

Delete it, with every version and its own world. **Admin.** `in_use` (409)
while a server is being made from it. Returns `204`.

### `POST /orgs/{org}/templates/{template}/versions`

Save a new version. **Admin.** Body: `{server}` or `{snapshot}`. As for
making a template, including `?wait=true`. Returns the template.

### `DELETE /orgs/{org}/templates/{template}/versions/{id}`

Delete a version. **Admin.** The last one cannot be (`409 last_version`).
Returns the template.

### `PUT /orgs/{org}/templates/{template}/world`

Give the template its own world, replacing any it had. **Admin.** One
source:

- the world as an archive in the body (up to 2 GB), or `?upload=<id>&size=<bytes>`
  for one sent [in parts](#uploads); with `root` (the folder in the archive
  that is the world) and optionally `name`, `version` (the game version it
  was last opened in) and `file`;
- `?library=<world id>`, a world from the library;
- `?from=<template id>`, another template's own world.

`?pack=true` also takes that world's resource pack. Returns the template.

### `DELETE /orgs/{org}/templates/{template}/world`

Remove its own world. **Admin.** Returns the template.

### `PATCH /orgs/{org}/templates/{template}/world`

Change its world's resource pack settings. **Admin.** Body:
`{resourcePack: null | {enforce?, prompt?}}`. Returns the template.

## Uploads

Large files are sent in parts, then used by another request. An upload is
named by an ID you choose (8–64 of `A-Z a-z 0-9 -`). Parts are up to
64 MB. An upload nobody adds to for a day is thrown away; one that is used
is thrown away after.

The routes that take an upload sent here, as `?upload=<id>&size=<bytes>`:
[`POST /orgs/{org}/worlds/upload`](#post-orgsorgworldsupload),
[`PUT /orgs/{org}/templates/{template}/world`](#put-orgsorgtemplatestemplateworld)
and [`PUT .../modpack`](#put-orgsorgserversservermodpack). `size` must be
the whole file's size. An archive to import into a server is sent in parts
to the server itself instead, with
[`PUT .../import/upload`](#put-orgsorgserversserverimportupload).

### `GET /orgs/{org}/uploads`

How much has arrived. **Operator.** Query: `upload`. Returns `{received}`.

### `PUT /orgs/{org}/uploads`

Send a part. **Operator.** Query: `upload`, `offset` (bytes sent so far;
0 starts over), `size?` (the whole file, to check there is room),
`length?` (this part's length, to notice one cut short). Body: the part.
Returns `{received}`. A part that is not the next one gets
`409 {"code":"offset","received":N}`.

### `DELETE /orgs/{org}/uploads`

Throw an upload away. **Operator.** Query: `upload`. Returns `204`.

## Saved player lists

Lists of players kept once and used on any number of servers, as their
whitelist or operators. A list is
`{id, name, createdBy?, updatedAt, players: [{uuid, name, kind, xuid?}], servers: [{name, displayName?, whitelist?, ops?}]}`.
`kind` is `java` or `bedrock`; `servers` are those using it.

### `GET /orgs/{org}/player-lists`

**Viewer.** Returns the lists.

### `POST /orgs/{org}/player-lists`

Make one. **Admin.** Body: `{name, names?: [Java names], bedrock?: [gamertags]}`,
or `{name, fromServer, list}` to copy a server's `whitelist` or `ops`.
Returns `201` with the list, plus `missing` and `missingBedrock`: names
that could not be found. Up to 1000 players a list and 200 lists.
`name_taken` (409).

### `GET /orgs/{org}/player-lists/{list}`

**Viewer.** Returns the list.

### `PATCH /orgs/{org}/player-lists/{list}`

Rename it. **Admin.** Body: `{name}`.

### `DELETE /orgs/{org}/player-lists/{list}`

Delete it, and take it off every server using it. **Admin.** Returns `204`.

### `POST /orgs/{org}/player-lists/{list}/players`

Add players. **Admin.** Body: `{names?, bedrock?}`. Returns the list with
`missing` and `missingBedrock`.

### `DELETE /orgs/{org}/player-lists/{list}/players/{uuid}`

Take one off. **Admin.** Returns the list.

## World library

Worlds kept to give to servers. A world is
`{id, name, description, createdBy, createdAt, updatedAt, root, version?, file?, size, unpacked?, source, saving?, ready, error?, kept?, unpacking?, unpackError?, resourcePack?}`.
`source` is `upload`, `server:<name>` or `snapshot:<server>`; `ready` is
false while it is being saved. `kept` is `folders` for a world kept as its
own files, whose `size` is the world's, or `archive` for one kept as the
archive it came in, as worlds were before; such a one says `unpacking`
while it is being kept as its own files instead, and `unpackError` if it
could not be, and is used as it is meanwhile.

### `GET /orgs/{org}/worlds`

**Viewer.** Returns the worlds.

### `POST /orgs/{org}/worlds`

Save a server's world into the library. **Operator.** Body:
`{name, description?, server | snapshot}`. From a server, a snapshot is
taken first. The world, its nether and its end are kept, with the server's
resource pack and icon. Returns `202` with the world (`saving: true`);
with `?wait=true`, `201` once saved.

### `POST /orgs/{org}/worlds/upload`

Add a world from an archive. **Operator.** Query: `name`, `description?`,
`root?` (the folder in the archive that is the world), `version?`,
`file?`. Body: the archive, a `.zip`, `.tar` or `.tar.gz` (up to 2 GB), or
`?upload=<id>&size=<bytes>`. The archive is unpacked and the world kept as
its own files: `400` if `root` has no `level.dat`, `507` if it does not fit
on the panel's disk. Returns `201` with the world, once it is kept.

### `GET /orgs/{org}/worlds/{world}`

**Viewer.** Returns the world.

### `PATCH /orgs/{org}/worlds/{world}`

**Operator.** Body: any of `{name, description, resourcePack}`.
`resourcePack` is `null` (remove it), `{url, sha1?, project?, enforce?, prompt?}`
(a pack from an address), or `{enforce?, prompt?}`. Returns the world.

### `DELETE /orgs/{org}/worlds/{world}`

**Admin.** Servers that were given it keep it. Returns `204`.

### `GET /orgs/{org}/worlds/{world}/archive`

Download it. **Operator.** A zip of a single-player save, made as it is
sent. One uploaded and still kept as an archive comes back as the file that
was uploaded.

### `GET /orgs/{org}/worlds/{world}/icon`

**Viewer.** A 64-pixel PNG.

### `PUT /orgs/{org}/worlds/{world}/icon`

**Operator.** Body: a PNG, JPEG or GIF of up to 4 MB. Returns `204`.

### `DELETE /orgs/{org}/worlds/{world}/icon`

**Operator.** Returns `204`.

### `PUT /orgs/{org}/worlds/{world}/resource-pack`

Give the world a resource pack. **Operator.** Body: a zip of up to 250 MB;
`?name=` its file name. Returns the world.

## Servers

A server is named in paths by its short name. While another piece of work
holds a server, changes to it are refused with `409` and a code that says
what holds it: `restoring`, `creating`, `deleting`, `moving`, `importing`,
`busy` (a snapshot is being taken) or `move_unfinished`. A server whose
files did not all arrive will not start: `create_incomplete`,
`restore_incomplete` or `import_incomplete`.

Giving a server a world last opened in a newer game version than it runs
is refused with `409 newer_world`, unless the request adds
`?newerWorld=true` to say that is meant. That covers making a copy, a
`PATCH` that changes the version, `PUT .../modpack` and `PUT .../import`.

### `GET /orgs/{org}/servers`

**Viewer.** Returns the organization's servers, by name.

### `POST /orgs/{org}/servers`

Make a server. **Admin.** Body:

| Field | |
| --- | --- |
| `name` | Short name: a lowercase letter, then up to 39 of `a-z 0-9 -`. |
| `spec` | The server's settings: see [The server resource](#the-server-resource). `eula` must be `true`. |
| `fromSnapshot` | Make it from a snapshot. Whatever `spec` leaves out is as the snapshot has it. |
| `fromServer` | A copy of another server of the organization, as it is now. |
| `fromTemplate` | From a template's current version. |
| `world` | With one of the three above: `new` for a new world in place of the source's, `kept` to keep a template's world even where it gives its servers new ones. |
| `node` | A node to put it on, in place of the panel's choice. |
| `picture` | With one of the three above: a PNG as base64, or `null` for none. |

At most one of `fromSnapshot`, `fromServer` and `fromTemplate`.

- **A new server** is made from `spec`. What it leaves out: running, the
  newest release (settled to a version number now, so it never upgrades
  by itself), Vanilla, 4Gi of memory, 10Gi of storage, the shared router,
  and an address of `<name>.<org>.<domain>` where the panel has a router
  domain. Content is pinned to versions now. Known mods' ports are opened
  if `spec.ports` is not given. Through the API, nobody is whitelisted by
  membership unless `spec.autoWhitelist` says so (the web form whitelists
  every member).
- **From a snapshot, a server or a template**, the server is made stopped
  and its files are copied in the background; it then takes the state
  asked for. `fromServer` takes a snapshot of the original first, unless
  its newest already has everything. A snapshot of a deleted server, under
  its own name, brings it back with its history. Game settings in
  `spec.properties` are laid over the source's, and `null` stops the
  panel setting one. The modpack, and the source's mods, always come from
  the source. A copy never takes the source's `image`, `env` or JVM flags
  unless `spec` names them.
- **A modpack** from Modrinth or CurseForge is
  `spec.modpack: {source, project, version?}`; the panel pins it to a
  version, its game version and loader. For an uploaded `.mrpack`, make
  the server stopped with `spec.modpack: {source: "upload", project: "pack.mrpack"}`,
  then send the file to [`PUT .../modpack`](#put-orgsorgserversservermodpack).
- **A world** from an archive, the library or a template, and uploaded
  content and pictures, are sent after the server is made, to
  [`PUT .../import`](#put-orgsorgserversserverimport),
  [`PUT .../content`](#put-orgsorgserversservercontent) and
  [`PUT .../icon`](#put-orgsorgserversservericon), with `?wait=true` so
  each waits for the new server's files. This is what the web form does.

Returns `201` with the server. With `?wait=true`, a copy answers once its
files are in. Errors: `conflict` (409, the name is taken),
`name_has_history` (409, a deleted server had the name: bring it back from
its snapshot or pick another), `hostname_taken` (409, another server has
the address), `domain_claimed` (409, the address is under another
organization's domain), `unsettled` (409, the original is starting or
stopping), `invalid` (400).

```sh
# From a snapshot, stopped.
curl -H "Authorization: Bearer $TOKEN" -d '{"name":"creative-copy","fromSnapshot":"<id>","spec":{"state":"Stopped"}}' "$AVSYS/orgs/friends/servers?wait=true"
# A copy on another game version, with game settings laid over the original's.
curl -H "Authorization: Bearer $TOKEN" -d '{"name":"creative-next","fromServer":"creative","spec":{"version":"1.21.5","properties":{"difficulty":"peaceful","pvp":null}}}' "$AVSYS/orgs/friends/servers?wait=true"
```

### `GET /orgs/{org}/servers/{server}`

**Viewer.** Returns the server.

### `PATCH /orgs/{org}/servers/{server}`

Change a server's settings. **Admin.** Body: `{spec: {...}}`, applied as a
JSON merge patch: what is left out stays, `null` clears, lists are
replaced whole. Most changes to a running server apply at its next restart
(`status.pendingRestart` says so). Changing the version or loader pins its
content again for the new one. Returns the server. `stale` (409) if it kept
changing underneath.

```sh
curl -H "Authorization: Bearer $TOKEN" -X PATCH -d '{"spec":{"resources":{"memory":"6Gi"}}}' $AVSYS/orgs/friends/servers/creative
# Its own snapshot schedule; null goes back to the organization's.
curl -H "Authorization: Bearer $TOKEN" -X PATCH -d '{"spec":{"backups":{"keep":{"daily":30}}}}' $AVSYS/orgs/friends/servers/creative
```

### `DELETE /orgs/{org}/servers/{server}`

Delete a server. **Admin.** The server is stopped and a last snapshot is
taken first, so it can be brought back; `?snapshot=false` skips that. The
request holds until the snapshot is taken. If the snapshot fails, the
server is not deleted (`502 snapshot_failed`). Returns `204`.

### `POST /orgs/{org}/servers/{server}/{action}`

`start`, `stop` or `restart`. **Operator.** Each also wakes a sleeping
server. Restarting a running server whose settings have not changed
restarts the game in place; otherwise its pod is made again. Returns the
server. `no_modpack` (409) for a server waiting for its `.mrpack`. Any
other action is `404`.

### The server resource

The `spec` of a server, as in `kubectl get mcs -o yaml`:

| Field | What it is |
| --- | --- |
| `displayName` | Name shown in the panel and the server list. |
| `state` | `Running` or `Stopped`. |
| `eula` | Must be `true`: you accept the Minecraft EULA. |
| `version` | Game version, or `LATEST`. Ignored with a modpack. |
| `loader` | `vanilla`, `paper`, `purpur`, `folia`, `fabric`, `quilt`, `forge` or `neoforge`. |
| `loaderVersion` | A loader build to stay on; empty for the newest. |
| `gameJava` | The Java the game version ships with. Set by the panel. |
| `modpack` | `{source: modrinth\|curseforge\|upload, project, version, gameVersion, loader, title, versionName, iconUrl}`. |
| `mods` | Mods, plugins and datapacks: `[{source: modrinth\|curseforge\|url\|upload, kind: mod\|plugin\|datapack, project, version, java, file, title, versionName, iconUrl}]`. |
| `resourcePack` | `{url, sha1, enforce, prompt, uploaded, project, formats, stack}`. |
| `properties` | `server.properties` entries the panel sets. |
| `ops`, `whitelist` | Players, by name or UUID, up to 1000 each. |
| `autoWhitelist` | Members at this role or above are whitelisted: `viewer`, `operator`, `admin`, `owner`, or `off`/empty for none. |
| `autoOp` | Members at this role or above are operators: empty (operator), `operator`, `admin`, `owner` or `off`. |
| `whitelistMembers`, `opMembers` | `{also: [user IDs], except: [user IDs]}`: members picked one by one. |
| `whitelistLists`, `opLists` | IDs of saved player lists, up to 50. |
| `resources` | `{memory, cpu?}`. Memory is the limit; the game's heap is sized under it. At least 512Mi. |
| `jvm` | `{aikarFlags, extraFlags}`. Extra flags may not set the heap. |
| `storage` | `{size, storageClassName?}`. Can grow, not shrink. At least 1Gi. |
| `expose` | `{type: Router\|LoadBalancer\|NodePort\|None, hostname, loadBalancerIP, nodePort, serviceAnnotations}`. |
| `ports` | Extra ports, up to 8: `[{name, port, protocol: TCP\|UDP, for?}]`. Not 25565 or 25575. |
| `sleep` | `{enabled, idleMinutes}` (default 15). Only with the router. |
| `backups` | The server's own snapshot schedule: any of `{enabled, minute, keep}`; what is left out follows the organization. |
| `worldOrigin` | Where its world came from. Set by the panel; a request cannot set it. See [Where a world came from](#where-a-world-came-from). |
| `image` | Another image in place of `itzg/minecraft-server`. |
| `env` | More environment variables for the game, applied last. |
| `nodeSelector` | Leave placing to the scheduler, on these nodes. |

#### Where a world came from

`spec.worldOrigin` is `{kind, library?, seed?, file?, server?, snapshot?, template?, time}`,
written by the panel whenever it puts a world in. `time` is when. `kind` is:

| `kind` | |
| --- | --- |
| `new` | Made by the game: a new server, `world=new` or `world=none` on import, or a copy or snapshot made with `world: "new"`. `seed` is the one asked for, if any. |
| `library` | `library: {id, name}`: the library's world, named as it was then. |
| `upload` | A world sent to `PUT .../import?world=true`; `file` is its name as the request's `file` gave it. |
| `archive` | A server's archive imported with its world; `file` as above. |
| `copy`, `snapshot`, `template` | The world came with a copy, a snapshot or a template that knew nothing of where it came from. |

A copy (`fromServer`), a server made from another's snapshot or a template
keep the source's `worldOrigin`, with `server` (the original, or the
server the snapshot is of), `snapshot` (its ID) or `template: {id, name}`
saying what it came by way of, and `time` now: a copy of a server made
from the library is still that library world. A server brought back from
its own snapshot, and a restore, have the snapshot's, as it was then. A
server made before the panel kept this, or outside it, has none.

`status` has `phase` (`Stopped`, `Stopping`, `Starting`, `Running`,
`Sleeping` or `Error`), `message`, `address` (what players type),
`ports` (where each extra port is reached), `version` (as the running game
says), `players: {online, max, names}`, `maxPlayers`, `node`,
`lastActive`, `restarts`, `pendingRestart`, `snapshots` (the last one, and
the last error) and `observedGeneration`.

## A server's content

### `PUT /orgs/{org}/servers/{server}/content`

Upload a mod, plugin or datapack. **Admin.** Query: `kind` (`mod`,
`plugin` or `datapack`), `name` (the file's name: `.jar`, or `.zip` for a
datapack), `wait=true` (for a server made a moment ago). Body: the file, up
to 256 MiB. It is kept in the server's files and installed at the next
start. Returns the server.

Content from Modrinth, CurseForge or an address is added by changing
`spec.mods` with `PATCH`; the panel pins each entry to a version that fits
the server's loader, game version and Java.

### `PUT /orgs/{org}/servers/{server}/modpack`

Upload the server's `.mrpack`, or a newer one. **Admin.** The server must
have an uploaded modpack set. Query: `name` (ends in `.mrpack`), `wait`,
`newerWorld`, or `upload` and `size` for one sent [in parts](#uploads).
Body: the file, up to 2 GiB. Returns the server.

### `GET /orgs/{org}/servers/{server}/modpack/contents`

What the server's modpack holds. **Viewer.** Returns
`{files: [{path, server, project?, version?, slug?, title?, iconUrl?, source?, page?, manual?}]}`.
`server` is false for files only players need; `manual` marks a CurseForge
file that has to be downloaded by hand.

### `GET /orgs/{org}/servers/{server}/modpack/icon`

The picture in an uploaded `.mrpack`. **Viewer.**

### `PUT /orgs/{org}/servers/{server}/import`

Put files into a server from an archive: a whole server folder, or a world.
**Admin.** The server must be stopped (`409 running`). Query:

| Parameter | |
| --- | --- |
| `world` | Empty: the archive is a server folder, for a server with no files yet. `true`: it is a world. `new`: no archive; the game makes a new world, with `{"properties": {"level-seed", "level-type", "generate-structures", "hardcore"}}` as the body. `none`: a server folder, leaving its world out. |
| `replace=true` | Replace the world the server has. A snapshot is taken first. |
| `root` | The folder in the archive to take. |
| `template=<id>`, `library=<id>` | No archive: the world of a template or of the library. `pack=true` also takes its resource pack. |
| `from=upload`, `upload`, `size` | The archive was sent in parts with [`PUT .../import/upload`](#put-orgsorgserversserverimportupload). |
| `file` | The archive's name, kept in `spec.worldOrigin.file`. |
| `version` | The game version the world was last opened in. |
| `content=listed` | Leave out the archive's mods, plugins and datapacks; those in `spec.mods` are installed instead. |
| `players=listed` | Trim the archive's operators and whitelist to the server's lists. |
| `wait`, `newerWorld` | |

Body: the archive (`.zip` or `.tar.gz`), up to 2 GiB. Returns `202` with the
server once the archive is in; the rest goes on in the background. With
`?wait=true`, `200` when all is done. Errors: `has_files` and `has_world`
(409) when there is something to replace and `replace` was not said.
Once the world is in, `spec.worldOrigin` says where it came from. Putting a
library world back is this with `world=true&replace=true&library=<id>`.
Deleting the world is `world=new&replace=true` with no body: the server's
settings stay as they are, and the next start makes a new world from them;
`spec.worldOrigin` is then `new`, with the seed `server.properties` sets,
if any.

### `GET /orgs/{org}/servers/{server}/world`

What the server's world says of itself, read from its files. **Viewer.**
Returns `{level, exists, name?, seed?, version?, lastPlayed?, size?}`:
the world's folder (as `server.properties` names it), whether it is there
yet, its `level.dat`'s name, seed, the game version it was last opened in
and when it was last played, and its folders' size in bytes, left out if
counting them takes too long. `502 workspace` when the files cannot be
reached; `409 busy` while files are being put in.

### `GET /orgs/{org}/servers/{server}/world/archive`

Download the server's world as it is now. **Operator**, as for the
server's files. A zip of a single-player save named `<server> world.zip`,
made as it is sent: the world (the folder `server.properties` names) in a
folder named after the server, with a Nether and End kept beside it, as
Paper and Spigot keep them (`world_nether/DIM-1`, `world_the_end/DIM1`), put
in it as `DIM-1` and `DIM1`, as for a
[library world](#get-orgsorgworldsworldarchive). A running game is told to
save everything and then hold its saves (`save-all flush`, `save-off`) while
the files are read, and to save again (`save-on`) as the download ends,
however it ends; a game that does not answer is read as its files are. Saves
are held for an hour at most: a download that takes longer is cut off, also
one to a client that stopped reading. One download of a server's world at a
time (`409 busy`). While it is sent no snapshot, restore or import begins, and a game restarted
meanwhile cuts it off, which the browser shows as a failed download. Errors:
`404 no_world` before the game has made a world; `409` while another
operation runs, or while the server starts or stops; `502 workspace` when
the files cannot be reached.

### `GET /orgs/{org}/servers/{server}/import/upload`

How much of an archive sent in parts to this server has arrived. **Admin.**
Query: `upload`. Returns `{received}`.

### `PUT /orgs/{org}/servers/{server}/import/upload`

Send a part of an archive straight into the server's volume. **Admin.**
Query: `upload`, `offset`, `length?`, `wait?`. Body: up to 64 MiB. Returns
`{received}`; `409 offset` as for [uploads](#uploads). Then call
`PUT .../import?from=upload&upload=<id>&size=<bytes>`.

### `DELETE /orgs/{org}/servers/{server}/import/upload`

Throw such an upload away. **Admin.** Returns `204`.

## A server's console, logs and players

### `GET /orgs/{org}/servers/{server}/console`

The live console, as [server-sent events](#live-events). **Viewer.**
Events: `reset` (the game started or the stream joined it; up to 500 recent
lines follow), `lines` (an array of lines), `waiting` (no game running yet).
It stays open across restarts.

### `POST /orgs/{org}/servers/{server}/console`

Run a command. **Operator.** Body: `{command, as?}`. With no `as`, it goes
through RCON and its answer comes back as `{output}`. With
`"as":"server"`, it is typed at the server console: players see it come
from `[Server]` rather than `[Rcon]`, and what it prints goes to the log.
Errors: `not_running` (409), `snapshotting` (409: `save-off`, `save-on`
and `save-all` wait while a snapshot holds the game's saves).

```sh
curl -H "Authorization: Bearer $TOKEN" -d '{"command":"list"}' $AVSYS/orgs/friends/servers/creative/console
curl -H "Authorization: Bearer $TOKEN" -d '{"command":"say hello","as":"server"}' $AVSYS/orgs/friends/servers/creative/console
```

### `GET /orgs/{org}/servers/{server}/console/commands`

The game's own commands for the version the server runs, for completing
them. **Operator.** Returns the game's command tree, as its data generator
writes it in `reports/commands.json`: nodes of `type` `root`, `literal` or
`argument`, with `children`, `executable`, `redirect`, and an argument's
`parser` and `properties`. The game version is in the header
`X-Game-Version`. The first time a version is asked for, the panel makes
its tree: the answer is `202` with `{making: true}` until it is ready, a
minute at most. A version older than 1.13, or one whose tree could not be
made, is `no_commands` (404). Commands of mods and plugins are not in it.

### `GET /orgs/{org}/servers/{server}/logs`

Earlier logs and crash reports. **Viewer.** Returns
`{logs: [{name, kind, size, modTime}]}`, `kind` being `current`, `log` or
`crash`.

### `GET /orgs/{org}/servers/{server}/logs/file`

One of them. **Viewer.** Query: `name` (as listed), `q` (search),
`from` / `before` (page by line, 2000 lines a page), `download` (the file
itself; `download=text` unpacks a `.gz`). Returns
`{name, total, truncated, firstError, lines: [{n, text, match?}], search?, capped?, more?}`.

### `GET /orgs/{org}/servers/{server}/activity`

What was done to the server, and by whom: the newest 50. **Viewer**; file
and terminal activity is shown from operator up. Returns
`[{at, action, detail?, user?}]`.

### `GET /orgs/{org}/servers/{server}/lists`

Players on the server's whitelist and operators that were added in game,
not by the panel. **Admin.** Returns
`{ops: {known, inGame: [{name, uuid?, bedrock?}]}, whitelist: {...}}`;
`known` is false if the lists could not be read.

### `DELETE /orgs/{org}/servers/{server}/lists/{list}/{name}`

Take a player added in game off `ops` or `whitelist`. **Admin.** Returns
`204`. A player the panel put there is refused: `member`, `saved_list` or
`listed` (409).

## A server's snapshots and operations

### `GET /orgs/{org}/servers/{server}/snapshots`

**Viewer.** Returns `{available, snapshots, operations}`.

### `POST /orgs/{org}/servers/{server}/snapshots`

Take a snapshot. **Operator.** A running game is told to write everything
out and hold its saves while the files are copied. Returns `202` with the
operation; with `?wait=true`, `201` with the snapshot. `unsettled` (409)
while the server is starting or stopping.

### `POST /orgs/{org}/servers/{server}/snapshots/{id}/restore`

Put the server back to one of its own snapshots. **Admin.** A snapshot is
taken first, so a restore can be undone. The server is stopped, its files
are put back, and so are its game version, loader, modpack, mods, resource
pack, ports, game settings, operators and whitelist. Memory, storage,
address, sleep, image and environment are left as they are. It is started
again if it was running. Returns `202` with the operation; with
`?wait=true`, the snapshot.

### `GET /orgs/{org}/servers/{server}/operation`

The work going on now, or `null`. **Viewer.** Returns
`{kind, org, server, snapshot?, stage, percent, startedAt}`, `kind` being
`snapshot`, `restore`, `create`, `delete-server`, `move`, `import` or
`template`.

### `GET /orgs/{org}/servers/{server}/usage`

Recent use, for the graphs. **Viewer.** Returns
`{available, limit, cpuLimit?, noTicks?, every, samples: [{t, cpu?, memory?, players, tps?, tickTarget?, mspt?}]}`.

### `GET /orgs/{org}/servers/{server}/placement`

Where the server's files are. **Admin.** Returns
`{node?, local, labels?, down?, newest?, unable?}`. `local` is whether
they are on one node's own disk; `down`, `newest` (the newest snapshot)
and `unable` are there when that node is down.

### `POST /orgs/{org}/servers/{server}/move`

Move the server to another node. **Admin.** Body: `{node, fromSnapshot?}`.
Files on a node's own disk go by snapshot: the server is stopped, a
snapshot taken, and it is restored on the new node. When the old node is
down, give `fromSnapshot`, the ID of the newest snapshot, to rebuild from
it (`409 node_down` without). Returns `202` with the operation; with
`?wait=true`, the server.

## A server's terminal, picture and resource pack

Paths are under `/orgs/{org}/servers/{server}`.

### `GET /orgs/{org}/servers/{server}/terminal`

A shell in the server's workspace. **Operator.** A WebSocket. Binary
messages carry the terminal's bytes both ways. A text message
`{"type":"resize","cols":N,"rows":N}` resizes it. The shell starts at
80×24 as `xterm-256color`, in `/data`.

### `GET /orgs/{org}/servers/{server}/icon`

The server's icon, as the 64-pixel PNG players see. **Viewer.** `404` if
it has none.

### `PUT /orgs/{org}/servers/{server}/icon`

Set it. **Admin.** Body: a PNG, JPEG or GIF of up to 4 MB, at most 4096
pixels a side. The panel crops it square, scales it to 64 pixels and writes
`server-icon.png`. Query: `wait=true` waits for a server made a moment ago
to have its files; with it, `start=true` starts the server afterwards.
Returns `204`.

### `DELETE /orgs/{org}/servers/{server}/icon`

Remove it. **Admin.** Returns `204`.

### `PUT /orgs/{org}/servers/{server}/resource-pack`

Give the server a resource pack, replacing the one it has. **Admin.** Body:
a zip with `pack.mcmeta` at its top. Query: `name` (the file's name), or
`world=<id>` / `template=<id>` to use the pack of a library world or
template in place of a body; `wait=true`. The pack is kept in the server's
files and served to players from the panel. It can be up to 250 MB for
1.18 and later, 100 MB for 1.15–1.17, 50 MB before. Returns the server.
`packs_unavailable` (503) where the panel has nowhere to keep packs.

### `DELETE /orgs/{org}/servers/{server}/resource-pack`

Remove the server's resource pack. **Admin.** Returns the server.

### `POST /orgs/{org}/servers/{server}/resource-packs`

Add a pack on top of the server's packs; several are joined into one,
higher ones winning where two have the same file. **Admin.** Body and query
as for `PUT .../resource-pack`. Up to 32 packs. Returns the server.

### `POST /orgs/{org}/servers/{server}/resource-packs/url`

Add a pack from an address. **Admin.** Body:
`{url, sha1?, project?: {source, id, ...}}`. `source` is `modrinth` or
`curseforge`. Returns the server.

### `PUT /orgs/{org}/servers/{server}/resource-packs`

Reorder the joined packs. **Admin.** Body: `{order: [sha1, ...]}`, top
first; packs left out are taken off, and an empty list removes the pack.
Takes `wait=true`. Returns the server.

## A server's files

Paths are under `/orgs/{org}/servers/{server}`. All need **Operator**.
`path` is a path in the server's directory, as `/world/level.dat`; leaving
it out means the top. Nothing outside the server's directory can be named.
A server that is being restored, made, moved, deleted or imported refuses
changes with `409` and a code that says which. `workspace` (502) when the
server's workspace cannot be reached.

### `GET /orgs/{org}/servers/{server}/files`

List a folder. Returns `{path, entries: [{name, size, dir, link?, modTime}]}`,
folders first.

### `DELETE /orgs/{org}/servers/{server}/files`

Delete a file or folder, with everything in it. A link is removed, never
what it points to. Returns `204`.

### `GET /orgs/{org}/servers/{server}/files/content`

A file's bytes. Query: `path`, `download` (to save it as a file), `view`
(to show a picture as one). A file of 2 MB or less comes with an `ETag`,
to send back as `If-Match` when saving. Ranges work.

### `PUT /orgs/{org}/servers/{server}/files/content`

Write a file. Body: its bytes, up to 2 GB in one request.

- With `If-Match: <etag>` (as the editor does), the save is refused with
  `412 changed` if the file changed since it was read, or `412 removed` if
  it is gone. Files saved this way are up to 2 MB.
- With `If-None-Match: *`, it is refused with `409 exists` if something is
  there.
- `no_room` (507) if it does not fit.

**In parts**, for large files: add `upload=<id>` (8–64 of
`A-Z a-z 0-9 -`, chosen by you), `offset=<bytes sent so far>` and
`size=<whole size>`, and send parts of up to 64 MB in order. Each answers
`{received}`. A part that is not the next one gets
`409 {"code":"offset", "received": N}`, saying where to go on from. When
the last part is in, the file is put in place.

### `GET /orgs/{org}/servers/{server}/files/upload`

How much of an upload in parts has arrived. Query: `path`, `upload`,
`size?`. Returns `{received}`; with `size`, an upload that is all there is
put in place.

### `DELETE /orgs/{org}/servers/{server}/files/upload`

Throw away an upload in parts. Query: `path`, `upload`. Returns `204`.

### `POST /orgs/{org}/servers/{server}/files/mkdir`

Make a folder, and any above it. Query: `path`. Returns `204`.

### `POST /orgs/{org}/servers/{server}/files/rename`

Rename or move. Query: `path` and `to`. Returns `204`. `exists` (409) if
something is at `to`.

### `POST /orgs/{org}/servers/{server}/files/extract`

Unpack an archive (`.zip`, `.tar`, `.tar.gz`, `.tar.xz`, `.tar.bz2`, `.7z`,
`.rar`) next to it. Query: `path`. Nothing is overwritten. Returns
`{path}`, the folder it made or filled.

### `GET /orgs/{org}/servers/{server}/files/archive`

Download as a zip. Query: `path`, any number of times (up to 500): none for
the whole server, one folder, or several files and folders.

### `GET /orgs/{org}/servers/{server}/files/search`

Find files by name. Query: `q` (part of a name, 1–100 characters).
Searches the whole server. Returns `{results: [{name, path, dir, size, modTime}], more}`,
at most 200. `slow` (504) if it takes over ten seconds.

## Live events

### `GET /events`

A stream of what changes, as
[server-sent events](https://html.spec.whatwg.org/multipage/server-sent-events.html).
**Signed in.** Each event is `event: <type>` and `data: <json>`. You get
events of the organizations you are in (all of them, for the instance
administrator). Query: `tab`, an ID of your choosing for this page, used
for showing who has which server open.

| Event | Data |
| --- | --- |
| `server.updated`, `server.deleted` | The server |
| `operation.progress` | `{kind, org, server, snapshot?, stage, percent, startedAt}` |
| `operation.done` | `{kind, org, server, error?, scheduled?, calledOff?, snapshot?}` |
| `activity` | `{org, server}`: something was done to the server; read its activity again |
| `offsite.done` | `{org, error?}` |
| `presence` | `{org, people: [{user: {id, name, avatarUrl?}, server, file?}]}` |
| `cluster` | `{reachable, since?}` |
| `resync` | The stream missed something: read again what you show |
| `ping` | Every 20 seconds, to keep the connection open |

`kind` is as in [`GET .../operation`](#get-orgsorgserversserveroperation);
`operation.done` also comes with `delete` for a snapshot deleted.

### `GET /orgs/{org}/presence`

Who has which of the organization's servers open. **Viewer.** Returns
`[{user, server, file?}]`, as `people` in the `presence` event. `file` is
shown from operator up.

### `PUT /orgs/{org}/presence`

Say which server this page has open. **Viewer.** Body:
`{tab, server?, file?}`; an empty `server` says the page left. A page is
forgotten a minute after it last said so. Returns `204`.

## Catalog

Looking things up for the forms. All are **signed in**. Answers from
Modrinth and CurseForge are cached by the panel.

### `GET /players/{uuid}/head`

A player's face from their skin, as a PNG. **Signed in.** Takes Java UUIDs
and Bedrock players' Floodgate UUIDs. `404` if there is none.

### `GET /catalog/minecraft/versions`

Game versions a loader has builds for. Query: `loader`. Returns
`{latest: {release, snapshot}, versions: [{id, type, releaseTime, java?}], loader?}`.

### `GET /catalog/minecraft/pack-formats`

The resource pack formats of game versions. Query: `versions`
(comma-separated). Returns `{min, max}`.

### `GET /catalog/modrinth/search`

Search Modrinth. Query: `type` (`mod`, `plugin`, `datapack`, `modpack`,
`resourcepack`), `q`, `loader`, `gameVersion`, `side`, `categories`,
`sort` (`relevance`, `downloads`, `follows`, `newest`, `updated`),
`offset`. Returns Modrinth's own search answer.

### `GET /catalog/modrinth/categories`

Modrinth's categories for a `type`. Returns a list of names.

### `GET /catalog/modrinth/projects`

Several projects at once. Query: `ids` (comma-separated, up to 50).
Returns `[{id, slug, title, icon_url, project_type}]`.

### `GET /catalog/modrinth/projects/{id}/versions`

A project's versions, narrowed by `loader` and `gameVersion`. Returns
Modrinth's own list.

### `POST /catalog/sha1`

Download a file to learn its SHA-1. Body: `{url}` (a public `http` or
`https` address; up to 512 MB). Returns `{sha1, size, formats?}`.

### `POST /catalog/modrinth/files`

Which Modrinth files these are. Body: `{hashes: [sha1, ...]}` (up to 1000).
Returns `[{hash, project, version, versionName, url}]`.

### `GET /catalog/curseforge/search`

Search CurseForge. Query as for Modrinth. Returns
`{hits: [...], offset, limit, total_hits}`, in Modrinth's shape. Each hit
has `downloadable`: false for a file its author allows to be downloaded only
on CurseForge. Every CurseForge route answers `404 no_curseforge` when no
key is set.

### `GET /catalog/curseforge/categories`

CurseForge's categories for a `type`.

### `GET /catalog/curseforge/projects`

Several projects at once. Query: `ids` (numbers, up to 50).

### `GET /catalog/curseforge/projects/{id}/versions`

A project's files, as versions in Modrinth's shape, narrowed by `loader`
and `gameVersion`.

### `GET /catalog/curseforge/projects/{id}/page`

Redirects to the project's page on CurseForge.

## Outside the API

- `GET /packs/<sha1>.zip` serves the resource packs uploaded to servers, to
  players' games, with no sign-in. Only packs some server has are served.
  This can also be on a listener of its own: see
  [Install](install.md#resource-packs-on-a-separate-address).
- Anything else under `/api/` is `404 not_found`. Every other path is the
  web UI.
- **SSH** on port 2222 is not HTTP: `ssh <server>.<org>@<panel host> -p 2222`,
  with a key from your account. See the [user guide](user-guide.md#ssh-sftp-and-vs-code).
