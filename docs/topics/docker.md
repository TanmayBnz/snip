# Docker

## What it is

Docker packages an application together with everything it needs to run (the binary,
libraries, filesystem layout) into an **image** — a static, portable snapshot — and
runs it as a **container**, an isolated process with its own filesystem view, sharing
the host's kernel (unlike a VM, which virtualizes hardware and runs a whole separate
kernel). A **multi-stage build** uses more than one `FROM` in a single `Dockerfile`:
an early stage does the compiling, using a full toolchain image, and the final stage
copies only the compiled output into a much smaller runtime base — the build tools
never end up in the image that actually gets deployed.

## How it is used here

`apps/snip/Dockerfile`, two stages:

```dockerfile
FROM golang:1.25 AS build
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

`COPY go.mod go.sum ./` then `RUN go mod download` happens *before* `COPY . .` (the
full source) on purpose: Docker caches each layer, invalidating it and everything
after only when its inputs change. Dependencies change far less often than source
code, so this ordering means editing a `.go` file doesn't force a full
`go mod download` re-run on the next build — only editing `go.mod`/`go.sum` does.

`CGO_ENABLED=0` is what makes the second stage possible at all: it's the same
constraint discussed in [sqlite.md](sqlite.md)'s driver choice (`modernc.org/sqlite`,
pure Go, no C) — without it, the binary would dynamically link against libc, and
`distroless/static` has no libc, no shell, no package manager, nothing but the Go
runtime's own minimal requirements. `distroless/static-debian12` specifically (not
`distroless/base`, which
does include libc) was chosen because there's nothing in this binary that needs it.

Final measured image size: **39.8MB** (`docker images snip:local --format
'{{.Size}}'`). Verified end-to-end, not just read: `docker build -t snip:local .`
succeeded, then `docker run -d --rm -p 18082:8080 -e DB_PATH=/tmp/snip.db
snip:local` followed by `curl -X POST localhost:18082/api/shorten -d
'{"url":"https://example.com"}'` returned a correct `{"code":"1","short_url":"..."}`
with the actual containerized binary, confirming env-var config (`DB_PATH`) and the
embedded `templates/` directory both work inside the container, not just locally.

The plan's Dockerfile specified `FROM golang:1.22` for the build stage; that was
bumped to `golang:1.25` here because `go.mod`'s `go` directive was already `1.25.0` —
auto-bumped by `go get modernc.org/sqlite` back in Task 3, since that driver's own
`go.mod` requires a newer language version than 1.22. Building with `golang:1.22`
against a `go.mod` declaring `go 1.25.0` would fail outright (the toolchain refuses to
build a module declaring a newer minimum version than itself) — caught by actually
trying the build, which is the whole reason the plan's Step 3 smoke-test exists rather
than assuming a written Dockerfile is correct.

`apps/snip/.dockerignore`:
```
*.db
snip
```
Keeps the build's `COPY . .` from dragging a locally-built binary or a stray `.db`
file (leftover from local `go run`/testing) into the build context, which would both
bloat the context sent to the Docker daemon and risk stale binaries confusing what
actually got compiled inside the container.

## What someone would ask

**"Why not just run `go run .` in a plain image?"** That would need the full
`golang:1.25` image (\~800MB+) present at runtime, plus source code shipped into
production, plus a JIT-recompile-on-start style startup cost. The multi-stage build
means the ~800MB build toolchain image never leaves the CI runner / local build
machine — only the 39.8MB output does.

**"Why distroless instead of alpine, which is also small?"** Alpine still has a shell,
a package manager, and other Linux userland tools — useful for `docker exec`-ing in to
debug, but each of those is also attack surface if the container is ever compromised.
Distroless intentionally has none of that: no shell means no interactive
debugging inside a running container, which is a real cost during development,
traded for a smaller and harder-to-pivot-from image at runtime. The trade-off flips
if this needed frequent in-container debugging — Alpine (or even a debug-variant
distroless image) would be the better choice then.

**"How would you debug a crash inside this container, given there's no shell?"**
Honestly: `docker logs` for stdout/stderr (which is all `main.go`'s `log.Printf`/
`log.Fatal` write to), or attaching a debugger isn't practical against this image at
all as built. The realistic path is reproducing the failure against a locally-run
`go run .` binary instead, using the container only to confirm the packaged build
behaves the same. Not solved here — see below.

## What is not done

- **No image scanning for vulnerabilities** (e.g. `docker scout`, `trivy`) — the base
  image and Go module dependencies are trusted without automated CVE checking. For a
  resume/demo project with no real user data, an acceptable gap; would not be for
  anything handling real traffic or credentials.
- **No multi-arch build** (`--platform linux/amd64,linux/arm64`) — built for whatever
  architecture the build machine is, which happens to match the target EC2 instance
  type decided in the design spec, so this hasn't caused a problem, but it's not
  explicit or guaranteed to keep matching if the instance type ever changes.
- **No layer-cache-friendly separation of `go.sum` verification from `go mod
  download`'s actual network fetch** — not a real problem for a project with ~20
  dependencies and a fast build, but at a larger dependency tree this ordering
  matters more.
- **No non-root user configured.** `distroless/static-debian12`'s default image runs
  as root inside the container (the `:nonroot` tag variant exists specifically to fix
  this and wasn't used here). Low real risk given no exposed ingress and the
  container never mounts host paths, but worth naming as a shortcut rather than
  implying it was considered and dismissed for a good reason — it wasn't considered
  at build time, full stop.
- **No `HEALTHCHECK` directive** — Kubernetes readiness/liveness probing (Task 9) is
  what will actually gate traffic, so a Docker-level healthcheck would be redundant
  for this deployment target, but it does mean `docker run` alone gives no signal
  about whether the process inside is actually healthy versus just running.
