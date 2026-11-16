# Development

Building avSYS, running it locally, testing it, and how changes get in.
[Architecture](architecture.md) explains how the parts fit.

## What you need

Go, [Bun](https://bun.sh), Docker, [restic](https://restic.net) and a
cluster in your kubectl context (OrbStack's works well). Without restic the
panel runs, with snapshots switched off.

## Running it

```sh
make dev        # builds everything and runs the panel on http://localhost:8080
```

`make dev` runs the panel on your machine against the current kubectl
context, reaching pods through the API server, and keeps its database in
`.dev/`. For UI work, also run `bun run dev` in `web/` and open the address
Vite prints; it proxies API calls to the panel.

## Tests

```sh
make test            # unit tests, static checks, type checks and lint
make test-snapshots  # the snapshot engine against a real workspace container
make test-browser    # the web UI in Chromium and WebKit, against a panel with nothing behind it
make e2e             # creates a real server through a running panel and checks it end to end
make e2e-cluster     # the same from nothing: an empty k3s in Docker, installed with the chart
make chart           # after changing deploy/chart: deploy/install.yaml and the chart's test renders
make generate        # after changing api/
```

- **API tests** in `internal/api` run the panel in-process and check every
  route against every role. A new route under an organization has to be
  added to that table (`TestOrganizationRoutes`), or the tests fail. A new
  route of any kind also needs a heading in [docs/api.md](api.md)
  (`TestEveryRouteIsDocumented`).
- **`make test-browser`** needs neither a cluster nor the internet. It
  starts `cmd/avsys-uitest`, which is the real API and web UI with
  stand-ins for everything they reach out to: servers are remembered but
  never run, and what the panel looks up at Modrinth, CurseForge, Mojang
  and the build sites is answered from `web/tests/upstream`. The tests in
  `web/tests` open every page, use the dialogs and forms, and check what
  each role is shown, in both engines, and fail on any error thrown in the
  page. Fetch the browsers once with `cd web && bunx playwright install`.
  A change to the web UI should come with a test here.
- **`make e2e`** needs `AVSYS_URL` and `AVSYS_TOKEN` (an instance
  administrator's API token). It makes a throwaway organization and a Paper
  server with plugins, checks the console, files, snapshots and restore,
  SSH and the router against it, and deletes both.
- **`make e2e-cluster`** needs only Docker. It starts an empty single-node
  k3s in a container, builds both images, installs the chart, creates the
  first account and then runs the same check.

## How changes get in

`main` is protected. Every change is a pull request, and CI must pass on
it: `test` (Go tests, static checks, known vulnerabilities, generated code,
the chart, the web UI's checks and image scans), `e2e` (the end-to-end
check on a fresh k3s) and the browser tests. CI also runs every night on
`main`, so that a change outside the repository (a new Paper build, a new
game image, a change at Modrinth) shows up there and not on the next pull
request. Images are published only from a run that passed.

## Releases

Push a version tag on main: `git tag v0.1.0 origin/main && git push origin v0.1.0`.
CI tests it, publishes both images under that tag and the chart as version
`0.1.0`, and makes a GitHub release with notes from the pull requests
merged since the last one, the install and upgrade commands, and that
version's `install.yaml`. A tag such as `v0.2.0-rc.1` makes a prerelease.
