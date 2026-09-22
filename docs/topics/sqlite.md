# SQLite (via `database/sql` + `modernc.org/sqlite`)

## What it is

SQLite is a database engine that lives entirely inside your application process as a
library — there's no separate server to run, no network connection, no daemon to
crash independently of your program. The whole database is one file on disk (or, for
tests, purely in RAM). This is different from Postgres/MySQL, where the database is a
separate process your app talks to over a socket.

`database/sql` is Go's standard, driver-agnostic API for talking to *any* SQL
database — the same `*sql.DB`, `Exec`, `QueryRow` calls work whether the underlying
driver is SQLite, Postgres, or MySQL. It doesn't know how to speak any particular
database's wire protocol itself; that's the driver's job.

`modernc.org/sqlite` is the specific driver used here. The more common SQLite driver
for Go, `mattn/go-sqlite3`, wraps the real C SQLite library via cgo (Go calling into
C code). `modernc.org` is a transpiled, pure-Go port — no C, no cgo.

## How it is used here

Everything lives in `apps/snip/store.go`. The driver is registered with a blank
import:

```go
_ "modernc.org/sqlite"
```

(`store.go:8`) — imported only for its `init()` side effect of registering itself
under the driver name `"sqlite"` with `database/sql`. Nothing in the file calls
`modernc.org/sqlite` directly; `sql.Open("sqlite", path)` (`store.go:32`) is the only
place the name is used, and it's a string, not a type — this only gets caught at
runtime, not compile time, if the import is missing or the name is misspelled.

Schema, created idempotently on every `NewStore` call via `CREATE TABLE IF NOT
EXISTS` (`store.go:12-18`):

```sql
CREATE TABLE IF NOT EXISTS links (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	code TEXT UNIQUE NOT NULL,
	url TEXT NOT NULL,
	clicks INTEGER NOT NULL DEFAULT 0,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

`NewStore` (`store.go:31-43`) does three things in order: `sql.Open` (just validates
arguments, doesn't touch the DB — `sql.Open` never fails just because the file/path
is bad), `db.Ping()` (forces the actual connection, so failures surface immediately
instead of on the first real query), then runs the schema.

`CreateLink` (`store.go:49-63`) is two statements, not one, because the short code
is derived from the row's autoincrement id, which SQLite only assigns at insert time:
insert with a placeholder `code = ''`, read `result.LastInsertId()`, compute
`Encode(id)` (Task 2's function, same package), then `UPDATE ... SET code = ? WHERE
id = ?`.

`GetLink` translates SQLite's "no row" signal into the package's own sentinel error:

```go
err := s.db.QueryRow(...).Scan(&link.URL, &link.Clicks)
if errors.Is(err, sql.ErrNoRows) {
	return Link{}, ErrNotFound
}
```

so callers never need to know SQLite is underneath — they just check
`errors.Is(err, ErrNotFound)`.

`IncrementClicks` does the increment inside the SQL statement —
`UPDATE links SET clicks = clicks + 1 WHERE code = ?` — rather than reading the
current count in Go, adding one, and writing it back. That avoids a read-modify-write
race between two concurrent redirects to the same code.

Tests (`store_test.go`) run against `NewStore(":memory:")` — SQLite's special path
that keeps the whole database in RAM for that connection's lifetime, never touching
disk. This is a real database engine executing real SQL, not a mock, so a broken
query or schema mistake fails the test the same way it'd fail in production. All 4
store tests plus both `Encode` tests pass (`go test ./... -v`, 0.005s total).

`go get modernc.org/sqlite` silently bumped `go.mod`'s `go` directive from `1.22`
(what the plan assumed) to `1.25.0` — the driver's own `go.mod` requires a newer
language version than the toolchain would otherwise default to for this module.

## What someone would ask

**"Why not just use Postgres, since that's what most production systems use?"**
Non-goal here is a separate database pod — the whole point of embedded SQLite is
staying inside the box's ~1GB RAM budget (see the design spec's Infrastructure
section) and removing one more component to run, monitor, and fail independently.
For a single-writer URL shortener with no need for concurrent-writer scaling, SQLite
is a legitimate production choice, not just a shortcut — the trade-off flips at the
point where you need multiple app instances writing to the same database
concurrently, since SQLite locks the whole database file per write.

**"Why the pure-Go driver instead of the standard cgo one?"**
`mattn/go-sqlite3` requires cgo, which means the C toolchain has to be available at
build time and the resulting binary can't be `CGO_ENABLED=0`. That matters because
the plan targets a `distroless/static` Docker base image (no libc, no shell, minimal
attack surface) — cgo binaries need libc at runtime, which `distroless/static`
doesn't have. `modernc.org/sqlite` is slower than the C original (it's a transpiled
port, not hand-optimized C) — for this project's request volume that's irrelevant;
at genuinely high QPS on the SQLite write path, that performance gap would matter
more than the image-size/build-simplicity win.

**"Walk me through what happens when two people paste a URL at the same time."**
Two `CreateLink` calls can each independently insert a row with `code = ''` before
either has run its `UPDATE`. SQLite's `UNIQUE` constraint on `code` allows this only
because it's momentary — the two inserts don't collide with each other on `code`
uniqueness *unless they land between each other's insert and update*, in which case
the second insert of `''` hits the `UNIQUE` constraint and fails outright with a
constraint-violation error, which `CreateLink` would currently just wrap and return
as a generic error, not retry. See "What is not done" below.

## What is not done

- **The placeholder-code race is real, not fixed.** Under concurrent `CreateLink`
  calls, the second `INSERT ... VALUES ('', ...)` can hit the `UNIQUE` constraint
  before the first call's `UPDATE` sets its real code, and the whole call fails. Not
  retried, not transaction-wrapped. Acceptable for a single-operator demo project
  with a load generator as its only concurrent writer; would need to become either a
  single SQL statement (impossible here, since the code depends on the id SQLite
  hasn't assigned yet) or a retry loop / transaction with a temporary unique
  placeholder (e.g. the id itself, not `''`) before this could take real concurrent
  traffic.
- **No connection pool tuning.** `database/sql` defaults (`SetMaxOpenConns` etc.)
  are untouched. SQLite only supports one writer at a time regardless, so this
  mostly doesn't matter yet, but hasn't been measured under the load generator's
  actual traffic pattern (Task 14).
- **No migrations system.** Schema changes would mean hand-editing the `CREATE
  TABLE IF NOT EXISTS` string and manually reasoning about what happens to
  already-deployed databases with the old schema. Fine at one table with no
  production data yet; wouldn't be at a second schema change post-launch.
- **No `WAL` mode configured.** SQLite's default journal mode blocks readers during
  writes; `PRAGMA journal_mode=WAL` would let reads and writes overlap. Not
  configured — hasn't caused an observed problem yet since traffic is low, but is
  the standard first tuning step for a real SQLite deployment.
- **Storage location for the deployed instance isn't wired up yet.** `NewStore`
  takes any path including `:memory:`; Task 9 (Kubernetes manifests) is what
  actually decides the on-disk path and PersistentVolume for the real deployment —
  not yet built.
