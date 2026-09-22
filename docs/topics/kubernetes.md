# Kubernetes (k3s)

**Status: in progress** — covers Tasks 9-12 (`snip`, node-exporter, Prometheus,
Grafana) so far. Will extend as Task 13 adds Alertmanager, all part of the same
Kubernetes topic rather than split into five separate notes.

## What it is

Kubernetes runs containers across a cluster of machines and continuously works to
keep reality matching a declared desired state — similar spirit to Terraform, but for
running workloads instead of cloud infrastructure, and continuously reconciling
rather than only acting when you run a command. You declare "1 replica of this
container should be running" and Kubernetes' control loop keeps checking that against
reality, restarting things that die, rescheduling things off failed nodes, etc.

**k3s** specifically is a lightweight, single-binary distribution of Kubernetes from
Rancher — the same API and concepts as full Kubernetes, but with a much smaller
footprint (drops some less-essential components, bundles others together), designed
to run on resource-constrained hardware like a single small VM. This is why it's the
choice here rather than full upstream Kubernetes: [terraform.md](terraform.md)'s EC2
instance is a `t3.micro` with roughly 1GB RAM total for *everything* — full
Kubernetes' own control plane alone typically needs more than that.

Config is YAML manifests, each declaring an `apiVersion`, `kind` (the resource type),
`metadata` (name, labels), and a type-specific `spec`.

## How it is used here

`k8s/snip/` — three manifests, one Deployment's worth of resources.

**PVC (`pvc.yaml`)** requests persistent storage:
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
A pod's own filesystem is ephemeral — deleted when the pod is, including on a routine
restart. `snip`'s SQLite file ([sqlite.md](sqlite.md)) needs to survive that, so it
lives on a PVC instead — a request for storage that Kubernetes binds to an actual
volume (on k3s's default local-path provisioner, this ends up as a directory on the
node's own disk) and keeps around independent of any particular pod's lifecycle.
`ReadWriteOnce` (one node can mount read-write at a time) is the right/only sane
choice here — SQLite itself doesn't support multiple processes writing concurrently
from different hosts, and there's only one node anyway.

**Deployment (`deployment.yaml`)** is the resource that actually keeps a pod running:
```yaml
resources:
  requests:
    cpu: 25m
    memory: 32Mi
  limits:
    cpu: 200m
    memory: 96Mi
```
This block is the actual enforcement of the design spec's "every Deployment gets
explicit, small resource requests/limits" constraint — not a comment, a real budget.
`requests` is what the scheduler reserves capacity for when deciding whether a pod
fits on the node at all; `limits` is the hard ceiling — exceed the memory limit and
the container gets OOM-killed, exceed the CPU limit and it gets throttled (slowed,
not killed). `25m` CPU means 25 millicores, i.e. 2.5% of one core — genuinely tiny,
appropriate for a Go binary handling low request volume.

`selector.matchLabels: {app: snip}` and `template.metadata.labels: {app: snip}` both
declaring the same label is not redundant boilerplate — it's how the Deployment finds
the pods it's responsible for. Get these out of sync (e.g. a typo in one) and the
Deployment either can't find its own pods or accidentally claims someone else's.

**Service (`service.yaml`)** provides a stable address:
```yaml
spec:
  selector:
    app: snip
  ports:
    - port: 8080
      targetPort: 8080
```
Pod IPs are not stable — every restart can assign a new one. A Service gives anything
else in the cluster (Task 11's Prometheus scrape config will reference `snip:8080`)
one name that always routes to whichever pod(s) currently match `selector: app:
snip`, regardless of how many times they've restarted or rescheduled.

**Validated against a real cluster, not just syntax.** `kubectl apply
--dry-run=client -f k8s/snip/` fails outright with "connection refused" if no cluster
is configured — on `kubectl` v1.37, even client-side dry-run does live API discovery
against a real server to resolve `kind`/`apiVersion` to actual schema, so "no cluster
needed" (as the plan's Step 4 describes it) turned out not to be true for this
`kubectl` version. Installed `k3d` (a tool that runs a real k3s cluster inside Docker
containers — not a mock, an actual lightweight Kubernetes cluster, just running
locally instead of on real hardware) via the official install script into
`~/.local/bin` (no `sudo` needed, checksum-verified), then `k3d cluster create
snip-validate --wait`. After that, `kubectl apply --dry-run=client -f k8s/snip/`
returned exactly what the plan expected:
```
deployment.apps/snip created (dry run)
persistentvolumeclaim/snip-data created (dry run)
service/snip created (dry run)
```
This cluster is reused for Tasks 10-13's manifests too, rather than reinstalling
tooling per task.

**DaemonSet (`k8s/node-exporter/daemonset.yaml`, Task 10)** is a different scheduling
model from Deployment: instead of "run N replicas, scheduler decides where," a
DaemonSet runs exactly one pod *per node*, automatically, with no replica count to
specify at all. On this single-node cluster it behaves identically to `replicas: 1`,
but the mechanism differs — on a multi-node cluster it would guarantee monitoring
coverage on every node with zero additional configuration.

```yaml
spec:
  hostNetwork: true
  hostPID: true
  volumes:
    - name: rootfs
      hostPath:
        path: /
```
`hostNetwork`/`hostPID` and a read-only `hostPath` mount of the host's root
filesystem are what let `node-exporter` report the *actual host's* CPU/memory/disk,
not just its own container's isolated (and largely meaningless, for this purpose)
view. `hostPath` is a real escape hatch out of container isolation — appropriate for
a monitoring agent that specifically needs host visibility, a red flag for almost
anything else.

**ConfigMap (`k8s/prometheus/configmap.yaml`, Task 11)** is how a whole config *file*
(not just a scalar value) gets into a container:
```yaml
data:
  prometheus.yml: |
    global:
      scrape_interval: 15s
    ...
```
The `|` is YAML's literal block scalar syntax — everything indented under it becomes
one multi-line string, preserving newlines exactly. Mounted into the Prometheus
Deployment via `volumes.configMap` at `/etc/prometheus/`, this ConfigMap's two keys
(`prometheus.yml`, `rules.yml`) become two real files at that path inside the
container. Contrast with `snip`'s own Deployment (Task 9), which used plain `env` var
entries — appropriate there because it's a handful of scalar values, not a structured
config file Prometheus itself needs to parse.

**Kubernetes Service DNS is how Prometheus finds its scrape targets**, without any
service-discovery machinery:
```yaml
scrape_configs:
  - job_name: snip
    static_configs:
      - targets: ["snip:8080"]
```
`"snip:8080"` resolves via the cluster's internal DNS to the `snip` Service (Task 9),
which routes to whichever pod(s) currently match its label selector — Prometheus
never needs to know a pod IP directly. This is why the plan calls this
"single-node cluster, no service-discovery/RBAC needed" — a static list of 3 known
Service names is sufficient here. A real multi-node, many-services production
cluster would instead use `kubernetes_sd_configs` (Prometheus querying the
Kubernetes API directly to discover targets dynamically as pods come and go), which
needs RBAC permissions this project deliberately doesn't grant.

**ConfigMap content is opaque to Kubernetes — a second, separate validation is
needed.** `kubectl apply --dry-run=client` confirms `configmap.yaml` is valid
Kubernetes YAML; it does **not** parse or validate `prometheus.yml`/`rules.yml`'s
*contents*, since to Kubernetes that's just a string value, not something it
understands the internal structure of. Verified separately with `promtool` (see
[prometheus.md](prometheus.md)) — a `kubectl`-only validation pass would have missed
a PromQL syntax error entirely.

## What someone would ask

**"Why k3d for local validation instead of minikube or kind?"** Not a strong
technical reason — k3d specifically runs k3s (the same distribution this project
actually deploys to EC2), so validating against it is marginally closer to the real
target than kind/minikube's full-Kubernetes-in-a-container approach. Either would
have worked for pure manifest validation; k3d's k3s-specificity is a nice-to-have,
not load-bearing.

**"Why not just always deploy to the real EC2 instance to check manifests?"** Cost and
speed — spinning up and tearing down real AWS infrastructure for every manifest
change would be slow and (per the design spec's zero-cost goal) actively worked
against. Local k3d validation catches the class of error that's purely
structural/schema-level (wrong field name, bad indentation, invalid resource type)
without ever touching AWS. It does *not* catch everything — see below.

## What is not done

- **Never actually deployed the real image.** The Deployment's image field is still
  the `REPLACE_WITH_DOCKERHUB_USERNAME` placeholder — `kubectl apply` (not
  `--dry-run`) against this manifest today would create the Deployment successfully,
  then sit in `ImagePullBackOff` forever, since that image doesn't exist. This won't
  be resolvable until Task 15 fills in the real Docker Hub username.
- **Local k3d validation doesn't catch everything a real deploy would.** It confirms
  the YAML is schema-valid and the resources *would* be created — it does not confirm
  the container actually starts successfully, that the PVC actually binds to
  available storage, that resource requests actually fit within a real (RAM-limited)
  node's capacity, or that the app inside behaves correctly once running. Those are
  only verified once this actually deploys to the real k3s instance during the
  README/demo task.
- **No liveness/readiness probes configured** on the Deployment — Kubernetes has no
  way to know if `snip`'s process is actually healthy versus just running; a hung
  (but still alive) process wouldn't get automatically restarted or removed from the
  Service's routing. A real gap, not yet decided whether it'll be added — `snip`
  doesn't currently expose a dedicated health endpoint (`GET /` would work as an
  improvised one, but isn't purpose-built for it).
- **No `PodDisruptionBudget`, no `HorizontalPodAutoscaler`** — not applicable at
  `replicas: 1` on a single-node cluster with no autoscaling infrastructure, but
  worth naming as absent rather than implying single-replica-no-autoscaling was
  itself a considered trade-off beyond "this is a demo project on one small box."
- **No RBAC configured anywhere, deliberately.** Prometheus scrapes via static,
  hardcoded Service names rather than the Kubernetes API, so it never needs a
  ServiceAccount with API read permissions. This is a real trade-off, not a gap
  glossed over: it means adding a fourth scraped service means manually editing
  `configmap.yaml` rather than it being auto-discovered — acceptable at 3 static
  targets on 1 node, would not scale past a handful of services or multiple nodes
  where pods (and their IPs) come and go on their own schedule.
- **`node-exporter`'s `hostPath`/`hostNetwork`/`hostPID` access hasn't been
  security-reviewed beyond "this is what the upstream docs say node-exporter needs."**
  These are real container-isolation escapes, appropriate for a monitoring agent but
  worth being able to name explicitly as a deliberately elevated-privilege workload
  if asked "what in this cluster has host-level access and why."
