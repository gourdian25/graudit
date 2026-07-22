# graudit

Append-only, tamper-evident audit trail for the gourdian ecosystem.

graudit answers "what changed, who did it, and can we prove the record
hasn't been altered" — a different question from what
[grlog](https://github.com/gourdian25/grlog) answers ("what happened during
this request"). It is a library component that could contribute to
SOC2/HIPAA/PCI readiness, not a compliance certification product itself.

## Part of the gourdian25 ecosystem

graudit is one of several small, independent Go libraries meant to be used
together:

- [gourdiantoken](https://github.com/gourdian25/gourdiantoken) — JWT
  access/refresh token issuance, verification, revocation, and rotation.
- [grlog](https://github.com/gourdian25/grlog) — zero-dependency structured
  logging; graudit's optional `Logger` interface is satisfied by it directly.
- [grcache](https://github.com/gourdian25/grcache) — backend-agnostic
  caching abstraction, the same interface-plus-multiple-backends pattern
  graudit uses, both flattened into a single package.
- [grevents](https://github.com/gourdian25/grevents) — an in-process event
  bus; graudit publishes an `"audit.recorded"` event through it on every
  successful write (see below).
- [grpolicy](https://github.com/gourdian25/grpolicy) — attribute-based
  policy evaluation (RBAC/ABAC), independent of any notion of "user" or
  "role".

## The precise claim (read this before trusting `Verify()`)

Hash-chaining proves **internal consistency**: nothing in the recorded
range was altered or removed without `Verify()` detecting it. It does
**not** prove *who* wrote a given entry beyond whatever `ActorID` the
caller supplied, and it does **not** protect against a privileged attacker
with direct database access regenerating the entire chain from scratch —
a hash chain only detects partial tampering, not wholesale regeneration by
someone who controls the storage. Don't let "tamper-evident" read as
"cryptographically un-forgeable."

## Install

```sh
go get github.com/gourdian25/graudit
```

## Quickstart

```go
import (
	"context"
	"log"

	"github.com/gourdian25/graudit"
)

func main() {
	auditLog, err := graudit.NewMemoryAuditLog()
	if err != nil {
		log.Fatal(err)
	}
	defer auditLog.Close()

	ctx := context.Background()
	id, err := auditLog.Record(ctx, graudit.AuditEvent{
		ActorID:    "user:42",
		EntityType: "invoice",
		EntityID:   "inv_123",
		Action:     "create",
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Println("recorded entry", id)
}
```

See [example/example.go](example/example.go) for a fuller runnable demo
(`Record`, `RecordChange`, `Verify`, `Query`).

## Backends

graudit is a flat, single package — every backend's constructor and
Config/Option type live directly in `github.com/gourdian25/graudit`, no
subpackages to import selectively.

| Backend | Constructor | Use case | Serialization |
|---|---|---|---|
| In-memory | `NewMemoryAuditLog` | Test/dev only — never for anything you need to keep | `sync.Mutex` |
| PostgreSQL | `NewPostgresAuditLog` | Production | `pg_advisory_xact_lock` + explicitly-assigned `EntryID` |
| MongoDB | `NewMongoAuditLog` | Production (requires a replica set) | Multi-document ACID transaction |

```go
// Postgres — pgx/v5 with sqlc-generated queries, no ORM.
auditLog, err := graudit.NewPostgresAuditLog(graudit.PostgresConfig{
	DSN: "host=localhost user=myuser password=mypass dbname=mydb port=5432 sslmode=disable",
})

// MongoDB — must be a replica set (single-node is sufficient); construction
// fails fast otherwise, wrapping graudit.ErrReplicaSetRequired.
auditLog, err := graudit.NewMongoAuditLog(graudit.MongoConfig{
	URI:      "mongodb://localhost:27017/?replicaSet=rs0",
	Database: "myapp",
})
```

### Why this shape

graudit used to split Postgres/Mongo/memory into separate importable
subpackages and used GORM for its Postgres backend. Both changed as part
of an ecosystem-wide standardization pass: the backends flattened into
this one root package, and GORM was replaced with `pgx/v5` +
sqlc-generated queries. See [CHANGELOG.md](CHANGELOG.md)'s `[Unreleased]`
entry for the full rationale.

## grevents integration

Every backend accepts an optional [`grevents.Bus`](https://github.com/gourdian25/grevents)
via its `Config`/`Option`, defaulting to `nil` — no bus configured means
`Record` simply doesn't publish, which is not an error. When configured,
every successful `Record`/`RecordChange` publishes one `"audit.recorded"`
event carrying the full recorded `AuditEvent` as `Payload`. A publish
failure is logged, never propagated as a `Record` error — the durable
write is graudit's guarantee, grevents delivery is a best-effort side
channel on top of it.

```go
import "github.com/gourdian25/grevents"

bus, _ := grevents.NewBus()
auditLog, err := graudit.NewPostgresAuditLog(graudit.PostgresConfig{
	DSN:      dsn,
	EventBus: bus,
})
```

## Logger

Every backend accepts an optional `graudit.Logger` (`Infof`/`Warnf`/`Errorf`)
for diagnostic messages — connection failures, grevents publish failures,
shutdown. A `nil` Logger (the default) means graudit logs nothing.
[`*grlog.Logger`](https://github.com/gourdian25/grlog) satisfies this
interface with no adapter, the same pattern grcache uses — see
`logger_test.go`'s `TestGrlogSatisfiesLoggerInterface` for the compile+run
proof, and [example/example.go](example/example.go) for a runnable demo.

```go
import "github.com/gourdian25/grlog"

logger := grlog.NewDefaultLogger()
auditLog, err := graudit.NewPostgresAuditLog(graudit.PostgresConfig{
	DSN:    dsn,
	Logger: logger, // *grlog.Logger satisfies graudit.Logger directly
})

// memory takes MemoryOption functional options instead of a Config field:
auditLog, err := graudit.NewMemoryAuditLog(graudit.WithLogger(logger))
```

## `Verify()` semantics

```go
ok, detail, err := auditLog.Verify(ctx, 1, latestID)
if err != nil {
	// operational failure — could not even attempt verification
}
if !ok {
	log.Printf("tampering detected at entry %d: expected %s, got %s",
		detail.BrokenAt, detail.Expected, detail.Actual)
}
```

`Verify` runs two checks per entry: that each entry's stored hash matches
one recomputed from its own stored fields, and that each entry's stored
`PrevHash` matches the immediately preceding entry's stored hash. See
[docs/architecture.md](docs/architecture.md) for why both are needed.

## Testing

graudit's own tests run against real local Postgres/MongoDB instances — no
mocks — mirroring the ecosystem's testing philosophy. These are the same
shared containers grnoti, grcache, and gourdiantoken test against (each
repo gets its own database) — start them with:

```sh
make docker-up   # starts the shared Postgres/Mongo(auth)/Mongo(standalone) test containers
make docker-down # stops them when you're done
```

The root package maintains 95.2% test coverage, enforced by a 95% gate
(`make coverage-check`).

The primary Mongo container is an authenticated single-node replica set —
the workspace-wide standard. A second, genuinely standalone (no `--replSet`,
no auth) instance is also started, required by
`TestNewMongoAuditLog_RequiresReplicaSet` — the one test that actually
proves construction fails fast against a non-replica-set deployment (see
`mongo_test.go`'s comment for why this test must never be skipped: an
earlier version of this exact test caught a real bug where the fail-fast
check silently passed against a standalone instance).

The Mongo backend additionally requires the instance to be configured as a
replica set (single-node is sufficient) — construction fails fast against
a standalone instance.

```sh
make test             # go test -cover ./...
make race             # go test -race ./...  (mandatory before any commit touching the hash-chain or serialization code)
make coverage-check   # verify the root package meets 95% coverage
make bench            # go test -bench=. -benchmem -benchtime=10s ./...
make lint             # golangci-lint run ./...
```

A shared contract test suite (`contract_audit_test.go`, run via
`TestAuditLog_Contract`'s per-backend subtests) runs one behavioral suite
(hash-chain integrity, concurrent-write ordering, deliberate-tamper
detection, hash determinism, grevents publish/publish-failure) against all
three backends through the `AuditLog` interface — folded from a standalone
`conformance` package into the root package's own tests, matching the rest
of the gourdian ecosystem's convention.

## Out of scope (v1)

Full SOC2/HIPAA/PCI/ISO certification tooling, real-time alerting (a
consumer's job, reacting to `"audit.recorded"`), auto-instrumentation
(every entry comes from an explicit `Record`/`RecordChange` call, never
automatic interception), cryptographic per-entry signatures (true
non-repudiation — see the precise claim above), and archival/retention
sweeping of old entries. See
[docs/plan/graudit-plan.md](docs/plan/graudit-plan.md) for the full
roadmap.

## Contributing

```sh
make fmt              # gofmt
make vet              # go vet
make lint              # golangci-lint (if installed)
make test               # go test -cover ./...
make race                # go test -race ./...  — mandatory before any PR touching the hash-chain or serialization code
make coverage-check        # the root package must meet 95%
```

See [CLAUDE.md](CLAUDE.md) for the full architecture rundown.

## License

MIT — see [LICENSE](LICENSE).

See [CHANGELOG.md](CHANGELOG.md) for release history and
[SECURITY.md](SECURITY.md) to report a vulnerability privately instead of
opening a public issue.
