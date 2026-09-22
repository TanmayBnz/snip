# Grafana

## What it is

Grafana is a dashboard/visualization layer that sits on top of a data source (here,
Prometheus — [prometheus.md](prometheus.md)) and renders time-series query results as
panels: line charts, single-stat numbers, tables. It doesn't store metrics itself; it
queries whatever backend you configure (PromQL, in this case) and renders the result.
**Provisioning** is Grafana's term for configuring it entirely through files it reads
on startup — datasources, dashboards, alert rules — as an alternative to manually
clicking through its web UI to set the same things up. Provisioning is what makes a
Grafana setup reproducible and version-controllable instead of living only in one
person's browser session.

## How it is used here

`k8s/grafana/` — two ConfigMaps for provisioning (datasource + dashboard), a
Deployment running `grafana/grafana:11.2.0`, a Service on port 3000.

**Datasource provisioning**, `configmap-datasource.yaml`:
```yaml
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
Mounted into `/etc/grafana/provisioning/datasources/` — a directory Grafana scans on
startup specifically for datasource definitions like this one. `url:
http://prometheus:9090` is the same Kubernetes Service DNS pattern used throughout
this project ([kubernetes.md](kubernetes.md)) — Grafana, running as its own pod,
reaches Prometheus's pod(s) through the `prometheus` Service, never a pod IP
directly. `access: proxy` means Grafana's own backend makes the query request to
Prometheus (server-side), not the viewer's browser directly — the only mode that
makes sense here, since Prometheus has no public ingress at all; a browser trying to
reach it directly would have nothing to connect to.

**Dashboard provisioning is two separate ConfigMaps working together**,
`configmap-dashboard.yaml`:
```yaml
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
# second ConfigMap, grafana-dashboard-snip, holding the actual dashboard JSON
```
The **provider** config (mounted at `/etc/grafana/provisioning/dashboards/`) doesn't
contain a dashboard itself — it tells Grafana "watch this directory
(`/var/lib/grafana/dashboards`) on disk and load any dashboard JSON files found
there." The **dashboard JSON** (mounted at that watched path, from a second, separate
ConfigMap) is the actual panel definitions. Two ConfigMaps, two different mount
paths in `deployment.yaml`, working together as "here's where to look" +
"here's what's there."

**One dashboard, four panels, each a PromQL query wrapped in Grafana's panel JSON
schema** — request rate by route, 5xx error rate, p99 latency, and links
created/redirects. Every `expr` here is identical PromQL to what's already covered in
[prometheus.md](prometheus.md) (the same `rate`/`histogram_quantile` patterns as the
alert rules) — Grafana panels and Prometheus alert rules both just wrap the same
underlying query language for different purposes (visualize vs. threshold-and-page).

**`GF_AUTH_ANONYMOUS_ENABLED=true`** — Grafana normally requires login. This project
disables that requirement entirely, and it's a stated trade-off, not an oversight:
the *only* way to reach Grafana at all is `kubectl port-forward` tunneled over an
already-authenticated SSH session (the design spec's no-public-ingress model). A
second login screen behind an already-authenticated tunnel is pure friction with no
real security benefit here — the SSH key *is* the access control.

**Validated three ways**, since three different things can each independently be
wrong: `kubectl apply --dry-run=client -f k8s/grafana/` confirmed k8s schema
correctness across all 5 resources (including the two-ConfigMap-documents-in-one-file
`---` split). Separately, since `kubectl` never looks inside a ConfigMap's string
values, a direct `yaml.safe_load`/`json.loads` pass confirmed `datasource.yml`,
`provider.yml`, and `snip.json` are each genuinely well-formed — catching a class of
mistake (an unescaped quote in the embedded JSON, say) that would have shipped
silently past `kubectl` alone.

## What someone would ask

**"Why provisioning instead of just configuring it through the UI once?"** A manual
UI setup lives only in that one running Grafana instance's own database — lost the
moment the pod (and its ephemeral filesystem/emptyDir-backed state) is recreated,
which happens on every `terraform destroy` + `apply` cycle in this project's
demo-session model. Provisioning-as-code means the dashboard and datasource are
recreated automatically and identically every time the stack comes up, checked into
git like everything else here — genuinely necessary given how often this specific
deployment gets torn down and rebuilt, not just a nice-to-have.

**"Isn't disabling auth a security problem?"** Only if the thing gating access to
Grafana in the first place were weaker than the SSH access it currently rides on. The
actual access control here is "do you have the SSH private key to this EC2 instance
and is your IP in the security group's allowlist" — both already required before
`kubectl port-forward` to Grafana is even reachable. Anonymous-viewer mode inside
that tunnel doesn't remove a layer of real security, it removes a redundant one. The
trade-off flips immediately if this were ever exposed via public ingress instead —
then Grafana's own auth becomes the only thing standing between the internet and the
dashboard.

**"What would you have to change to add a second dashboard?"** Add a third ConfigMap
(another `<name>.json`) and mount it into the same
`/var/lib/grafana/dashboards` directory the provider already watches — the provider
config doesn't need to change at all, since it's already watching the whole
directory, not a specific filename.

## What is not done

- **Never actually run.** Like the rest of the observability stack, this has only
  been schema/content-validated locally — never deployed to a real cluster, so
  whether the dashboard actually renders correctly against live Prometheus data
  (correct panel layout, PromQL that returns what's expected once real traffic
  exists) is unverified until the README/demo task.
- **No alerting configured in Grafana itself** — all alerting lives in Prometheus's
  own rule evaluation + Alertmanager (Tasks 11/13), not Grafana's separate unified
  alerting feature. A deliberate choice to keep alerting logic in one place rather
  than split across two systems, though not explicitly stated as a decision anywhere
  in the plan/spec — inferred from what was actually built, not a documented
  rationale.
- **Dashboard JSON was hand-written to match the plan exactly**, not exported from a
  real running Grafana instance after visually tuning panel layout/thresholds/colors.
  `gridPos` values are plausible-looking but unverified against how the dashboard
  actually looks rendered.
- **No dashboard version/schemaVersion migration path considered** — `schemaVersion:
  39` is pinned to what Grafana 11.2.0 expects; upgrading the Grafana image version
  later could require a schema migration this project has no process for.
