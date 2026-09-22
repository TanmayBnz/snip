# snip

A URL shortener written in Go, with Terraform-provisioned Kubernetes (k3s)
infrastructure, a hand-rolled Prometheus/Grafana/Alertmanager observability
stack, and a GitHub Actions CI/CD pipeline that runs real integration tests
against the built binary before every deploy.

Built from zero prior Go experience, as a learning project and a portfolio
piece — every piece is something I can explain, not an installed Helm chart
or a scaffold I don't understand. See [docs/topics/](docs/topics/) for
detailed, defensible notes on each technology used, including the mistakes
found along the way.

## What it does

Paste a URL in, get a short link back. The short link redirects to the
original and counts clicks.

```
POST /api/shorten   {"url": "https://example.com/very/long/path"}
                  →  {"code": "1", "short_url": "http://localhost:8080/1"}

GET  /1              → 302 redirect to https://example.com/very/long/path
GET  /metrics        → Prometheus exposition format
GET  /               → the web UI
```

## Stack

- **App**: Go, stdlib `net/http` (no framework), `modernc.org/sqlite` (pure
  Go, no CGO), `prometheus/client_golang`.
- **Container**: multi-stage Docker build — `golang:1.25` to compile
  (`CGO_ENABLED=0`), `gcr.io/distroless/static-debian12` to run. No shell, no
  package manager, no libc in the final image. Measured 39.8 MB.
- **Infra**: Terraform (AWS provider) provisions a single EC2 instance and
  bootstraps k3s via `user_data`.
- **Orchestration**: Kubernetes manifests, hand-written, no Helm — a
  Deployment + Service + PVC for the app, a DaemonSet for node-exporter, and
  raw manifests for Prometheus, Grafana, and Alertmanager.
- **CI/CD**: GitHub Actions — test, then a smoke test against the actual
  built binary, then build/push/deploy, gated in sequence.

## Repo layout

```
apps/snip/       Go source, tests, Dockerfile, HTML template
infra/           Terraform: VPC/security group, EC2, k3s bootstrap
k8s/             Kubernetes manifests, one directory per component
  snip/            the app itself: Deployment, Service, PVC
  node-exporter/   host metrics, DaemonSet (hostNetwork/hostPID)
  prometheus/      scrape config + 5 alert rules, as a ConfigMap
  grafana/         datasource + dashboard provisioned as ConfigMaps
  alertmanager/    routing config; secret.yaml is gitignored, see
                   secret.yaml.example
  load-generator/  CronJob that curls the app to produce demo traffic
.github/workflows/ CI pipeline (test → smoke → build/push/deploy)
docs/topics/      One study note per technology, written incrementally
```

## Running it locally

```sh
cd apps/snip
go test ./...                          # 14 test functions, ~0.02s
DB_PATH=:memory: PORT=8080 go run .    # or: go build && ./snip
curl -X POST localhost:8080/api/shorten -d '{"url":"https://example.com"}'
```

Config is three environment variables, all optional:

| Var | Default | Purpose |
|---|---|---|
| `PORT` | `8080` | listen port |
| `DB_PATH` | `snip.db` | SQLite file path (`:memory:` for scratch runs) |
| `BASE_URL` | `http://localhost:$PORT` | prefix used when building `short_url` |

Or via Docker:

```sh
docker build -t snip apps/snip
docker run -p 8080:8080 -e DB_PATH=:memory: snip
```

## CI/CD

Three jobs, each gated on the previous one passing:

1. **test** — `go test ./...`.
2. **smoke** — builds the binary, starts it, actually creates a link over
   HTTP and follows the redirect, asserting a real 302. This caught a real
   bug during development: `curl -L --max-redirs 0` in the redirect check is
   a self-contradictory flag combination that curl treats as an immediate
   "too many redirects" error, which silently killed the script under
   bash's default `-e` before the real assertion ever ran — every run would
   have failed, and the YAML alone gave no hint of it. Found by running the
   workflow locally with [`act`](https://github.com/nektos/act), fixed by
   dropping `-L`, verified with three clean reruns.
3. **build-and-deploy** — builds and pushes the Docker image, then deploys
   over SSH.

## What's real and what isn't (yet)

- Verified for real: the Go test suite, a real `docker build` + `docker run`
  hitting the container over HTTP, `terraform init/fmt/validate` against the
  actual `infra/` config, a full deploy to a local `k3d` cluster with the
  load generator running against it (`links_created_total ==
  redirects_total == 37` after a real run), and three clean local CI runs
  via `act`.
- **Not yet done**: `terraform apply` against real AWS. There is no live
  URL, no deployed instance, no real Grafana screenshot from a production
  target. Prometheus, Grafana, and Alertmanager have been config-validated
  (`promtool check config`, `amtool check-config`) and proven against the
  local k3d cluster, but never run against the real target box.
- **Deliberate non-goals**: no HTTPS/TLS, no auth, no rate limiting, no
  public ingress — this is a single-operator demo, reached via `kubectl
  port-forward` over SSH, not a service open to the internet.

## Docs

- [docs/topics/](docs/topics/) — one note per technology (Go, SQLite,
  Docker, Terraform, Kubernetes, Prometheus, Grafana, Alertmanager, GitHub
  Actions), each covering what it is, how it's actually used here, and what
  corners were cut.
- [docs/superpowers/specs/2026-09-22-snip-observability-stack-design.md](docs/superpowers/specs/2026-09-22-snip-observability-stack-design.md)
  — the original design spec.
- [docs/superpowers/plans/2026-09-22-snip-observability-stack.md](docs/superpowers/plans/2026-09-22-snip-observability-stack.md)
  — the task-by-task implementation plan this was built against.
