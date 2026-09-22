# snip: a URL shortener with a full CI/CD + observability stack

Date: 2026-09-22
Status: approved, pending implementation plan

## Purpose

A portfolio/resume project that demonstrates real CI/CD and infrastructure
skills (Terraform, Kubernetes, GitHub Actions, Prometheus/Grafana/Alertmanager)
around an actual working product — a URL shortener called `snip` — rather than
a throwaway demo app. It needs to:

- Be presentable live in an interview or demo (not just described).
- Cost effectively nothing to run (no ongoing AWS bill).
- Be defensible in detail: every component should be something the author
  can explain, not a black-box Helm chart.

## Non-goals

- High availability / multi-node clustering.
- User accounts, auth, or multi-tenant link ownership.
- Public, always-on hosting (see "Presentation model" below).
- Log aggregation (Loki) — deferred, metrics-only observability for now.
- Rate limiting / abuse protection beyond what the alerting rules surface.

## Product: `snip`

A single Go binary providing:

- `GET /` — minimal server-rendered HTML page: paste a long URL, get a short
  link back. No separate frontend build step.
- `POST /api/shorten` — JSON API (`{"url": "..."}` → `{"code": "...",
  "short_url": "..."}`) for programmatic use.
- `GET /:code` — 302 redirect to the original URL; increments a click counter
  for that code.
- Storage: embedded SQLite on a small PersistentVolume. No separate database
  pod — keeps the whole stack inside the 1GB RAM budget of a free-tier
  instance and removes one more component to run and monitor.
- Prometheus metrics exposed at `/metrics`: request count and latency
  histogram by route/status code, links-created counter, redirects-served
  counter, per-link click counts.

A CronJob-based load generator continuously creates random short links and
hits redirects, so dashboards always have live data and alert rules have
something to fire on, even when nobody is actively demoing the system.

## Infrastructure

- **Compute**: a single EC2 instance (t2.micro or t3.micro, whichever is
  free-tier eligible in the target region). k3s (lightweight Kubernetes,
  single node) is installed via `user_data` on first boot.
- **Provisioning**: Terraform manages the VPC/security group (SSH only,
  restricted to the operator's IP), the EC2 instance, the key pair, and the
  `user_data` k3s bootstrap script. State is a local `terraform.tfstate` file
  — this is a solo project with a single operator, so remote state/locking
  is unnecessary overhead.
- **Lifecycle**: the instance is not left running. `terraform apply` before a
  demo or work session, `terraform destroy` after. This keeps AWS cost at
  effectively zero regardless of free-tier status. Because the public IP
  changes on every apply, the README documents the manual step of updating
  the GitHub Actions deploy secret with the new IP after each apply.
- **No public ingress**: Grafana, Prometheus, and `snip` itself are reached
  via `kubectl port-forward` tunneled over SSH. The only port exposed to the
  internet is SSH (22). This is a deliberate cost/attack-surface tradeoff,
  not an oversight — documented as such in the README.

## Observability stack

Prometheus, Grafana, Alertmanager, and node-exporter are deployed as
hand-rolled Kubernetes manifests (Deployments/DaemonSets + ConfigMaps), not
via the `kube-prometheus-stack` Helm chart. Rationale:

- The full chart's footprint (kube-state-metrics, CRDs, multiple extra
  components) is heavier than needed on a 1GB-RAM box that's already running
  k3s, `snip`, and its own SQLite volume.
- Hand-writing scrape configs, alert rules, and dashboard JSON means every
  piece of the stack is something the author configured and can explain in
  detail, rather than "I installed a chart."

Components:

- **node-exporter** (DaemonSet): host-level CPU/memory/disk metrics.
- **Prometheus**: scrapes `snip`, node-exporter, and itself. Alert rules
  defined as code (see below).
- **Grafana**: a single pre-provisioned dashboard (as JSON, checked into the
  repo) showing request rate, latency, error rate, and links/redirects over
  time.
- **Alertmanager**: routes firing alerts to a Discord channel via Discord's
  Slack-compatible webhook endpoint (append `/slack` to a Discord webhook
  URL and use Alertmanager's native `slack_configs` receiver).

Alert rules (initial set):

- `snip` target down.
- Elevated 5xx rate over a short window.
- High p99 request latency.
- Node disk or memory pressure.

## CI/CD

Two separate GitHub Actions workflows:

1. **`ci-build.yml`** (on push to main, path-filtered to `apps/snip/**`):
   run Go unit tests (short code generation, redirect logic, click
   counting), run a curl-based smoke test against a locally-run container,
   then build and push the image to Docker Hub (public repo) tagged with
   the git SHA. On success, SSH into the EC2 box (host IP and SSH key as
   GitHub secrets) and run `kubectl set image deployment/snip
   snip=<user>/snip:<sha>` to roll out the new version.
2. **`terraform-check.yml`** (on PR touching `/infra`): runs `terraform fmt
   -check` and `terraform validate` only — no `apply`/`destroy` in CI at
   all. This is a lightweight IaC gate that costs nothing and catches
   mistakes early.

Terraform's `apply`/`destroy` lifecycle is run manually, from the operator's
own machine, via `terraform apply` / `terraform destroy` in `/infra`. This
is a deliberate consequence of the "local state file" decision above: state
that lives only on the operator's laptop cannot be applied or destroyed
from a GitHub Actions runner, which starts with an empty filesystem on every
run — a `destroy` from CI would have no record of what `apply` created. A
CI-driven Terraform lifecycle would require a remote state backend (S3 +
DynamoDB), which was deliberately rejected above to keep this a zero-cost,
zero-setup solo project. The README documents the manual apply/destroy
commands as part of the demo workflow.

## Presentation model

Because the instance isn't left running, `snip` is not a permanently live
resume link. Instead:

- README documents a "how to spin this up for a demo" sequence: `terraform
  apply` → wait for k3s/manifests to be ready → port-forward → demo (create
  a link, watch Grafana move, optionally trigger an alert by generating
  load) → `terraform destroy`.
- Screenshots/short recording of the working dashboards and the shortener
  UI are kept in the repo for anyone reviewing it without a live session.

## Testing

- Go unit tests for `snip`'s core logic (code generation collisions,
  redirect correctness, click-count accuracy).
- A curl-based smoke test in CI (`POST /api/shorten` then `GET /:code`
  redirects correctly) before an image is considered good to deploy.
- `terraform validate` (and `fmt -check`) on every PR touching `/infra`.

## Repo layout

```
/apps/snip              — Go service (handlers, SQLite storage, metrics, HTML template)
/k8s/snip                — Deployment, Service, PVC for snip
/k8s/prometheus          — Deployment, ConfigMap (scrape config + alert rules), Service
/k8s/grafana             — Deployment, ConfigMap (dashboard JSON, datasource), Service
/k8s/alertmanager        — Deployment, ConfigMap (Discord route), Service
/k8s/node-exporter       — DaemonSet
/infra                   — Terraform: VPC/SG, EC2, key pair, user_data (k3s install)
/.github/workflows       — ci-build.yml, terraform.yml
README.md                — demo instructions, architecture summary, screenshots
```

## Open decisions deferred, not blocking

- Log aggregation (Loki) — explicitly out of scope for v1, notable as a
  documented "next step" rather than a gap to hide.
- Whether to eventually add ArgoCD/GitOps — rejected for v1 due to RAM cost
  on a 1GB box; SSH + `kubectl set image` is the chosen mechanism.
