VERSION ?= dev
IMAGE ?= ghcr.io/gt-drehmal/avsys
WORKSPACE_IMAGE ?= ghcr.io/gt-drehmal/avsys-workspace

.PHONY: dev web generate test test-snapshots test-browser e2e e2e-cluster images

# Run the panel on this machine against the current kubectl context.
dev: web
	./hack/dev.sh

web:
	cd web && bun install && bun run build

# Regenerate deepcopy code and the CRD after changing api/. The generator is
# a fixed version: CI checks that what is committed is what it produces, and
# a new release of it must not be what fails that.
CONTROLLER_GEN_VERSION ?= v0.22.0
generate:
	go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION) object crd paths=./api/... output:crd:dir=deploy/crds

test:
	go vet ./...
	go test ./...
	cd web && bun run typecheck

# The snapshot engine against a real workspace container. Needs Docker and
# restic.
test-snapshots:
	docker build -q -t avsys-workspace:dev -f images/workspace/Dockerfile .
	AVSYS_TEST_WORKSPACE_IMAGE=avsys-workspace:dev go test -count=1 ./internal/backup/

# The web UI in a browser, in Chromium and WebKit, against a panel with
# nothing behind it (cmd/avsys-uitest). Needs neither a cluster nor the
# internet. The first time, fetch the browsers: cd web && bunx playwright install
test-browser: web
	cd web && bunx playwright test

# Exercise a running panel and cluster end to end. Needs AVSYS_URL and
# AVSYS_TOKEN (an instance admin's API token).
e2e:
	./hack/e2e.sh

# The same check from nothing: an empty k3s in Docker, the panel installed
# from deploy/install.yaml, its first account, then e2e. Needs only Docker.
e2e-cluster:
	./hack/e2e-cluster.sh

images:
	docker build -t $(IMAGE):$(VERSION) --build-arg VERSION=$(VERSION) .
	docker build -t $(WORKSPACE_IMAGE):$(VERSION) -f images/workspace/Dockerfile .
