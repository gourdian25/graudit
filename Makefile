# File: Makefile

# File: Makefile

.PHONY: help test race coverage coverage-summary coverage-check bench lint vet fmt clean deps docker-up docker-create docker-down tag release goreleaser-check guard-version

GO := go
MODULE := github.com/gourdian25/graudit
COVERAGE_MIN := 95
VERSION ?=

# The shared Postgres/Redis/Mongo containers every gourdian25 repo tests
# against. grsentry's docker-compose.yml is the source of truth for how
# they're built (images, ports, credentials, volumes); this path is where
# it sits in the usual side-by-side checkout. graudit needs no Redis or Kafka.
GRSENTRY_COMPOSE ?= ../grsentry/docker-compose.yml
SHARED_CONTAINERS := gourdian-postgres gourdian-mongo-auth

# wait_for runs the command $(2) once a second until it succeeds, giving up
# after 60 tries with an error naming container $(1), never looping forever.
wait_for = for i in $$(seq 60); do { $(2); } >/dev/null 2>&1 && exit 0; sleep 1; done; \
	echo "FAIL: $(1) is not ready after 60s; see 'docker logs $(1)'"; exit 1


help:
	@echo "Makefile targets for graudit:"
	@echo ""
	@echo "  make test             Run all tests"
	@echo "  make race             Run tests with race detector (mandatory before any commit touching the hash-chain or serialization code)"
	@echo "  make coverage         Generate HTML coverage report"
	@echo "  make coverage-summary Show coverage summary by function"
	@echo "  make coverage-check   Check the root package meets the $(COVERAGE_MIN)% threshold"
	@echo "  make bench            Run benchmarks"
	@echo "  make lint             Run linters (requires golangci-lint)"
	@echo "  make vet              Run go vet"
	@echo "  make fmt              Format code"
	@echo "  make clean            Clean build artifacts"
	@echo "  make deps             Verify and tidy dependencies"
	@echo "  make docker-up        Start the shared Postgres/Mongo test containers (from grsentry's docker-compose.yml), plus the standalone Mongo (idempotent)"
	@echo "  make docker-create    Create any missing container with docker run (fallback when grsentry isn't checked out beside this repo)"
	@echo "  make docker-down      Stop those containers (state preserved for a fast restart)"
	@echo "  make tag VERSION=vX.Y.Z         Create and push a git tag"
	@echo "  make release VERSION=vX.Y.Z     Tag, push, and run goreleaser release --clean"
	@echo "  make goreleaser-check           Dry run: validate config + snapshot release (no tag/push)"

test:
	@echo "Running tests..."
	$(GO) test -count=1 -timeout=5m -cover ./...
	@echo "Tests passed"

race:
	@echo "Running tests with race detector..."
	$(GO) test -race -timeout 5m ./...
	@echo "Race detector tests passed"

coverage:
	@echo "Generating coverage report..."
	$(GO) test -coverprofile=coverage.out -covermode=atomic ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "HTML coverage report saved as coverage.html"

coverage-summary:
	@echo "Coverage summary by function:"
	@$(GO) test -coverprofile=coverage.out ./...
	@$(GO) tool cover -func=coverage.out

# Requires local Postgres/MongoDB (see README) — checks only the root
# package against COVERAGE_MIN. Before flattening, this looped over one
# directory per backend subpackage plus root, including a stale "./mongo"
# entry that never matched the actual "mongostore" directory name and so
# silently reported "no coverage output" for it on every run — moot now
# that there's only one package to measure. example/ (a runnable demo, not
# library code under test) is excluded.
coverage-check:
	@echo "Checking the root package meets $(COVERAGE_MIN)% coverage..."
	@out=$$($(GO) test -cover . 2>&1); \
	pct=$$(echo "$$out" | grep -o '[0-9.]*%' | tr -d '%'); \
	if [ -z "$$pct" ]; then echo "FAIL: no coverage output"; exit 1; fi; \
	below=$$(awk -v p="$$pct" -v m="$(COVERAGE_MIN)" 'BEGIN { print (p < m) ? 1 : 0 }'); \
	if [ "$$below" = "1" ]; then \
		echo "FAIL: $$pct% is below $(COVERAGE_MIN)% threshold"; exit 1; \
	else \
		echo "OK: $$pct%"; \
	fi

bench:
	@echo "Running benchmarks..."
	$(GO) test -bench=. -benchmem -benchtime=10s ./...
	@echo "Benchmarks complete"

lint:
	@echo "Running linters..."
	@which golangci-lint > /dev/null || (echo "golangci-lint not found. Install with: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest" && exit 1)
	golangci-lint run ./...
	@echo "Linting passed"

vet:
	@echo "Running go vet..."
	$(GO) vet ./...
	@echo "Vet analysis complete"

fmt:
	@echo "Formatting code..."
	$(GO) fmt ./...
	@echo "Code formatted"

clean:
	@echo "Cleaning build artifacts..."
	rm -f coverage.out coverage.html
	$(GO) clean ./...
	@echo "Clean complete"

deps:
	@echo "Verifying dependencies..."
	$(GO) mod verify
	@echo "Tidying dependencies..."
	$(GO) mod tidy
	@echo "Dependency verification complete"

# docker-up is idempotent and shares its Postgres/Mongo containers with
# every other gourdian25 repo (graudit's are the graudit_test database and
# Mongo database). Containers that already exist, whoever made them, are
# just started. Missing ones are created from grsentry's docker-compose.yml
# when that checkout sits beside this one, so they're exactly what grsentry
# runs; otherwise docker-create builds them by hand. Every wait assumes the
# compose layout (mongod on 27018 inside its container) and gives up after
# 60s rather than hanging. graudit is the only repo needing the second,
# standalone (no-auth, no-replset) Mongo container -- required by
# TestNewMongoAuditLog_RequiresReplicaSet, which must never be skipped (see
# CLAUDE.md for why) -- so it is always run here, on 27019.
docker-up:
	@missing=""; for c in $(SHARED_CONTAINERS); do docker inspect $$c >/dev/null 2>&1 || missing="$$missing $$c"; done; \
	if [ -z "$$missing" ]; then \
		echo "Starting the shared test containers..."; docker start $(SHARED_CONTAINERS) >/dev/null; \
	elif [ -f "$(GRSENTRY_COMPOSE)" ]; then \
		echo "Creating$$missing from $(GRSENTRY_COMPOSE)..."; docker compose -f "$(GRSENTRY_COMPOSE)" up -d postgres mongo mongo-init; \
	else \
		echo "$(GRSENTRY_COMPOSE) not found; creating$$missing with docker run..."; $(MAKE) --no-print-directory docker-create; \
	fi
	@docker inspect gourdian-mongo-standalone >/dev/null 2>&1 || docker run -d --name gourdian-mongo-standalone --restart unless-stopped -p 27019:27017 mongo:8.3 >/dev/null
	@docker start gourdian-mongo-standalone >/dev/null
	@echo "Waiting for Postgres..."; $(call wait_for,gourdian-postgres,docker exec gourdian-postgres pg_isready -U postgres_user -d postgres)
	@docker exec gourdian-postgres psql -U postgres_user -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname = 'graudit_test'" | grep -q 1 || \
		docker exec gourdian-postgres createdb -U postgres_user graudit_test
	@echo "Waiting for Mongo..."; $(call wait_for,gourdian-mongo-auth,docker exec gourdian-mongo-auth mongosh --port 27018 --quiet --eval 'db.runCommand({ping:1})')
	@docker exec gourdian-mongo-auth mongosh --port 27018 --quiet -u root -p mongo_password --authenticationDatabase admin --eval \
		'try { rs.status() } catch (e) { try { rs.initiate({_id: "rs0", members: [{_id: 0, host: "127.0.0.1:27018"}]}) } catch (e2) {} }' >/dev/null
	@echo "Waiting for the Mongo replica set..."; $(call wait_for,gourdian-mongo-auth,docker exec gourdian-mongo-auth mongosh --port 27018 --quiet -u root -p mongo_password --authenticationDatabase admin --eval 'rs.status().members[0].stateStr' | grep -q PRIMARY)
	@echo "Waiting for Mongo (standalone)..."; $(call wait_for,gourdian-mongo-standalone,docker exec gourdian-mongo-standalone mongosh --quiet --eval 'db.runCommand({ping:1})')
	@echo "Docker test infrastructure ready (postgres/mongo-auth/mongo-standalone, database graudit_test)"

# docker-create builds whichever shared container is missing with plain
# docker run, for a checkout without grsentry beside it. Every image, port,
# credential and volume mirrors $(GRSENTRY_COMPOSE): keep the two in sync.
# The Mongo replica set is initiated by docker-up, as compose's one-shot
# mongo-init service does there.
docker-create:
	@docker inspect gourdian-postgres >/dev/null 2>&1 || docker run -d --name gourdian-postgres --restart unless-stopped -p 5432:5432 \
		-e POSTGRES_USER=postgres_user -e POSTGRES_PASSWORD=postgres_password -e POSTGRES_DB=graudit_test \
		-v gourdian-postgres-data:/var/lib/postgresql postgres:18-alpine
	@docker inspect gourdian-mongo-auth >/dev/null 2>&1 || { \
		docker run --rm -v gourdian-mongo-keyfile:/keyfile-dir --entrypoint bash mongo:8.3 -c \
			'[ -f /keyfile-dir/mongo-keyfile ] || { openssl rand -base64 756 > /keyfile-dir/mongo-keyfile && chmod 400 /keyfile-dir/mongo-keyfile && chown 999:999 /keyfile-dir/mongo-keyfile; }' && \
		docker run -d --name gourdian-mongo-auth --restart unless-stopped -p 27018:27018 \
			-e MONGO_INITDB_ROOT_USERNAME=root -e MONGO_INITDB_ROOT_PASSWORD=mongo_password \
			-v gourdian-mongo-keyfile:/etc/mongo-keyfile-dir -v gourdian-mongo-data:/data/db \
			mongo:8.3 --port 27018 --replSet rs0 --keyFile /etc/mongo-keyfile-dir/mongo-keyfile; }

docker-down:
	@docker stop $(SHARED_CONTAINERS) gourdian-mongo-standalone 2>/dev/null || true
	@echo "Stopped (containers preserved for a fast restart via 'make docker-up')"

guard-version:
	@if [ -z "$(VERSION)" ]; then \
		echo "VERSION is required (example: make release VERSION=v0.1.0)"; \
		exit 1; \
	fi

tag: guard-version
	@echo "Tagging $(VERSION)..."
	git tag $(VERSION)
	git push origin $(VERSION)
	@echo "Tagged and pushed $(VERSION)"

release: guard-version tag
	@echo "Releasing $(VERSION) with goreleaser..."
	@which goreleaser > /dev/null || (echo "goreleaser not found. Install with: go install github.com/goreleaser/goreleaser/v2@latest" && exit 1)
	goreleaser release --clean
	@echo "Released $(VERSION)"

# Dry run: validates .goreleaser.yaml and builds a snapshot release locally
# without requiring a git tag or pushing anything.
goreleaser-check:
	@which goreleaser > /dev/null || (echo "goreleaser not found. Install with: go install github.com/goreleaser/goreleaser/v2@latest" && exit 1)
	goreleaser check
	goreleaser release --snapshot --clean

.DEFAULT_GOAL := help
