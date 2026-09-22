# Prometheus

## What it is

Prometheus is a metrics collection and alerting system built around one core idea:
it **pulls** metrics by periodically HTTP-scraping a known list of targets (rather
than applications pushing metrics to it), stores them as time series (a metric name
+ labels + a sequence of (timestamp, value) pairs), and lets you query that data with
**PromQL**, its own query language. A separate component, **Alertmanager**, receives
alerts Prometheus decides should fire (based on rules evaluated against that stored
data) and handles routing them to actual notification channels (Discord, in this
project — [alertmanager.md](alertmanager.md), not yet written).

## How it is used here

`k8s/prometheus/` — a ConfigMap holding the actual Prometheus config and alert rules,
a Deployment running the `prom/prometheus:v2.54.1` image against that config, and a
Service exposing it on port 9090.

**Scrape config**, from `configmap.yaml`'s `prometheus.yml`:
```yaml
global:
  scrape_interval: 15s
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
```
Every 15 seconds, Prometheus makes an HTTP GET to each target's `/metrics` endpoint
(the default path) and parses whatever it finds — for `snip`, that's the
`http_requests_total`/`http_request_duration_seconds`/`links_created_total`/
`redirects_total` metrics [metrics.go](../../apps/snip/metrics.go) registers ([go.md](go.md)
covers the actual instrumentation code). `targets` uses Kubernetes Service DNS names
(`snip`, `node-exporter`) rather than pod IPs — see [kubernetes.md](kubernetes.md) for
why that's sufficient here without any service-discovery machinery. Prometheus also
scrapes itself (`job: prometheus`, `localhost:9090`) — it exposes its own operational
metrics the same way any other target does.

**Alert rules are PromQL expressions evaluated on a schedule**, from `rules.yml`:
```yaml
- alert: SnipDown
  expr: up{job="snip"} == 0
  for: 1m
```
`up` is a metric Prometheus generates automatically for every scrape target — `1` if
the last scrape succeeded, `0` if it didn't (timeout, connection refused, non-200
response). `for: 1m` means the condition has to hold continuously for a full minute
before the alert actually fires, not on the first failed scrape — this is what turns
"one network blip" into "not an alert" and "genuinely down for a while" into "paged."

```yaml
- alert: SnipHighErrorRate
  expr: |
    sum(rate(http_requests_total{job="snip",status=~"5.."}[5m]))
    /
    sum(rate(http_requests_total{job="snip"}[5m])) > 0.05
  for: 5m
```
`http_requests_total` is a Prometheus counter — a number that only ever increases,
incremented once per request by `metrics.go`'s `Instrument` middleware. A raw counter
value is useless for alerting on directly (it just keeps growing); `rate(...[5m])`
converts it into "average per-second increase over the last 5 minutes," which is what
actually behaves like "how many requests per second." `status=~"5.."` is a **regex
label matcher** (`=~`, not `=`) — matches any `status` label value starting with `5`
(500, 502, 503, ...) via the `5..` pattern. Dividing the 5xx rate by the total rate
gives a fraction; `> 0.05` is the 5% threshold from the design spec's alert list.

```yaml
- alert: SnipHighLatency
  expr: |
    histogram_quantile(0.99,
      sum(rate(http_request_duration_seconds_bucket{job="snip"}[5m])) by (le)
    ) > 1
```
`http_request_duration_seconds` is a Histogram (declared in `metrics.go` as a
`HistogramVec`) — internally, a set of counters, one per bucket boundary (`le` label,
"less than or equal to"), each counting how many observations fell at or under that
boundary. `histogram_quantile(0.99, ...)` estimates the 99th percentile latency from
those bucket counts — not an exact value (histograms are inherently an approximation
based on bucket boundaries), but accurate enough to alert on, and far cheaper to
compute/store than keeping every individual latency value.

**6-hour retention** (`--storage.tsdb.retention.time=6h` in `deployment.yaml`) is a
deliberate choice stated directly in the plan: this instance only ever runs for the
length of a demo session, so long-term retention has no value and only costs
disk/memory — the opposite of a production Prometheus, which typically retains weeks
to months of data.

**Validated two separate ways**, because `kubectl` and Prometheus check different
things:
```
$ kubectl apply --dry-run=client -f k8s/prometheus/
configmap/prometheus-config created (dry run)
deployment.apps/prometheus created (dry run)
service/prometheus created (dry run)
```
confirms the *Kubernetes* YAML is schema-valid — but `prometheus.yml`/`rules.yml` are
just opaque string values to Kubernetes; a PromQL typo would sail through this check.
Separately, via `docker run --entrypoint promtool prom/prometheus:v2.54.1 check
config ...` (using the exact same image version the Deployment references, mounted at
`/etc/prometheus/` to match the real Deployment's `mountPath` so the config's
`rule_files: [/etc/prometheus/rules.yml]` reference resolves):
```
Checking /etc/prometheus/prometheus.yml
  SUCCESS: 1 rule files found
 SUCCESS: /etc/prometheus/prometheus.yml is valid prometheus config file syntax
Checking /etc/prometheus/rules.yml
  SUCCESS: 5 rules found
```
confirms the actual PromQL/rule syntax is valid — the check `kubectl` alone would
have missed entirely.

## What someone would ask

**"Why pull instead of push?"** Pull means Prometheus itself controls the scrape
schedule and can tell the difference between "target is down" (scrape fails) and
"target stopped reporting" (which, with push, looks identical to "target is fine and
just has nothing new to say"). The trade-off: pull needs Prometheus to know the
target list in advance (a real constraint that's exactly why this project's
`static_configs` approach doesn't scale past a known, fixed set of services — see
[kubernetes.md](kubernetes.md)'s RBAC/service-discovery discussion) — a system with
many short-lived, unpredictable targets (serverless functions, batch jobs) often
favors push instead, via something like a Pushgateway.

**"Walk me through what happens on a real 5xx spike."** `snip`'s `metrics.go`
`Instrument` middleware increments `http_requests_total{status="500", ...}` on every
matching request. Every 15s, Prometheus scrapes and records the current counter
value. `rate(...[5m])` computes the per-second increase over a rolling 5-minute
window from those recorded points. If that rate, as a fraction of total request rate,
exceeds 5% continuously for 5 minutes (`for: 5m`), `SnipHighErrorRate` transitions
from pending to firing and gets sent to Alertmanager's configured receiver
([alertmanager.md](alertmanager.md)).

**"Why 6h retention specifically, and what would break at 6 hours and 1 minute?"**
Nothing breaks — old data just gets deleted once the TSDB (time-series database,
Prometheus's on-disk storage format) exceeds that window, freeing the disk space.
There's no cliff or failure mode; it's purely a disk/memory budget choice for an
instance that never runs longer than a demo session anyway. The trade-off flips for
any deployment meant to answer "what did latency look like last month" — this
config genuinely cannot answer that question after 6 hours, by design.

## What is not done

- **No recording rules** — every alert's PromQL (especially `SnipHighLatency`'s
  `histogram_quantile` over a `sum(rate(...))`) gets recomputed from raw scrape data
  on every rule evaluation, rather than precomputing a cheaper intermediate metric.
  Irrelevant at this scrape volume/cardinality; would matter at a much larger metric
  count where rule evaluation cost itself becomes a bottleneck.
- **No remote write / long-term storage backend** — data genuinely only exists for 6
  hours and only on this one Prometheus instance's local disk. If the pod restarts,
  all history is gone (TSDB rebuilds from nothing). Consistent with the "this is a
  demo instance, not a production monitoring system" framing throughout, but a real
  limitation if anyone ever wanted historical data from a past demo session.
- **No authentication on Prometheus's own `/`-served UI or API** — anyone who can
  reach port 9090 (via `kubectl port-forward`, per the design spec's no-public-ingress
  model) has full read access to all metrics and can run arbitrary PromQL. Acceptable
  because the only way to reach it at all is already gated behind SSH access to the
  EC2 instance; would not be acceptable if this were ever exposed more broadly.
- **Alert thresholds (5% error rate, 1s p99 latency, 10% free memory/disk) are the
  plan's initial choices, not tuned against any observed real traffic pattern.** No
  data exists yet (Task 14's load generator hasn't run) to know whether these are
  sensible for `snip`'s actual behavior under the demo's synthetic load, versus just
  reasonable-sounding defaults.
