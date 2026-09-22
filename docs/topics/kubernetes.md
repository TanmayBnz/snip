# Kubernetes (k3s)

**Status: in progress** — this note covers Task 9 (`snip`'s own Deployment/Service/PVC)
and will be extended as Tasks 10-13 add node-exporter, Prometheus, Grafana, and
Alertmanager manifests, all part of the same Kubernetes topic rather than split into
five separate notes.

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
