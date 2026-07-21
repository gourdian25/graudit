# File: Makefile

# File: Makefile

.PHONY: help test race coverage coverage-summary coverage-check bench lint vet fmt clean deps docker-up docker-down tag release goreleaser-check guard-version

GO := go
MODULE := github.com/gourdian25/graudit
COVERAGE_MIN := 80
VERSION ?=

help:
	@echo "Makefile targets for graudit:"
	@echo ""
	@echo "  make test             Run all tests"
	@echo "  make race             Run tests with race detector (mandatory before any commit touching the hash-chain or serialization code)"
	@echo "  make coverage         Generate HTML coverage report"
	@echo "  make coverage-summary Show coverage summary by function"
	@echo "  make coverage-check   Check each package meets the $(COVERAGE_MIN)% threshold"
	@echo "  make bench            Run benchmarks"
	@echo "  make lint             Run linters (requires golangci-lint)"
	@echo "  make vet              Run go vet"
	@echo "  make fmt              Format code"
	@echo "  make clean            Clean build artifacts"
	@echo "  make deps             Verify and tidy dependencies"
	@echo "  make docker-up        Start the shared Postgres/Mongo test containers (idempotent)"
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

# Requires local Postgres/MongoDB (see README) — every non-test-helper
# package must independently meet COVERAGE_MIN, matching grcache's own
# per-package coverage-check convention. The conformance package (no
# _test.go of its own) and example (a runnable demo, not library code
# under test) are skipped.
coverage-check:
	@echo "Checking each package meets $(COVERAGE_MIN)% coverage..."
	@fail=0; \
	for pkg in . ./memory ./postgres ./mongo; do \
		out=$$($(GO) test -cover $$pkg 2>&1); \
		pct=$$(echo "$$out" | grep -o '[0-9.]*%' | tr -d '%'); \
		if [ -z "$$pct" ]; then echo "FAIL $$pkg: no coverage output"; fail=1; continue; fi; \
		below=$$(awk -v p="$$pct" -v m="$(COVERAGE_MIN)" 'BEGIN { print (p < m) ? 1 : 0 }'); \
		if [ "$$below" = "1" ]; then \
			echo "FAIL $$pkg: $$pct% is below $(COVERAGE_MIN)% threshold"; fail=1; \
		else \
			echo "OK $$pkg: $$pct%"; \
		fi; \
	done; \
	exit $$fail

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

# docker-up is idempotent: safe to run repeatedly, and safe to run
# alongside grnoti/grcache/gourdiantoken's own `make docker-up` since every
# gourdian25 repo shares these same container names/ports — each just
# gets its own database inside them (see CLAUDE.md). graudit is the only
# repo needing the second, standalone (no-auth, no-replset) Mongo
# container — required by TestNewMongoAuditLog_RequiresReplicaSet, which
# must never be skipped (see CLAUDE.md for why).
docker-up:
	@echo "Starting shared test containers..."
	@docker inspect gourdian-postgres >/dev/null 2>&1 || docker run -d --name gourdian-postgres -p 5432:5432 \
		-e POSTGRES_USER=postgres_user -e POSTGRES_PASSWORD=postgres_password -e POSTGRES_DB=graudit_test postgres:16
	@docker start gourdian-postgres >/dev/null 2>&1 || true
	@docker volume create gourdian-mongo-keyfile >/dev/null
	@docker inspect gourdian-mongo-auth >/dev/null 2>&1 || (docker run --rm -v gourdian-mongo-keyfile:/keyfile-dir mongo:7 bash -c "openssl rand -base64 756 > /keyfile-dir/mongo-keyfile && chmod 400 /keyfile-dir/mongo-keyfile && chown 999:999 /keyfile-dir/mongo-keyfile" && docker run -d --name gourdian-mongo-auth -p 27018:27017 -e MONGO_INITDB_ROOT_USERNAME=root -e MONGO_INITDB_ROOT_PASSWORD=mongo_password -v gourdian-mongo-keyfile:/etc/mongo-keyfile-dir mongo:7 --replSet rs0 --keyFile /etc/mongo-keyfile-dir/mongo-keyfile)
	@docker start gourdian-mongo-auth >/dev/null 2>&1 || true
	@docker inspect gourdian-mongo-standalone >/dev/null 2>&1 || docker run -d --name gourdian-mongo-standalone -p 27019:27017 mongo:7
	@docker start gourdian-mongo-standalone >/dev/null 2>&1 || true
	@echo "Waiting for Postgres..."
	@until docker exec gourdian-postgres pg_isready -U postgres_user >/dev/null 2>&1; do sleep 1; done
	@docker exec gourdian-postgres psql -U postgres_user -d postgres -tc "SELECT 1 FROM pg_database WHERE datname = 'graudit_test'" | grep -q 1 || \
		docker exec gourdian-postgres psql -U postgres_user -d postgres -c "CREATE DATABASE graudit_test"
	@echo "Waiting for Mongo (auth + replica set)..."
	@until docker exec gourdian-mongo-auth mongosh --quiet -u root -p mongo_password --authenticationDatabase admin --eval 'db.runCommand({ping:1})' >/dev/null 2>&1; do sleep 1; done
	@docker exec gourdian-mongo-auth mongosh --quiet -u root -p mongo_password --authenticationDatabase admin --eval 'rs.initiate()' >/dev/null 2>&1 || true
	@echo "Waiting for Mongo (standalone)..."
	@until docker exec gourdian-mongo-standalone mongosh --quiet --eval 'db.runCommand({ping:1})' >/dev/null 2>&1; do sleep 1; done
	@echo "Docker test infrastructure ready (postgres/mongo-auth/mongo-standalone)"

docker-down:
	@docker stop gourdian-postgres gourdian-mongo-auth gourdian-mongo-standalone 2>/dev/null || true
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
