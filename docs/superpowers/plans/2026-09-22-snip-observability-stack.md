# snip Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Do NOT use superpowers:subagent-driven-development for Tasks 2-6 (see "Go Learning Mode" below) — those require live, interactive teaching with the human, which a dispatched subagent cannot do. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build and deploy `snip`, a URL shortener, with Terraform-provisioned k3s infrastructure, a hand-rolled Prometheus/Grafana/Alertmanager observability stack, and GitHub Actions CI/CD.

**Architecture:** A single Go binary (`snip`) serves an HTML form, a JSON API, and redirects, backed by embedded SQLite, instrumented with Prometheus metrics. It runs in k3s on a single free-tier EC2 instance provisioned by Terraform. GitHub Actions tests/builds/pushes the image and rolls it out via SSH + `kubectl set image`. Prometheus/Grafana/Alertmanager/node-exporter are deployed as plain manifests (no Helm) to stay inside the box's 1GB RAM budget.

**Tech Stack:** Go 1.22 (stdlib `net/http` with pattern-based `ServeMux`), `modernc.org/sqlite` (pure-Go, no CGO — keeps the Docker image on `distroless/static`), `github.com/prometheus/client_golang`, Terraform (AWS provider), k3s, Prometheus/Grafana/Alertmanager (raw manifests), GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-09-22-snip-observability-stack-design.md`

## Global Constraints

- No CGO in the Go build — use `modernc.org/sqlite`, not `mattn/go-sqlite3` — so the final image can be `CGO_ENABLED=0` on `distroless/static`.
- Target instance has ~1GB RAM total (k3s + all pods) — every Deployment gets explicit, small resource requests/limits.
- No public ingress: only port 22 (SSH) is opened by the security group. All human access to `snip`/Grafana/Prometheus is via `kubectl port-forward` over SSH.
- Terraform state is local (`terraform.tfstate` in `/infra`, gitignored) and `apply`/`destroy` are run manually from the operator's machine — never from CI.
- No secrets committed to git: the Alertmanager Discord webhook and the Dockerhub/SSH GitHub Actions secrets are never checked in.

---

## Go Learning Mode (Tasks 2-6)

The user is learning Go through this project and wants to **write the Go implementation themselves**, not have it written for them (confirmed decision — see project memory `collab-go-learning`). For Tasks 2-6, this plan intentionally does **not** provide the implementation code. Instead, each step provides:

- The exact test code (defines the required behavior/contract).
- The exact function/type signatures the implementation must satisfy (so later tasks can rely on them).
- A list of the Go concepts needed for that step, to be explained before the user writes any code.

**Execution for these tasks:** work through them inline, in conversation. Before each implementation step, explain the listed concepts in plain terms with a small standalone example if useful, then let the user write the actual function. Review their code against the test and the required signature; point out bugs and idiom issues rather than rewriting it for them, unless they explicitly ask for the answer.

Tasks 1 and 7-17 have no such restriction — implement them directly.

---

### Task 1: Go module scaffold and directory layout

**Files:**
- Create: `apps/snip/go.mod`
- Create: `apps/snip/.gitignore`
- Create: `apps/snip/templates/index.html.tmpl` (placeholder, filled properly in Task 4)

**Interfaces:**
- Produces: a Go module named `snip`, Go version 1.22, ready for `go get`.

- [ ] **Step 1: Create the module**

```bash
mkdir -p apps/snip/templates
cd apps/snip
go mod init snip
```

- [ ] **Step 2: Add build artifacts to .gitignore**

`apps/snip/.gitignore`:
```
/snip
*.db
```

- [ ] **Step 3: Add a placeholder template so the directory is tracked**

`apps/snip/templates/index.html.tmpl`:
```html
<!-- replaced in Task 4 -->
```

- [ ] **Step 4: Commit**

```bash
git add apps/snip
git commit -m "chore: scaffold snip Go module"
```

---

### Task 2: Short code generation

**Concepts to explain first:** Go packages and files, `const`, byte/rune vs string indexing, `strings.Builder`, why base62 (URL-safe alphabet) is a reasonable choice for short codes, integer division/modulo for base conversion, table-driven tests (`[]struct{...}`), `t.Run` subtests.

**Files:**
- Create: `apps/snip/shortcode.go`
- Test: `apps/snip/shortcode_test.go`

**Interfaces:**
- Produces: `func Encode(id int64) string` — converts a positive integer ID into a base62 string. Later tasks (Task 3) call this after inserting a row to compute the code to store.

- [ ] **Step 1: Write the failing test**

`apps/snip/shortcode_test.go`:
```go
package main

import "testing"

func TestEncode(t *testing.T) {
	cases := []struct {
		id   int64
		want string
	}{
		{1, "1"},
		{9, "9"},
		{10, "a"},
		{35, "z"},
		{36, "A"},
		{61, "Z"},
		{62, "10"},
		{124, "20"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			got := Encode(c.id)
			if got != c.want {
				t.Errorf("Encode(%d) = %q, want %q", c.id, got, c.want)
			}
		})
	}
}

func TestEncode_Uniqueness(t *testing.T) {
	seen := map[string]bool{}
	for id := int64(1); id <= 5000; id++ {
		code := Encode(id)
		if seen[code] {
			t.Fatalf("duplicate code %q for id %d", code, id)
		}
		seen[code] = true
	}
}
```

The alphabet ordering that produces these exact expected values is
`"0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"` (digits,
then lowercase, then uppercase), with standard base conversion (repeatedly
divide by 62, collect remainders, reverse).

- [ ] **Step 2: Run test to verify it fails**

Run: `cd apps/snip && go test ./... -run TestEncode -v`
Expected: FAIL — `Encode` is not declared.

- [ ] **Step 3: User writes `Encode` in `shortcode.go`**

Required signature: `func Encode(id int64) string`. Must produce the exact
mapping shown in the test table above. Guardrail: `id <= 0` will never be
called in this codebase (SQLite `AUTOINCREMENT` starts at 1), so it doesn't
need to handle zero/negative specially — but it's fine to note that as a
known limitation.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd apps/snip && go test ./... -run TestEncode -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add apps/snip/shortcode.go apps/snip/shortcode_test.go
git commit -m "feat(snip): add base62 short code encoding"
```

---

### Task 3: SQLite storage layer

**Concepts to explain first:** `database/sql` (driver-agnostic API), blank import for driver registration (`_ "modernc.org/sqlite"`), `sql.Open` vs actually connecting, prepared statements via `db.Exec`/`db.QueryRow`, `sql.ErrNoRows`, wrapping errors with `fmt.Errorf("...: %w", err)`, custom sentinel errors (`var ErrNotFound = errors.New(...)`), why `id, err := result.LastInsertId()`, struct embedding for a `Store` type, `defer db.Close()`, table-driven tests against a real in-memory database (`:memory:`) rather than mocks.

**Files:**
- Create: `apps/snip/store.go`
- Test: `apps/snip/store_test.go`
- Modify: `apps/snip/go.mod` (add `modernc.org/sqlite` dependency)

**Interfaces:**
- Consumes: `Encode(id int64) string` from Task 2.
- Produces (used by Task 4's handlers and Task 6's wiring):
  - `type Link struct { URL string; Clicks int64 }`
  - `var ErrNotFound = errors.New("link not found")`
  - `func NewStore(path string) (*Store, error)`
  - `func (s *Store) Close() error`
  - `func (s *Store) CreateLink(url string) (code string, err error)`
  - `func (s *Store) GetLink(code string) (Link, error)` — returns `ErrNotFound` if the code doesn't exist
  - `func (s *Store) IncrementClicks(code string) error`

Schema (created by `NewStore` if the table doesn't exist):
```sql
CREATE TABLE IF NOT EXISTS links (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    code TEXT UNIQUE NOT NULL,
    url TEXT NOT NULL,
    clicks INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

`CreateLink` flow: insert a row with `url` and a temporary empty `code`,
read back `LastInsertId()`, compute `code := Encode(id)`, `UPDATE` the row
to set the real code, return it.

- [ ] **Step 1: Add the dependency**

```bash
cd apps/snip
go get modernc.org/sqlite
```

- [ ] **Step 2: Write the failing tests**

`apps/snip/store_test.go`:
```go
package main

import (
	"errors"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCreateAndGetLink(t *testing.T) {
	s := newTestStore(t)

	code, err := s.CreateLink("https://example.com")
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	if code == "" {
		t.Fatal("expected non-empty code")
	}

	link, err := s.GetLink(code)
	if err != nil {
		t.Fatalf("GetLink: %v", err)
	}
	if link.URL != "https://example.com" {
		t.Errorf("URL = %q, want %q", link.URL, "https://example.com")
	}
	if link.Clicks != 0 {
		t.Errorf("Clicks = %d, want 0", link.Clicks)
	}
}

func TestGetLink_NotFound(t *testing.T) {
	s := newTestStore(t)

	_, err := s.GetLink("doesnotexist")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestIncrementClicks(t *testing.T) {
	s := newTestStore(t)

	code, _ := s.CreateLink("https://example.com")

	if err := s.IncrementClicks(code); err != nil {
		t.Fatalf("IncrementClicks: %v", err)
	}
	if err := s.IncrementClicks(code); err != nil {
		t.Fatalf("IncrementClicks: %v", err)
	}

	link, err := s.GetLink(code)
	if err != nil {
		t.Fatalf("GetLink: %v", err)
	}
	if link.Clicks != 2 {
		t.Errorf("Clicks = %d, want 2", link.Clicks)
	}
}

func TestDifferentURLsGetDifferentCodes(t *testing.T) {
	s := newTestStore(t)

	code1, _ := s.CreateLink("https://example.com/one")
	code2, _ := s.CreateLink("https://example.com/two")
	if code1 == code2 {
		t.Errorf("expected different codes, got %q twice", code1)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd apps/snip && go test ./... -run 'TestCreateAndGetLink|TestGetLink_NotFound|TestIncrementClicks|TestDifferentURLsGetDifferentCodes' -v`
Expected: FAIL — `Store`, `NewStore`, etc. not declared.

- [ ] **Step 4: User writes `store.go`**

Matching the interfaces and schema listed above exactly (later tasks depend
on these exact names and types).

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd apps/snip && go test ./... -v`
Expected: PASS (all tests including Task 2's)

- [ ] **Step 6: Commit**

```bash
git add apps/snip/store.go apps/snip/store_test.go apps/snip/go.mod apps/snip/go.sum
git commit -m "feat(snip): add SQLite-backed link storage"
```

---

### Task 4: HTTP handlers

**Concepts to explain first:** `net/http.Handler`/`HandlerFunc`, Go 1.22's enhanced `ServeMux` patterns (`"GET /{code}"`, `r.PathValue("code")`), `encoding/json` decode/encode, `http.Error`, setting status codes with `w.WriteHeader`, `net/url.ParseRequestURI` for validating an incoming URL, `html/template` (auto-escaping, `template.Must(template.ParseFiles(...))`), `httptest.NewRequest`/`httptest.NewRecorder` for handler tests without a real server.

**Files:**
- Create: `apps/snip/handlers.go`
- Create: `apps/snip/templates/index.html.tmpl` (real content, replaces Task 1's placeholder)
- Test: `apps/snip/handlers_test.go`

**Interfaces:**
- Consumes: `*Store` and its methods from Task 3 (`CreateLink`, `GetLink`, `IncrementClicks`, `ErrNotFound`).
- Produces (used by Task 6's wiring):
  - `type API struct { Store *Store; BaseURL string }`
  - `func NewAPI(store *Store, baseURL string) *API`
  - `func (a *API) Index(w http.ResponseWriter, r *http.Request)` — serves the HTML page
  - `func (a *API) Shorten(w http.ResponseWriter, r *http.Request)` — `POST`, body `{"url": "..."}`, responds `{"code": "...", "short_url": "..."}` with 201, or 400 on invalid/missing URL
  - `func (a *API) Redirect(w http.ResponseWriter, r *http.Request)` — reads `r.PathValue("code")`, 302-redirects to the stored URL and increments clicks, or 404 if not found

- [ ] **Step 1: Write the HTML template**

`apps/snip/templates/index.html.tmpl`:
```html
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>snip</title>
  <style>
    body { font-family: system-ui, sans-serif; max-width: 32rem; margin: 4rem auto; padding: 0 1rem; }
    input, button { font-size: 1rem; padding: 0.5rem; }
    input { width: 70%; }
    #result { margin-top: 1rem; word-break: break-all; }
  </style>
</head>
<body>
  <h1>snip</h1>
  <form id="f">
    <input id="url" type="url" placeholder="https://example.com/very/long/link" required>
    <button type="submit">Shorten</button>
  </form>
  <div id="result"></div>
  <script>
    document.getElementById('f').addEventListener('submit', async (e) => {
      e.preventDefault();
      const url = document.getElementById('url').value;
      const res = await fetch('/api/shorten', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ url }),
      });
      const div = document.getElementById('result');
      if (!res.ok) {
        div.textContent = 'Error: ' + (await res.text());
        return;
      }
      const data = await res.json();
      div.innerHTML = 'Short link: <a href="' + data.short_url + '">' + data.short_url + '</a>';
    });
  </script>
</body>
</html>
```

- [ ] **Step 2: Write the failing tests**

`apps/snip/handlers_test.go`:
```go
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestAPI(t *testing.T) *API {
	t.Helper()
	store := newTestStore(t)
	return NewAPI(store, "http://localhost:8080")
}

func TestShorten_ValidURL(t *testing.T) {
	api := newTestAPI(t)

	body := strings.NewReader(`{"url":"https://example.com"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/shorten", body)
	rec := httptest.NewRecorder()

	api.Shorten(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp struct {
		Code     string `json:"code"`
		ShortURL string `json:"short_url"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Code == "" || resp.ShortURL != "http://localhost:8080/"+resp.Code {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestShorten_InvalidURL(t *testing.T) {
	api := newTestAPI(t)

	body := strings.NewReader(`{"url":"not-a-url"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/shorten", body)
	rec := httptest.NewRecorder()

	api.Shorten(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestRedirect_KnownCode(t *testing.T) {
	api := newTestAPI(t)
	code, err := api.Store.CreateLink("https://example.com/target")
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/"+code, nil)
	req.SetPathValue("code", code)
	rec := httptest.NewRecorder()

	api.Redirect(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if loc := rec.Header().Get("Location"); loc != "https://example.com/target" {
		t.Errorf("Location = %q, want %q", loc, "https://example.com/target")
	}

	link, err := api.Store.GetLink(code)
	if err != nil {
		t.Fatalf("GetLink: %v", err)
	}
	if link.Clicks != 1 {
		t.Errorf("Clicks = %d, want 1", link.Clicks)
	}
}

func TestRedirect_UnknownCode(t *testing.T) {
	api := newTestAPI(t)

	req := httptest.NewRequest(http.MethodGet, "/doesnotexist", nil)
	req.SetPathValue("code", "doesnotexist")
	rec := httptest.NewRecorder()

	api.Redirect(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd apps/snip && go test ./... -run 'TestShorten|TestRedirect' -v`
Expected: FAIL — `API`, `NewAPI`, etc. not declared.

- [ ] **Step 4: User writes `handlers.go`**

Matching the interfaces above. `Index` should `template.Must(template.ParseFiles("templates/index.html.tmpl"))` once (package-level or in `NewAPI`) and `Execute` it with no data needed.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd apps/snip && go test ./... -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add apps/snip/handlers.go apps/snip/handlers_test.go apps/snip/templates/index.html.tmpl
git commit -m "feat(snip): add HTTP handlers for shorten/redirect/index"
```

---

### Task 5: Prometheus metrics + instrumentation middleware

**Concepts to explain first:** what a Prometheus client library actually does (in-process counters/histograms scraped over HTTP, not push-based), `prometheus.Registry` vs the global default registry (and why using a fresh one per test avoids "duplicate metrics collector registration" panics), `CounterVec`/`HistogramVec` and label cardinality, HTTP middleware as `func(http.HandlerFunc) http.HandlerFunc`, wrapping `http.ResponseWriter` to capture the status code (the stdlib doesn't expose it directly), `defer` with a captured start time for measuring duration, `github.com/prometheus/client_golang/prometheus/testutil` for asserting on metric values in tests.

**Files:**
- Create: `apps/snip/metrics.go`
- Test: `apps/snip/metrics_test.go`
- Modify: `apps/snip/go.mod` (add `github.com/prometheus/client_golang`)

**Interfaces:**
- Produces (used by Task 6's wiring):
  - `type Metrics struct { ... }` (fields unexported; exact metric names below are what Task 11's Prometheus scrape/alert rules query against, so they must match exactly)
  - `func NewMetrics(reg prometheus.Registerer) *Metrics` — creates and registers:
    - `http_requests_total` (CounterVec, labels: `route`, `method`, `status`)
    - `http_request_duration_seconds` (HistogramVec, labels: `route`, `method`)
    - `links_created_total` (Counter)
    - `redirects_total` (Counter)
  - `func (m *Metrics) Instrument(route string, next http.HandlerFunc) http.HandlerFunc` — wraps a handler, recording request count and duration on every call
  - `func (m *Metrics) LinkCreated()` / `func (m *Metrics) Redirected()` — called explicitly by `Shorten`/`Redirect` in Task 4's handlers (Task 6 wires this in)

- [ ] **Step 1: Add the dependency**

```bash
cd apps/snip
go get github.com/prometheus/client_golang/prometheus
go get github.com/prometheus/client_golang/prometheus/promhttp
```

- [ ] **Step 2: Write the failing tests**

`apps/snip/metrics_test.go`:
```go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestInstrument_RecordsRequestCount(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	handler := m.Instrument("/hello", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/hello", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	got := testutil.ToFloat64(m.requests.WithLabelValues("/hello", "GET", "200"))
	if got != 1 {
		t.Errorf("http_requests_total = %v, want 1", got)
	}
}

func TestInstrument_DefaultsToStatus200WhenWriteHeaderNotCalled(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	handler := m.Instrument("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok")) // no explicit WriteHeader call
	})

	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	got := testutil.ToFloat64(m.requests.WithLabelValues("/ok", "GET", "200"))
	if got != 1 {
		t.Errorf("http_requests_total = %v, want 1", got)
	}
}

func TestLinkCreatedAndRedirected(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	m.LinkCreated()
	m.LinkCreated()
	m.Redirected()

	if got := testutil.ToFloat64(m.linksCreated); got != 2 {
		t.Errorf("links_created_total = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.redirects); got != 1 {
		t.Errorf("redirects_total = %v, want 1", got)
	}
}
```

Note: the test refers to `m.requests`, `m.linksCreated`, `m.redirects` as
unexported fields — that's fine since the test file lives in the same
package (`package main`).

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd apps/snip && go test ./... -run 'TestInstrument|TestLinkCreatedAndRedirected' -v`
Expected: FAIL — `Metrics`, `NewMetrics` not declared.

- [ ] **Step 4: User writes `metrics.go`**

Key trap to flag before they start: `http.ResponseWriter` doesn't expose the
status code that was written. They need a small wrapper struct embedding
`http.ResponseWriter` that overrides `WriteHeader(code int)` to record the
code, and defaults to 200 if `Write` is called without an explicit
`WriteHeader` first (matching the second test above).

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd apps/snip && go test ./... -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add apps/snip/metrics.go apps/snip/metrics_test.go apps/snip/go.mod apps/snip/go.sum
git commit -m "feat(snip): add Prometheus metrics and instrumentation middleware"
```

---

### Task 6: main.go wiring

**Concepts to explain first:** `os.Getenv` with a fallback helper, `flag`-free config-via-env-vars as a 12-factor-app convention, building an `http.ServeMux` and registering method+pattern routes (`mux.HandleFunc("GET /{$}", ...)` for exact-root-only matching vs `"GET /{code}"`), `promhttp.HandlerFor(reg, ...)` vs the global `promhttp.Handler()`, why `log.Fatal(http.ListenAndServe(...))` is an acceptable stopping point for a demo (no graceful shutdown — deliberately deferred, not an oversight), why `main()` itself is hard to unit test and so wiring is split into a separate testable function.

**Files:**
- Create: `apps/snip/main.go`
- Test: `apps/snip/main_test.go`

**Interfaces:**
- Consumes: `NewStore` (Task 3), `NewAPI` (Task 4), `NewMetrics`/`Instrument`/`LinkCreated`/`Redirected` (Task 5).
- Produces: `func buildMux(api *API, metrics *Metrics) *http.ServeMux` — the full route table, used directly by `main()` and by the end-to-end test below.

Routes `buildMux` must register:
- `GET /{$}` → `metrics.Instrument("/", api.Index)`
- `POST /api/shorten` → `metrics.Instrument("/api/shorten", api.Shorten)` (handler must call `metrics.LinkCreated()` on success — wire this by having `Shorten` take/call it, or have `buildMux` chain a small closure; either is fine as long as the end-to-end test below passes)
- `GET /{code}` → `metrics.Instrument("/{code}", api.Redirect)` (handler must call `metrics.Redirected()` on a successful redirect)
- `GET /metrics` → `promhttp.HandlerFor(reg, promhttp.HandlerOpts{})`

Env vars read by `main()`: `PORT` (default `8080`), `DB_PATH` (default
`./snip.db`), `BASE_URL` (default `http://localhost:PORT`).

- [ ] **Step 1: Write the failing end-to-end test**

`apps/snip/main_test.go`:
```go
package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestBuildMux_EndToEnd(t *testing.T) {
	store := newTestStore(t)
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)
	api := NewAPI(store, "") // BaseURL filled below once we know the server's URL

	mux := buildMux(api, metrics)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	api.BaseURL = srv.URL

	// Create a link.
	resp, err := http.Post(srv.URL+"/api/shorten", "application/json", strings.NewReader(`{"url":"https://example.com"}`))
	if err != nil {
		t.Fatalf("POST /api/shorten: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	// Follow the redirect manually (don't let the client auto-follow).
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	getRoot, _ := client.Get(srv.URL + "/")
	if getRoot.StatusCode != http.StatusOK {
		t.Errorf("GET / status = %d, want 200", getRoot.StatusCode)
	}

	metricsResp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	body, _ := io.ReadAll(metricsResp.Body)
	if !strings.Contains(string(body), "links_created_total 1") {
		t.Errorf("/metrics does not show links_created_total 1:\n%s", body)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd apps/snip && go test ./... -run TestBuildMux_EndToEnd -v`
Expected: FAIL — `buildMux` not declared.

- [ ] **Step 3: User writes `buildMux` and `main()` in `main.go`**

Matching the routes and env vars listed above.

- [ ] **Step 4: Run all tests to verify they pass**

Run: `cd apps/snip && go test ./... -v`
Expected: PASS (every test from Tasks 2-6)

- [ ] **Step 5: Manual smoke check**

```bash
cd apps/snip
go build -o snip .
DB_PATH=:memory: ./snip &
curl -s -X POST localhost:8080/api/shorten -d '{"url":"https://example.com"}'
kill %1
```
Expected: JSON response with a `code` and `short_url`.

- [ ] **Step 6: Commit**

```bash
git add apps/snip/main.go apps/snip/main_test.go
git commit -m "feat(snip): wire up HTTP server and routes"
```

---

### Task 7: Dockerfile

**Files:**
- Create: `apps/snip/Dockerfile`
- Create: `apps/snip/.dockerignore`

**Interfaces:**
- Produces: a container image exposing port 8080, used by Task 9's Deployment and Task 15's CI workflow.

- [ ] **Step 1: Write `.dockerignore`**

`apps/snip/.dockerignore`:
```
*.db
snip
```

- [ ] **Step 2: Write the Dockerfile**

`apps/snip/Dockerfile`:
```dockerfile
FROM golang:1.22 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/snip .

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/snip /snip
COPY --from=build /src/templates /templates
WORKDIR /
EXPOSE 8080
ENTRYPOINT ["/snip"]
```

- [ ] **Step 3: Build and smoke-test locally**

```bash
cd apps/snip
docker build -t snip:local .
docker run --rm -p 8080:8080 -e DB_PATH=/tmp/snip.db snip:local &
sleep 1
curl -s -X POST localhost:8080/api/shorten -d '{"url":"https://example.com"}'
docker stop $(docker ps -q --filter ancestor=snip:local)
```
Expected: JSON response with a `code` and `short_url`.

- [ ] **Step 4: Commit**

```bash
git add apps/snip/Dockerfile apps/snip/.dockerignore
git commit -m "build(snip): add multi-stage Dockerfile"
```

---

### Task 8: Terraform infrastructure

**Files:**
- Create: `infra/main.tf`
- Create: `infra/variables.tf`
- Create: `infra/network.tf`
- Create: `infra/compute.tf`
- Create: `infra/outputs.tf`
- Create: `infra/user_data.sh`
- Create: `infra/.gitignore`

**Interfaces:**
- Produces: an EC2 instance running k3s, reachable via `terraform output ssh_command`, whose `kubeconfig` is at `/etc/rancher/k3s/k3s.yaml` (and copied to `/home/ubuntu/.kube/config`) once booted. Task 9 onward assumes `kubectl` works once SSH'd in (or via a copied-out kubeconfig).

- [ ] **Step 1: `.gitignore` the local state and any generated files**

`infra/.gitignore`:
```
.terraform/
*.tfstate
*.tfstate.backup
.terraform.lock.hcl
```

- [ ] **Step 2: Provider and version constraints**

`infra/main.tf`:
```hcl
terraform {
  required_version = ">= 1.5.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = var.aws_region
}
```

- [ ] **Step 3: Variables**

`infra/variables.tf`:
```hcl
variable "aws_region" {
  type    = string
  default = "us-east-1"
}

variable "instance_type" {
  type    = string
  default = "t3.micro"
}

variable "ssh_allowed_cidr" {
  description = "Your IP in CIDR form, e.g. 203.0.113.5/32"
  type        = string
}

variable "public_key_path" {
  type    = string
  default = "~/.ssh/id_ed25519.pub"
}
```

- [ ] **Step 4: Network (default VPC + security group, SSH only)**

`infra/network.tf`:
```hcl
data "aws_vpc" "default" {
  default = true
}

data "aws_subnets" "default" {
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default.id]
  }
}

resource "aws_security_group" "snip" {
  name        = "snip-demo"
  description = "SSH only, from operator IP"
  vpc_id      = data.aws_vpc.default.id

  ingress {
    description = "SSH"
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = [var.ssh_allowed_cidr]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}
```

- [ ] **Step 5: k3s bootstrap script**

`infra/user_data.sh`:
```bash
#!/bin/bash
set -euo pipefail
curl -sfL https://get.k3s.io | sh -
mkdir -p /home/ubuntu/.kube
cp /etc/rancher/k3s/k3s.yaml /home/ubuntu/.kube/config
chown -R ubuntu:ubuntu /home/ubuntu/.kube
sed -i "s/127.0.0.1/$(curl -s http://169.254.169.254/latest/meta-data/local-ipv4)/" /home/ubuntu/.kube/config
```

- [ ] **Step 6: Compute (key pair, AMI lookup, instance)**

`infra/compute.tf`:
```hcl
resource "aws_key_pair" "snip" {
  key_name   = "snip-demo"
  public_key = file(var.public_key_path)
}

data "aws_ami" "ubuntu" {
  most_recent = true
  owners      = ["099720109477"] # Canonical

  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd/ubuntu-jammy-22.04-amd64-server-*"]
  }
}

resource "aws_instance" "snip" {
  ami                         = data.aws_ami.ubuntu.id
  instance_type               = var.instance_type
  subnet_id                   = data.aws_subnets.default.ids[0]
  vpc_security_group_ids      = [aws_security_group.snip.id]
  key_name                    = aws_key_pair.snip.key_name
  associate_public_ip_address = true
  user_data                   = file("${path.module}/user_data.sh")

  tags = {
    Name = "snip-demo"
  }
}
```

- [ ] **Step 7: Outputs**

`infra/outputs.tf`:
```hcl
output "public_ip" {
  value = aws_instance.snip.public_ip
}

output "ssh_command" {
  value = "ssh ubuntu@${aws_instance.snip.public_ip}"
}
```

- [ ] **Step 8: Validate (does not apply/cost anything)**

```bash
cd infra
terraform init
terraform fmt -check
terraform validate
```
Expected: `Success! The configuration is valid.`

- [ ] **Step 9: Commit**

```bash
git add infra/
git commit -m "feat(infra): add Terraform for EC2 + k3s bootstrap"
```

(Actually running `terraform apply` happens later, once there's an image to deploy — see the README task at the end of this plan.)

---

### Task 9: Kubernetes manifests for snip

**Files:**
- Create: `k8s/snip/deployment.yaml`
- Create: `k8s/snip/service.yaml`
- Create: `k8s/snip/pvc.yaml`

**Interfaces:**
- Consumes: the image built in Task 7, pushed by Task 15's CI to `docker.io/<DOCKERHUB_USERNAME>/snip`.
- Produces: a `Service` named `snip` on port 8080, referenced by Task 11's Prometheus scrape config.

- [ ] **Step 1: PersistentVolumeClaim for the SQLite file**

`k8s/snip/pvc.yaml`:
```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: snip-data
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: 256Mi
```

- [ ] **Step 2: Deployment**

`k8s/snip/deployment.yaml`:
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: snip
  labels:
    app: snip
spec:
  replicas: 1
  selector:
    matchLabels:
      app: snip
  template:
    metadata:
      labels:
        app: snip
    spec:
      containers:
        - name: snip
          image: docker.io/REPLACE_WITH_DOCKERHUB_USERNAME/snip:latest
          ports:
            - containerPort: 8080
          env:
            - name: PORT
              value: "8080"
            - name: DB_PATH
              value: "/data/snip.db"
            - name: BASE_URL
              value: "http://localhost:8080"
          volumeMounts:
            - name: data
              mountPath: /data
          resources:
            requests:
              cpu: 25m
              memory: 32Mi
            limits:
              cpu: 200m
              memory: 96Mi
      volumes:
        - name: data
          persistentVolumeClaim:
            claimName: snip-data
```

- [ ] **Step 3: Service**

`k8s/snip/service.yaml`:
```yaml
apiVersion: v1
kind: Service
metadata:
  name: snip
  labels:
    app: snip
spec:
  selector:
    app: snip
  ports:
    - port: 8080
      targetPort: 8080
```

- [ ] **Step 4: Validate manifests are well-formed (no cluster needed)**

```bash
kubectl apply --dry-run=client -f k8s/snip/
```
Expected: three resources reported as `created (dry run)`, no errors.

- [ ] **Step 5: Commit**

```bash
git add k8s/snip/
git commit -m "feat(k8s): add snip Deployment, Service, PVC"
```

(Note: the `REPLACE_WITH_DOCKERHUB_USERNAME` placeholder is filled in once
during Task 15's setup, then never touched again — it's a one-time
real-value substitution, not a deferred implementation detail.)

---

### Task 10: node-exporter DaemonSet

**Files:**
- Create: `k8s/node-exporter/daemonset.yaml`
- Create: `k8s/node-exporter/service.yaml`

**Interfaces:**
- Produces: a `Service` named `node-exporter` on port 9100, scraped by Task 11's Prometheus.

- [ ] **Step 1: DaemonSet**

`k8s/node-exporter/daemonset.yaml`:
```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: node-exporter
  labels:
    app: node-exporter
spec:
  selector:
    matchLabels:
      app: node-exporter
  template:
    metadata:
      labels:
        app: node-exporter
    spec:
      hostNetwork: true
      hostPID: true
      containers:
        - name: node-exporter
          image: prom/node-exporter:v1.8.2
          args:
            - --path.rootfs=/host
          ports:
            - containerPort: 9100
          volumeMounts:
            - name: rootfs
              mountPath: /host
              readOnly: true
          resources:
            requests:
              cpu: 10m
              memory: 16Mi
            limits:
              cpu: 50m
              memory: 32Mi
      volumes:
        - name: rootfs
          hostPath:
            path: /
```

- [ ] **Step 2: Service**

`k8s/node-exporter/service.yaml`:
```yaml
apiVersion: v1
kind: Service
metadata:
  name: node-exporter
  labels:
    app: node-exporter
spec:
  selector:
    app: node-exporter
  ports:
    - port: 9100
      targetPort: 9100
```

- [ ] **Step 3: Validate**

```bash
kubectl apply --dry-run=client -f k8s/node-exporter/
```
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add k8s/node-exporter/
git commit -m "feat(k8s): add node-exporter DaemonSet"
```

---

### Task 11: Prometheus (scrape config, alert rules, Deployment)

**Files:**
- Create: `k8s/prometheus/configmap.yaml`
- Create: `k8s/prometheus/deployment.yaml`
- Create: `k8s/prometheus/service.yaml`

**Interfaces:**
- Consumes: `snip:8080` (Task 9) and `node-exporter:9100` (Task 10) as static scrape targets (single-node cluster, so no service-discovery/RBAC needed).
- Produces: a `Service` named `prometheus` on port 9090, used as Grafana's datasource (Task 12) and queried by Alertmanager's rules.

- [ ] **Step 1: Scrape config + alert rules**

`k8s/prometheus/configmap.yaml`:
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: prometheus-config
data:
  prometheus.yml: |
    global:
      scrape_interval: 15s
    alerting:
      alertmanagers:
        - static_configs:
            - targets: ["alertmanager:9093"]
    rule_files:
      - /etc/prometheus/rules.yml
    scrape_configs:
      - job_name: snip
        static_configs:
          - targets: ["snip:8080"]
      - job_name: node
        static_configs:
          - targets: ["node-exporter:9100"]
      - job_name: prometheus
        static_configs:
          - targets: ["localhost:9090"]
  rules.yml: |
    groups:
      - name: snip
        rules:
          - alert: SnipDown
            expr: up{job="snip"} == 0
            for: 1m
            labels:
              severity: critical
            annotations:
              summary: "snip is down"
              description: "Prometheus has not been able to scrape snip for 1 minute."
          - alert: SnipHighErrorRate
            expr: |
              sum(rate(http_requests_total{job="snip",status=~"5.."}[5m]))
              /
              sum(rate(http_requests_total{job="snip"}[5m])) > 0.05
            for: 5m
            labels:
              severity: warning
            annotations:
              summary: "snip error rate above 5%"
              description: "More than 5% of requests to snip have returned 5xx over the last 5 minutes."
          - alert: SnipHighLatency
            expr: |
              histogram_quantile(0.99,
                sum(rate(http_request_duration_seconds_bucket{job="snip"}[5m])) by (le)
              ) > 1
            for: 5m
            labels:
              severity: warning
            annotations:
              summary: "snip p99 latency above 1s"
              description: "99th percentile request latency has exceeded 1 second for 5 minutes."
      - name: node
        rules:
          - alert: NodeMemoryPressure
            expr: node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes < 0.1
            for: 5m
            labels:
              severity: warning
            annotations:
              summary: "Node available memory below 10%"
              description: "The node has less than 10% memory available."
          - alert: NodeDiskPressure
            expr: node_filesystem_avail_bytes{mountpoint="/"} / node_filesystem_size_bytes{mountpoint="/"} < 0.1
            for: 5m
            labels:
              severity: warning
            annotations:
              summary: "Node root disk below 10% free"
              description: "The node's root filesystem has less than 10% free space."
```

- [ ] **Step 2: Deployment**

`k8s/prometheus/deployment.yaml`:
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: prometheus
  labels:
    app: prometheus
spec:
  replicas: 1
  selector:
    matchLabels:
      app: prometheus
  template:
    metadata:
      labels:
        app: prometheus
    spec:
      containers:
        - name: prometheus
          image: prom/prometheus:v2.54.1
          args:
            - --config.file=/etc/prometheus/prometheus.yml
            - --storage.tsdb.retention.time=6h
          ports:
            - containerPort: 9090
          volumeMounts:
            - name: config
              mountPath: /etc/prometheus
          resources:
            requests:
              cpu: 50m
              memory: 128Mi
            limits:
              cpu: 250m
              memory: 256Mi
      volumes:
        - name: config
          configMap:
            name: prometheus-config
```

`--storage.tsdb.retention.time=6h` is a deliberate choice: this instance is
only ever up for a demo session, so there's no need for long retention, and
it keeps disk/memory pressure down.

- [ ] **Step 3: Service**

`k8s/prometheus/service.yaml`:
```yaml
apiVersion: v1
kind: Service
metadata:
  name: prometheus
  labels:
    app: prometheus
spec:
  selector:
    app: prometheus
  ports:
    - port: 9090
      targetPort: 9090
```

- [ ] **Step 4: Validate**

```bash
kubectl apply --dry-run=client -f k8s/prometheus/
```
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add k8s/prometheus/
git commit -m "feat(k8s): add Prometheus with snip/node scrape configs and alert rules"
```

---

### Task 12: Grafana (datasource, dashboard, Deployment)

**Files:**
- Create: `k8s/grafana/configmap-datasource.yaml`
- Create: `k8s/grafana/configmap-dashboard.yaml`
- Create: `k8s/grafana/deployment.yaml`
- Create: `k8s/grafana/service.yaml`

**Interfaces:**
- Consumes: `prometheus:9090` (Task 11) as the datasource.
- Produces: a `Service` named `grafana` on port 3000, reached via port-forward for demos.

- [ ] **Step 1: Provisioned datasource**

`k8s/grafana/configmap-datasource.yaml`:
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: grafana-datasource
data:
  datasource.yml: |
    apiVersion: 1
    datasources:
      - name: Prometheus
        type: prometheus
        access: proxy
        url: http://prometheus:9090
        isDefault: true
        uid: prometheus
```

- [ ] **Step 2: Provisioned dashboard**

`k8s/grafana/configmap-dashboard.yaml`:
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: grafana-dashboard-provider
data:
  provider.yml: |
    apiVersion: 1
    providers:
      - name: snip
        folder: ""
        type: file
        options:
          path: /var/lib/grafana/dashboards
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: grafana-dashboard-snip
data:
  snip.json: |
    {
      "title": "snip",
      "uid": "snip-overview",
      "timezone": "browser",
      "schemaVersion": 39,
      "panels": [
        {
          "id": 1,
          "title": "Request rate",
          "type": "timeseries",
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "gridPos": { "h": 8, "w": 12, "x": 0, "y": 0 },
          "targets": [
            { "expr": "sum(rate(http_requests_total{job=\"snip\"}[1m])) by (route)", "legendFormat": "{{route}}" }
          ]
        },
        {
          "id": 2,
          "title": "Error rate (5xx)",
          "type": "timeseries",
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "gridPos": { "h": 8, "w": 12, "x": 12, "y": 0 },
          "targets": [
            { "expr": "sum(rate(http_requests_total{job=\"snip\",status=~\"5..\"}[5m]))", "legendFormat": "5xx/s" }
          ]
        },
        {
          "id": 3,
          "title": "p99 latency",
          "type": "timeseries",
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "gridPos": { "h": 8, "w": 12, "x": 0, "y": 8 },
          "targets": [
            { "expr": "histogram_quantile(0.99, sum(rate(http_request_duration_seconds_bucket{job=\"snip\"}[5m])) by (le))", "legendFormat": "p99" }
          ]
        },
        {
          "id": 4,
          "title": "Links created / redirects",
          "type": "timeseries",
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "gridPos": { "h": 8, "w": 12, "x": 12, "y": 8 },
          "targets": [
            { "expr": "links_created_total", "legendFormat": "links created" },
            { "expr": "redirects_total", "legendFormat": "redirects" }
          ]
        }
      ]
    }
```

- [ ] **Step 3: Deployment**

`k8s/grafana/deployment.yaml`:
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: grafana
  labels:
    app: grafana
spec:
  replicas: 1
  selector:
    matchLabels:
      app: grafana
  template:
    metadata:
      labels:
        app: grafana
    spec:
      containers:
        - name: grafana
          image: grafana/grafana:11.2.0
          ports:
            - containerPort: 3000
          env:
            - name: GF_AUTH_ANONYMOUS_ENABLED
              value: "true"
            - name: GF_AUTH_ANONYMOUS_ORG_ROLE
              value: "Viewer"
          volumeMounts:
            - name: datasource
              mountPath: /etc/grafana/provisioning/datasources
            - name: dashboard-provider
              mountPath: /etc/grafana/provisioning/dashboards
            - name: dashboard-snip
              mountPath: /var/lib/grafana/dashboards
          resources:
            requests:
              cpu: 25m
              memory: 64Mi
            limits:
              cpu: 150m
              memory: 128Mi
      volumes:
        - name: datasource
          configMap:
            name: grafana-datasource
        - name: dashboard-provider
          configMap:
            name: grafana-dashboard-provider
            items:
              - key: provider.yml
                path: provider.yml
        - name: dashboard-snip
          configMap:
            name: grafana-dashboard-snip
```

`GF_AUTH_ANONYMOUS_ENABLED` is set because there's no public ingress — the
only way in is a port-forward over an already-authenticated SSH session, so
Grafana's own login is redundant friction for a solo demo.

- [ ] **Step 4: Service**

`k8s/grafana/service.yaml`:
```yaml
apiVersion: v1
kind: Service
metadata:
  name: grafana
  labels:
    app: grafana
spec:
  selector:
    app: grafana
  ports:
    - port: 3000
      targetPort: 3000
```

- [ ] **Step 5: Validate**

```bash
kubectl apply --dry-run=client -f k8s/grafana/
```
Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add k8s/grafana/
git commit -m "feat(k8s): add Grafana with provisioned Prometheus datasource and dashboard"
```

---

### Task 13: Alertmanager (Discord routing)

**Files:**
- Create: `k8s/alertmanager/secret.yaml.example`
- Create: `k8s/alertmanager/deployment.yaml`
- Create: `k8s/alertmanager/service.yaml`
- Modify: repo root `.gitignore` (ignore the real secret file)

**Interfaces:**
- Consumes: alerts fired by Task 11's Prometheus rules, sent to `alertmanager:9093`.
- Produces: a `Service` named `alertmanager` on port 9093 (referenced by Task 11's `prometheus.yml`, already wired).

- [ ] **Step 1: Real webhook stays out of git**

Repo root `.gitignore` (create if it doesn't exist):
```
k8s/alertmanager/secret.yaml
```

- [ ] **Step 2: Example secret (checked in; real one is not)**

`k8s/alertmanager/secret.yaml.example`:
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: alertmanager-config
stringData:
  alertmanager.yml: |
    route:
      receiver: discord
      group_wait: 10s
      group_interval: 5m
      repeat_interval: 3h
    receivers:
      - name: discord
        slack_configs:
          - api_url: "https://discord.com/api/webhooks/REPLACE_ME/slack"
            channel: "#alerts"
            send_resolved: true
            title: '{{ .CommonAnnotations.summary }}'
            text: '{{ .CommonAnnotations.description }}'
```

Copy this to `k8s/alertmanager/secret.yaml` with the real Discord webhook
URL (append `/slack` to the webhook Discord gives you — that's what makes
it speak Alertmanager's Slack-compatible format) before applying.

- [ ] **Step 3: Deployment**

`k8s/alertmanager/deployment.yaml`:
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: alertmanager
  labels:
    app: alertmanager
spec:
  replicas: 1
  selector:
    matchLabels:
      app: alertmanager
  template:
    metadata:
      labels:
        app: alertmanager
    spec:
      containers:
        - name: alertmanager
          image: prom/alertmanager:v0.27.0
          args:
            - --config.file=/etc/alertmanager/alertmanager.yml
          ports:
            - containerPort: 9093
          volumeMounts:
            - name: config
              mountPath: /etc/alertmanager
          resources:
            requests:
              cpu: 10m
              memory: 32Mi
            limits:
              cpu: 100m
              memory: 64Mi
      volumes:
        - name: config
          secret:
            secretName: alertmanager-config
```

- [ ] **Step 4: Service**

`k8s/alertmanager/service.yaml`:
```yaml
apiVersion: v1
kind: Service
metadata:
  name: alertmanager
  labels:
    app: alertmanager
spec:
  selector:
    app: alertmanager
  ports:
    - port: 9093
      targetPort: 9093
```

- [ ] **Step 5: Validate the example (not the real secret, which doesn't exist yet)**

```bash
kubectl apply --dry-run=client -f k8s/alertmanager/deployment.yaml -f k8s/alertmanager/service.yaml
```
Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add k8s/alertmanager/secret.yaml.example k8s/alertmanager/deployment.yaml k8s/alertmanager/service.yaml .gitignore
git commit -m "feat(k8s): add Alertmanager with Discord routing (secret kept out of git)"
```

---

### Task 14: Load generator CronJob

**Files:**
- Create: `k8s/load-generator/cronjob.yaml`

**Interfaces:**
- Consumes: `snip:8080` (Task 9).
- Produces: nothing consumed by later tasks — purely for keeping dashboards/alerts populated with data during a demo.

- [ ] **Step 1: CronJob**

A plain shell loop in a `curlimages/curl` container — no need for another
Go binary just to generate load.

`k8s/load-generator/cronjob.yaml`:
```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: load-generator
spec:
  schedule: "* * * * *"
  concurrencyPolicy: Forbid
  jobTemplate:
    spec:
      template:
        spec:
          restartPolicy: Never
          containers:
            - name: load-generator
              image: curlimages/curl:8.10.1
              command:
                - /bin/sh
                - -c
                - |
                  for i in $(seq 1 20); do
                    CODE=$(curl -s -X POST http://snip:8080/api/shorten \
                      -d "{\"url\":\"https://example.com/$RANDOM\"}" | \
                      sed -n 's/.*"code":"\([^"]*\)".*/\1/p')
                    if [ -n "$CODE" ]; then
                      curl -s -o /dev/null http://snip:8080/$CODE
                    fi
                    sleep 2
                  done
              resources:
                requests:
                  cpu: 5m
                  memory: 8Mi
                limits:
                  cpu: 50m
                  memory: 16Mi
```

- [ ] **Step 2: Validate**

```bash
kubectl apply --dry-run=client -f k8s/load-generator/
```
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add k8s/load-generator/
git commit -m "feat(k8s): add CronJob to generate demo traffic against snip"
```

---

### Task 15: CI — test, build, push, deploy

**Files:**
- Create: `.github/workflows/ci-build.yml`

**Interfaces:**
- Consumes: `apps/snip` (Tasks 1-7), the running k3s box (Task 8), the `snip` Deployment (Task 9).
- Requires these GitHub Actions secrets (set up once in the repo settings, not in code):
  - `DOCKERHUB_USERNAME`, `DOCKERHUB_TOKEN`
  - `SNIP_HOST` (the EC2 box's current public IP — updated by hand after every `terraform apply`, per the Global Constraints)
  - `SNIP_SSH_KEY` (private key matching `infra/variables.tf`'s `public_key_path`)

- [ ] **Step 1: Workflow**

`.github/workflows/ci-build.yml`:
```yaml
name: CI Build & Deploy

on:
  push:
    branches: [main]
    paths:
      - 'apps/snip/**'
      - '.github/workflows/ci-build.yml'

jobs:
  test:
    runs-on: ubuntu-latest
    defaults:
      run:
        working-directory: apps/snip
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      - run: go test ./...

  smoke:
    needs: test
    runs-on: ubuntu-latest
    defaults:
      run:
        working-directory: apps/snip
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      - run: go build -o snip .
      - name: Start server in background
        run: ./snip &
        env:
          PORT: "8080"
          DB_PATH: ":memory:"
          BASE_URL: "http://localhost:8080"
      - run: sleep 1
      - name: Create a link and follow the redirect
        run: |
          CODE=$(curl -s -X POST localhost:8080/api/shorten -d '{"url":"https://example.com"}' | jq -r .code)
          test -n "$CODE"
          STATUS=$(curl -s -o /dev/null -w '%{http_code}' -L --max-redirs 0 localhost:8080/$CODE)
          test "$STATUS" = "302"

  build-and-deploy:
    needs: smoke
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: docker/setup-buildx-action@v3
      - uses: docker/login-action@v3
        with:
          username: ${{ secrets.DOCKERHUB_USERNAME }}
          password: ${{ secrets.DOCKERHUB_TOKEN }}
      - uses: docker/build-push-action@v6
        with:
          context: apps/snip
          push: true
          tags: |
            ${{ secrets.DOCKERHUB_USERNAME }}/snip:${{ github.sha }}
            ${{ secrets.DOCKERHUB_USERNAME }}/snip:latest
      - name: Deploy via SSH
        uses: appleboy/ssh-action@v1
        with:
          host: ${{ secrets.SNIP_HOST }}
          username: ubuntu
          key: ${{ secrets.SNIP_SSH_KEY }}
          script: |
            kubectl set image deployment/snip snip=${{ secrets.DOCKERHUB_USERNAME }}/snip:${{ github.sha }}
            kubectl rollout status deployment/snip --timeout=60s
```

- [ ] **Step 2: One-time setup (manual, not a repo file)**

In the GitHub repo's Settings → Secrets and variables → Actions, add
`DOCKERHUB_USERNAME`, `DOCKERHUB_TOKEN` (a Docker Hub access token, not your
password), `SNIP_SSH_KEY` (the private half of the key pair referenced in
`infra/variables.tf`). `SNIP_HOST` gets added/updated after the first
`terraform apply` (see the README task).

Also replace `REPLACE_WITH_DOCKERHUB_USERNAME` in
`k8s/snip/deployment.yaml` (Task 9) with the real Docker Hub username now
that it's known.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci-build.yml k8s/snip/deployment.yaml
git commit -m "ci: add build/test/push/deploy workflow for snip"
```

---

### Task 16: CI — Terraform format/validate gate

**Files:**
- Create: `.github/workflows/terraform-check.yml`

**Interfaces:**
- Consumes: `infra/` (Task 8). Read-only — never runs `apply`/`destroy` (see Global Constraints and the spec's corrected CI/CD section).

- [ ] **Step 1: Workflow**

`.github/workflows/terraform-check.yml`:
```yaml
name: Terraform Check

on:
  pull_request:
    paths:
      - 'infra/**'

jobs:
  check:
    runs-on: ubuntu-latest
    defaults:
      run:
        working-directory: infra
    steps:
      - uses: actions/checkout@v4
      - uses: hashicorp/setup-terraform@v3
      - run: terraform init -backend=false
      - run: terraform fmt -check
      - run: terraform validate
```

- [ ] **Step 2: Commit**

```bash
git add .github/workflows/terraform-check.yml
git commit -m "ci: add terraform fmt/validate gate on PRs touching infra/"
```

---

### Task 17: README — demo instructions and architecture summary

**Files:**
- Create: `README.md`

**Interfaces:**
- Consumes: every prior task — this is the human-facing entry point tying it all together.

- [ ] **Step 1: Write the README**

Cover, concretely (not generically):

1. One-paragraph description of `snip` and why this project exists
   (resume/portfolio piece demonstrating Terraform + k3s + CI/CD +
   hand-rolled observability).
2. Architecture summary (can reuse/condense the spec's architecture
   section) with the constraint list from Global Constraints above
   (no budget, 1GB RAM, no public ingress) stated plainly — these
   constraints are part of the story, not something to hide.
3. **Exact spin-up sequence:**
   ```bash
   cd infra
   terraform apply -var="ssh_allowed_cidr=<your IP>/32"
   terraform output ssh_command
   # update the SNIP_HOST GitHub Actions secret with the new public_ip output
   ssh ubuntu@<ip>
   # on the box:
   git clone <this repo>
   kubectl apply -f k8s/snip/ -f k8s/node-exporter/ -f k8s/prometheus/ -f k8s/grafana/
   kubectl apply -f k8s/alertmanager/secret.yaml -f k8s/alertmanager/deployment.yaml -f k8s/alertmanager/service.yaml
   kubectl apply -f k8s/load-generator/
   ```
4. **Viewing dashboards during a demo** (from your own machine, one SSH
   tunnel per service):
   ```bash
   ssh -L 3000:localhost:3000 ubuntu@<ip> -- kubectl port-forward svc/grafana 3000:3000 &
   ssh -L 9090:localhost:9090 ubuntu@<ip> -- kubectl port-forward svc/prometheus 9090:9090 &
   ssh -L 8080:localhost:8080 ubuntu@<ip> -- kubectl port-forward svc/snip 8080:8080 &
   ```
   Then open `localhost:3000` (Grafana), `localhost:8080` (snip).
5. **Tearing down:** `cd infra && terraform destroy`.
6. What's deliberately deferred and why (copy from the spec's "Open
   decisions deferred" section: no Loki/log aggregation, no ArgoCD/GitOps,
   no remote Terraform state).
7. Placeholder section for screenshots/recording (to be filled in once the
   stack has actually been run once, per the spec's Presentation Model).

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: add README with architecture summary and demo instructions"
```

---

## Plan Self-Review Notes

- **Spec coverage:** product (Tasks 1-7), infra (Task 8), all four
  observability components (Tasks 9-13: snip svc doubles as the metrics
  target, node-exporter, Prometheus+rules, Grafana, Alertmanager+Discord),
  load generator (Task 14), both CI workflows per the corrected spec (Tasks
  15-16), presentation/demo docs (Task 17). No spec section is uncovered.
- **Terraform/CI inconsistency** caught while writing this plan and fixed
  in the spec itself (commit `018aa8b`) before being reflected here: no
  `workflow_dispatch` Terraform job exists in this plan; `apply`/`destroy`
  are manual, documented in Task 17's README.
- **Type/name consistency checked:** `Store`, `Link`, `ErrNotFound`, `API`,
  `Metrics` and their methods are defined once (Tasks 3-5) and referenced
  with identical names/signatures in every later task that uses them
  (Tasks 4, 6). Metric names (`http_requests_total`,
  `http_request_duration_seconds`, `links_created_total`,
  `redirects_total`) are defined in Task 5 and referenced identically in
  Task 11's alert rules and Task 12's dashboard queries.
