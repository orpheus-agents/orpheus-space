COMPOSE = docker compose
RUN = $(COMPOSE) --profile tools run --rm --no-deps tools
# The tools image has no local COPY inputs; its Dockerfile defines all tools.
# POSIX cksum works on macOS and Linux without requiring host Go or Python.
export ORPHEUS_UID ?= $(shell if [ "$$(uname -s)" = Linux ]; then id -u; else echo 1000; fi)
export ORPHEUS_GID ?= $(shell if [ "$$(uname -s)" = Linux ]; then id -g; else echo 1000; fi)
export ORPHEUS_TOOLS_IMAGE := orpheus-space-tools:$(shell cksum < .docker/tools/Dockerfile | awk '{print $$1 "-" $$2}')-$(ORPHEUS_UID)-$(ORPHEUS_GID)

.PHONY: tools tools-build start stop generate generate-check sqlc-check fix format gofix gofix-check tidy-check deadcode lint lint-go lint-api lint-docker test test-go test-integration test-migrations test-go-race vuln build docker-build migrate check

tools:
	@docker image inspect "$(ORPHEUS_TOOLS_IMAGE)" >/dev/null 2>&1 || $(COMPOSE) build tools

tools-build:
	$(COMPOSE) build tools

start: migrate
	$(COMPOSE) up -d --wait app

.PHONY: start-worker
start-worker: migrate
	$(COMPOSE) --profile worker up -d --wait worker

stop:
	$(COMPOSE) --profile tools --profile test --profile worker --profile saml-test down

migrate:
	$(COMPOSE) build app
	$(COMPOSE) up -d --wait db
	$(COMPOSE) run --rm --no-deps app go run ./cmd/server migrate up

generate: tools
	$(RUN) go run ./tools/generate

generate-check: tools
	$(RUN) go run ./tools/generate -check

.PHONY: generate-client generate-client-check test-client
generate-client: tools
	$(RUN) go run ./tools/generate -client

generate-client-check: tools
	$(RUN) go run ./tools/generate -client -check

test-client: tools
	$(RUN) go test -race ./client

sqlc-check: tools
	$(COMPOSE) --profile test up -d --wait test-db
	$(RUN) go test -tags integration -count=1 ./tools/sqlcheck

fix format: tools
	$(RUN) sh -ec 'gofmt -w client cmd internal tools; goimports -w client cmd internal tools'

gofix: tools
	$(RUN) go fix ./...

gofix-check: tools
	$(RUN) sh -ec 'f=$$(mktemp); trap '\''rm -f "$$f"'\'' EXIT; go fix -diff ./... > "$$f"; if test -s "$$f"; then cat "$$f"; exit 1; fi'

tidy-check: tools
	$(RUN) go mod tidy -diff

# deadcode reports findings on stdout but exits successfully; fail on any finding.
# Include test entrypoints and integration build tags so test-only helpers remain reachable.
# Public client methods are entrypoints for external consumers, not dead code.
deadcode: tools
	$(RUN) sh -ec 'f=$$(mktemp); trap '\''rm -f "$$f"'\'' EXIT; go tool deadcode -filter="^github.com/orpheus-agents/orpheus-space/(cmd|internal|tools)(/|$$)" -test -tags=integration ./... > "$$f"; if test -s "$$f"; then cat "$$f"; exit 1; fi'

lint-go: tools
	$(RUN) sh -ec 'files=$$(gofmt -l client cmd internal tools); if test -n "$$files"; then printf "%s\n" "$$files"; exit 1; fi'
	$(RUN) go vet ./...
	$(RUN) golangci-lint run --build-tags integration ./...

lint-api: tools
	$(RUN) redocly lint --config redocly.yaml api/openapi.yaml

lint-docker: tools
	$(RUN) hadolint .docker/app/dev/Dockerfile .docker/app/prod/Dockerfile .docker/tools/Dockerfile

lint: lint-go lint-api lint-docker

test-go: tools
	$(RUN) go test ./...

test-integration: tools
	$(COMPOSE) --profile test up -d --wait test-db
	$(RUN) go test -tags integration ./internal/... ./cmd/...

test-migrations: tools
	$(COMPOSE) --profile test up -d --wait test-db
	$(RUN) go test -tags integration ./internal/migrate

test: test-go test-integration test-migrations

test-go-race: tools
	$(COMPOSE) --profile test up -d --wait test-db
	$(RUN) go test -race -tags integration ./...

vuln: tools
	$(RUN) govulncheck ./...
	$(RUN) trivy fs --no-progress --db-repository ghcr.io/aquasecurity/trivy-db:2 --db-repository mirror.gcr.io/aquasec/trivy-db:2 --scanners vuln,misconfig --exit-code 1 --severity HIGH,CRITICAL --skip-dirs .git --skip-dirs bin .

build: tools
	$(RUN) go build -trimpath -o /tmp/orpheus-space ./cmd/server

.PHONY: build-client
build-client: tools
	$(RUN) go build ./client

docker-build:
	$(COMPOSE) build app
	docker build -f .docker/app/prod/Dockerfile -t orpheus-space:local .

# Keep integration and race tests serial: they share the test database.
CHECK_JOBS ?= 4

check: generate-check
	$(MAKE) -j$(CHECK_JOBS) tidy-check gofix-check lint deadcode sqlc-check test-go vuln build build-client build-cli
	$(MAKE) -j1 test-integration test-migrations test-go-race

.PHONY: smoke
smoke: tools docker-build
	$(COMPOSE) --profile test up -d --wait test-db
	sh tools/smoke.sh

# Isolated SAML round trip with a real IdP; no production credentials or UI.
.PHONY: test-saml
test-saml: tools
	$(COMPOSE) --profile test --profile saml-test up -d --wait test-db test-keycloak
	$(COMPOSE) --profile tools run --rm --no-deps -e TEST_KEYCLOAK_URL=http://test-keycloak:8080 tools go test -race -tags integration -count=1 -timeout=3m -run TestKeycloakRoundTrip ./internal/httpserver

.PHONY: build-cli release-cli
build-cli: tools
	$(RUN) go build -trimpath -o /tmp/orpheus-space-cli ./cmd/orpheus-space

release-cli: tools
	$(COMPOSE) --profile tools run --rm --no-deps -e VERSION=$(or $(VERSION),dev) tools sh tools/release-cli.sh
