# Go

**Mixed-authorship note, stated up front because it matters for defending this
project:** not every line below was hand-written by the project's author. The plan
(`docs/superpowers/plans/2026-09-22-snip-observability-stack.md`) explicitly put Tasks
2-6 in "Go Learning Mode" — concepts explained first, author writes the code, reviewed
after. In practice: `shortcode.go` (Task 2) and most of `main.go` (Task 6) were
author-written and reviewed. `store.go`'s `NewStore`/`Close` (Task 3) were
author-written; `CreateLink`/`GetLink`/`IncrementClicks` were written by the assistant
on explicit request after the author asked for the answer rather than debugging blind.
`handlers.go`'s `Index`/`Shorten` (Task 4) were author-written; `Redirect` was
author-written first, found to not compile, then fixed/rewritten by the assistant.
`metrics.go` (Task 5) was assistant-written entirely, after the response-writer-wrapper
trick was flagged as too much to derive unaided. This note explains the language
concepts and the traps found along the way — an interviewer asking "walk me through
this" should get this same honest breakdown, not a claim of solo authorship.

## What it is

Go is a compiled, statically-typed language from Google, designed for straightforward
concurrent networked services. Two things make it distinctive versus, say, Python or
JavaScript: **explicit error handling** (functions return `(value, error)` pairs that
callers check with `if err != nil`, instead of throwing exceptions) and a **small
standard library that already covers most of what a web service needs** — an HTTP
server, JSON encoding, SQL access, and templating are all in `net/http`,
`encoding/json`, `database/sql`, and `html/template`, no framework required. `snip`
uses none of Go's concurrency primitives (goroutines/channels) directly — the stdlib
HTTP server handles concurrent requests by running each one on its own goroutine
internally, invisibly to this codebase.

## How it is used here

The whole service is `package main` across seven files in `apps/snip/`: `shortcode.go`,
`store.go`, `handlers.go`, `metrics.go`, `main.go`, plus their five `_test.go`
counterparts — 650 total lines including tests, 14 top-level test functions (22
including `TestEncode`'s 8 table-driven subtests), all passing in 0.01s
(`go test ./... -v`).

**Error handling as values, not exceptions**, runs through every layer:
```go
// store.go
func (s *Store) GetLink(code string) (Link, error) {
	var link Link
	err := s.db.QueryRow("SELECT url, clicks FROM links WHERE code = ?", code).Scan(&link.URL, &link.Clicks)
	if errors.Is(err, sql.ErrNoRows) {
		return Link{}, ErrNotFound
	}
	...
```
`ErrNotFound` (`var ErrNotFound = errors.New("link not found")`) is a **sentinel
error** — a specific value other code checks for with `errors.Is`, rather than
inspecting an error's string message. It's how `store.go`'s SQLite-specific
`sql.ErrNoRows` gets translated into a package-level concept that `handlers.go` checks
without knowing SQLite is underneath.

**Interface embedding for the one real trick in this codebase**, in `metrics.go`:
```go
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
```
`http.ResponseWriter` is an interface with no way to read back what status code was
written. Embedding it (not as a named field — just the bare type) makes
`*statusRecorder` automatically satisfy the same interface by delegating every method
it doesn't override to the real writer underneath. Overriding only `WriteHeader`
intercepts the one call worth watching, while `Write` and `Header()` pass through
unchanged. This pattern (embed an interface, override one method) is used twice more
in `main.go`'s `buildMux` to detect whether `Shorten` returned 201 or `Redirect`
returned 302, so the right Prometheus counter fires only on success.

**Base conversion via div/mod**, in `shortcode.go`'s `Encode`:
```go
const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

func Encode(id int64) string {
	...
	for id > 0 {
		remainder := id % 62
		sb.WriteByte(alphabet[remainder])
		id /= 62
	}
	return reverseASCII(sb.String())
}
```
Same div/mod-collect-reverse algorithm as converting decimal to any other base, just
base 62 instead of base 2 or 16 — chosen because it's URL-safe and denser than base16,
so `Encode(124)` is `"20"` instead of a longer decimal or hex string.

**`net/http`'s pattern-based routing (Go 1.22+)**, in `main.go`:
```go
mux.HandleFunc("GET /{$}", metrics.Instrument("/", api.Index))
mux.HandleFunc("GET /{code}", metrics.Instrument("/{code}", ...))
```
`{$}` anchors to the exact root path only; `{code}` captures one path segment,
retrieved in the handler via `r.PathValue("code")`. Before Go 1.22 this needed a
third-party router (gorilla/mux, chi, etc.) — one less dependency here specifically
because of that stdlib addition.

## What someone would ask

**"Did you write all of this yourself?"** No — see the authorship note at the top.
The honest answer: wrote and understood `shortcode.go`'s algorithm and most of
`main.go`'s wiring myself; asked for the working implementation on `store.go`'s CRUD
methods, `handlers.go`'s `Redirect`, and all of `metrics.go` after review found real
bugs or after the response-writer-wrapper concept didn't click from explanation alone.
Reviewed and understood everything that landed either way, including two bugs an
agent's own first draft introduced (see below) — this wasn't blind copy-paste.

**"Walk me through a bug you found in your own code."** `handlers.go`'s first draft
of `Redirect` didn't compile: `Store.GetLink` returns a `Link` struct, and the draft
assigned that directly to a variable named `url`, then passed it straight to
`http.Redirect` as the third argument — which needs a `string`. Go's static typing
caught this at compile time (`cannot use url (variable of struct type Link) as string
value`), not at runtime with a bad redirect. The fix was reading `link.URL` off the
struct instead of the whole struct.

**"What's a mistake that would have shipped silently in a weaker language?"**
`main.go`'s first draft of the `BASE_URL` default had a trailing slash
(`"http://localhost:"+port+"/"`), while `handlers.go`'s `Shorten` independently builds
`ShortURL: a.BaseURL + "/" + code` — also assuming no trailing slash. Both pieces
compiled fine and all 13 tests passed, because the one test exercising this path
(`TestBuildMux_EndToEnd`) sets `api.BaseURL` directly, bypassing `main()`'s default
entirely. It only surfaced by actually running the built binary and hitting the real
endpoint: `curl -X POST localhost:18080/api/shorten ...` returned
`"short_url":"http://localhost:18080//1"` — a double slash, confirmed live, not caught
by any test. Fixed by removing the trailing slash from the default. This is the
argument for the plan's smoke-test step (Task 6, Step 5) existing at all — some bugs
only show up when the thing actually runs.

**"What was a false alarm during development?"** A `go vet` scare that wasn't a bug:
`string(alphabet[0])` in `shortcode.go` looked like it might trigger vet's
`stringintconv` warning (the classic Go footgun where `string(65)` silently gives you
`"A"` instead of `"65"`, because integer-to-string conversion treats the number as a
Unicode code point). It doesn't — `alphabet[0]` has type `byte`, and vet's
`stringintconv` check specifically excludes `byte`/`rune` conversions since those are
the legitimate "turn this one character into a string" case. `go vet ./...` runs
clean. Worth knowing the distinction rather than avoiding the pattern out of caution.

## What is not done

- **No goroutines or channels used directly anywhere in this codebase.** All
  concurrency (handling multiple simultaneous HTTP requests) comes for free from
  `net/http`'s server internals, not from anything written here. This project never
  needed to reach for Go's concurrency primitives directly — worth being honest about
  if asked "show me your concurrent Go code," since there isn't any beyond what the
  stdlib does invisibly.
- **No linter beyond `go vet` has been run** — no `golangci-lint`, no `staticcheck`.
  `go vet` catches a narrow set of known-bad patterns; a broader linter would likely
  surface more (unused error returns, e.g. `metrics.IncrementClicks`'s ignored error
  in `handlers.go`'s `Redirect`, deliberately ignored but never explicitly
  acknowledged with `_ = ...` or a comment).
- **No structured startup error handling** — `main.go` uses a bare `panic(err)` on
  `NewStore` failure rather than a logged message and clean `os.Exit(1)`. Fine for a
  demo binary that only ever runs interactively or under `kubectl`'s restart policy;
  a panic's stack trace in production logs is noisier than necessary for what's just
  "the DB file path was wrong."
- **No table-driven tests for `handlers.go` beyond the two `Shorten` cases and two
  `Redirect` cases already in the plan's provided test file** — no test for, e.g., a
  `Shorten` request with a missing `Content-Type` header, or a `Redirect` hitting a
  code with unicode in it (not that `Encode` can produce one, but nothing enforces
  that `r.PathValue("code")` won't).
- **Module cache integrity was compromised once, mid-project:** a method got
  hand-written into `client_golang`'s cached source instead of fixed in project code,
  caught via `go mod verify` reporting "dir has been modified," fixed by deleting and
  re-downloading that cached module version. Not a Go language gap, but a real
  incident worth being able to describe if asked "what went wrong during
  development."
