# graudit

Append-only, tamper-evident audit trail for the gourdian ecosystem.

graudit answers "what changed, who did it, and can we prove the record
hasn't been altered" — a different question from what
[grlog](https://github.com/gourdian25/grlog) answers ("what happened during
this request"). It is a library component that could contribute to
SOC2/HIPAA/PCI readiness, not a compliance certification product itself.

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
	"github.com/gourdian25/graudit/memory"
)

func main() {
	auditLog, err := memory.NewMemoryAuditLog()
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

Each backend lives in its own importable subpackage so consumers who only
need one backend don't pull in the others' client libraries.

| Backend | Package | Use case | Serialization |
|---|---|---|---|
| In-memory | `graudit/memory` | Test/dev only — never for anything you need to keep | `sync.Mutex` |
| PostgreSQL | `graudit/postgres` | Production | `pg_advisory_xact_lock` + explicitly-assigned `EntryID` |
| MongoDB | `graudit/mongo` | Production (requires a replica set) | Multi-document ACID transaction |

```go
// Postgres
import "github.com/gourdian25/graudit/postgres"
auditLog, err := postgres.NewPostgresAuditLog(postgres.PostgresConfig{
	DSN: "host=localhost user=myuser password=mypass dbname=mydb port=5432 sslmode=disable",
})

// MongoDB — must be a replica set (single-node is sufficient); construction
// fails fast otherwise, wrapping graudit.ErrReplicaSetRequired.
import "github.com/gourdian25/graudit/mongo"
auditLog, err := mongo.NewMongoAuditLog(mongo.MongoConfig{
	URI:      "mongodb://localhost:27017/?replicaSet=rs0",
	Database: "myapp",
})
```

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
auditLog, err := postgres.NewPostgresAuditLog(postgres.PostgresConfig{
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
auditLog, err := postgres.NewPostgresAuditLog(postgres.PostgresConfig{
	DSN:    dsn,
	Logger: logger, // *grlog.Logger satisfies graudit.Logger directly
})

// memory takes an Option instead of a Config field:
auditLog, err := memory.NewMemoryAuditLog(memory.WithLogger(logger))
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
mocks — mirroring the ecosystem's testing philosophy.

```sh
docker run -d --name graudit-postgres -p 5432:5432 \
  -e POSTGRES_USER=postgres_user -e POSTGRES_PASSWORD=postgres_password postgres:16
createdb -U postgres_user -h localhost graudit_test

# No auth env vars: MONGO_INITDB_ROOT_USERNAME/PASSWORD enables auth, which
# requires a keyFile once --replSet is also set ("security.keyFile is
# required when authorization is enabled with replica sets") — unnecessary
# complexity for a local test replica set, so this container runs without
# auth.
docker run -d --name graudit-mongo -p 27018:27017 mongo:7 --replSet rs0
docker exec graudit-mongo mongosh --eval 'rs.initiate()'

# A second, genuinely standalone (no --replSet) instance, required by
# TestNewMongoAuditLog_RequiresReplicaSet — the one test that actually
# proves construction fails fast against a non-replica-set deployment
# (see mongo/mongo_test.go's comment for why this test must never be
# skipped: an earlier version of this exact test caught a real bug where
# the fail-fast check silently passed against a standalone instance).
docker run -d --name graudit-mongo-standalone -p 27019:27017 mongo:7
```

The Mongo backend additionally requires the instance to be configured as a
replica set (single-node is sufficient) — construction fails fast against
a standalone instance.

```sh
make test             # go test -cover ./...
make race             # go test -race ./...  (mandatory before any commit touching the hash-chain or serialization code)
make coverage-check   # verify every package independently meets 80% coverage
make bench            # go test -bench=. -benchmem -benchtime=10s ./...
make lint             # golangci-lint run ./...
```

A shared `conformance` package runs one behavioral suite (hash-chain
integrity, concurrent-write ordering, deliberate-tamper detection, hash
determinism, grevents publish/publish-failure) against all three backends
through the `AuditLog` interface.

## Out of scope (v1)

Full SOC2/HIPAA/PCI/ISO certification tooling, real-time alerting (a
consumer's job, reacting to `"audit.recorded"`), auto-instrumentation
(every entry comes from an explicit `Record`/`RecordChange` call, never
automatic interception), cryptographic per-entry signatures (true
non-repudiation — see the precise claim above), and archival/retention
sweeping of old entries. See
[docs/plan/graudit-plan.md](docs/plan/graudit-plan.md) for the full
roadmap.

## License

MIT — see [LICENSE](LICENSE).
