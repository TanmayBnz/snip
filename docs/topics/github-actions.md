# GitHub Actions CI/CD

**Status: in progress** — covers Task 15 (`ci-build.yml`: test, smoke, build, push,
deploy). Will extend once Task 16 adds the Terraform format/validate gate.

## What it is

GitHub Actions runs workflows (YAML files in `.github/workflows/`) in response to
repo events — a push, a PR, a schedule. A workflow is one or more **jobs**, each
running on a fresh VM (`runs-on: ubuntu-latest`), each job a sequence of **steps**
(either a shell command via `run:`, or a reusable packaged action via `uses:`).
`needs: <job>` makes one job wait for another to succeed first — the mechanism this
project uses to gate deployment behind tests actually passing.

## How it is used here

`.github/workflows/ci-build.yml` — three jobs, each depending on the last:
`test` → `smoke` → `build-and-deploy`. Triggered only on `push` to `main`, and only
when `apps/snip/**` or the workflow file itself changed (`paths:` filter) — editing
Terraform or Kubernetes manifests doesn't trigger a Go rebuild.

**`test`** just runs `go test ./...` against the real 19-test suite from
[go.md](go.md). **`smoke`** builds the actual binary and runs it against real HTTP
requests — not a mock, the literal binary that would get deployed:
```yaml
- run: go build -o snip .
- name: Start server in background
  run: ./snip &
  env:
    PORT: "8080"
    DB_PATH: ":memory:"
    BASE_URL: "http://localhost:8080"
```
`DB_PATH: ":memory:"` reuses SQLite's in-memory mode ([sqlite.md](sqlite.md)) so the
smoke test needs no persistent volume or cleanup — a fresh, empty database every CI
run. `./snip &` backgrounds the server so the workflow can move to the next step
while it keeps running; a background process started in one `run:` step genuinely
does survive into later steps in real GitHub Actions (confirmed this holds under a
local Docker-based runner too — see below), since all steps in a job execute
sequentially against the same underlying VM/container.

**A readiness-polling loop, not a fixed sleep**, waits for the server before testing
against it:
```yaml
- name: Wait for server to be ready
  run: |
    for i in $(seq 1 20); do
      curl -s -o /dev/null localhost:8080/metrics && exit 0
      sleep 0.5
    done
    echo "server did not become ready in time" >&2
    exit 1
```
The plan's original script used a flat `sleep 1` instead. Replaced this while
investigating a real failure (see below) — polling with a timeout is strictly more
reliable than guessing a fixed delay is always enough, though it turned out not to
be the actual cause of that failure.

**The real bug, found by actually running this workflow, not just reading it:**
```yaml
- name: Create a link and follow the redirect
  run: |
    CODE=$(curl -s -X POST localhost:8080/api/shorten -d '{"url":"https://example.com"}' | jq -r .code)
    test -n "$CODE"
    STATUS=$(curl -s -o /dev/null -w '%{http_code}' --max-redirs 0 localhost:8080/$CODE)
    test "$STATUS" = "302"
```
The plan's original last line was `curl -s -o /dev/null -w '%{http_code}' -L
--max-redirs 0 localhost:8080/$CODE`. `-L` means "follow redirects"; `--max-redirs 0`
means "allow following at most zero redirects." Combined, curl treats the very first
redirect it encounters as `CURLE_TOO_MANY_REDIRECTS` — **exit code 47** — even though
it still correctly writes `302` to stdout via `-w`. Confirmed directly:
```
$ curl -s -o /dev/null -w '%{http_code}\n' -L --max-redirs 0 localhost:18095/2
302
curl exit: 47
```
Both happen — the right value gets printed, and the process still exits non-zero.
GitHub Actions' (and `act`'s) default shell for multi-line `run:` blocks is
effectively `bash -e`, so `STATUS=$(curl ...)` failing with exit 47 aborts the script
**immediately**, before `test "$STATUS" = "302"` is ever reached — with zero output,
since nothing in the original script explicitly echoed anything. This is why the
failure was silent and looked mysterious at first. Fix: drop `-L` — without it, curl
never attempts to follow the redirect at all, so it just reports `302` cleanly with
exit `0`. This would have failed on **every single real CI run** using the plan's
script exactly as written — not a rare edge case, a deterministic bug baked into the
provided smoke test.

**How this was actually found and verified**, not just asserted: installed `act`
(a tool that runs GitHub Actions workflows locally against Docker containers,
simulating the real runner environment) and `actionlint` (static analysis for
workflow YAML — job dependency checks, expression syntax, action version validity)
into `~/.local/bin`, no `sudo` needed. `actionlint` found zero issues (the YAML
structure itself was always fine — this was a shell/curl logic bug, not a YAML
mistake). Reproduced the failure twice with the original script via `act`, added
explicit `$?` tracing between each line to pinpoint exactly which command died,
confirmed the curl exit-code-47 behavior directly against a real running `snip`
instance outside of CI entirely, applied the fix, then ran the corrected workflow
**three times in a row** to confirm it wasn't a coincidence — all three passed.

**Two other fixes, smaller but still worth noting:**
- `go-version: '1.22'` (the plan's value) → `'1.25'`, matching `go.mod`'s directive
  (same root cause as [docker.md](docker.md)'s `golang:1.22`→`1.25` bump: `go get
  modernc.org/sqlite` auto-bumped the language version requirement back in Task 3).
- The repo's branch was `master` (created before the GitHub remote existed), but
  this workflow only triggers `on: push: branches: [main]`. Without catching this,
  CI would **never fire automatically on push** — renamed the branch to `main`
  (locally and on GitHub, including setting it as the new default and deleting the
  old `master`), matching what the workflow assumes.

**`build-and-deploy` remains genuinely untested** — it needs `DOCKERHUB_USERNAME`/
`DOCKERHUB_TOKEN`/`SNIP_SSH_KEY`/`SNIP_HOST` as real GitHub Actions secrets, and
`SNIP_HOST` specifically doesn't exist until a real `terraform apply` produces an
EC2 IP. First real end-to-end run happens at the README/demo task.

## What someone would ask

**"How did you find that curl bug — did you just know it?"** No — ran the actual
workflow locally with `act` rather than trusting that YAML which "looks right" is
right. The failure was completely silent at first (exit 47, zero output, ~80ms), so
the process was genuinely investigative: confirmed the server was reachable (it was —
a separate curl to `/metrics` succeeded instantly), confirmed the POST itself worked
(it did, valid JSON came back), then added `$?` tracing to find the exact line, then
tested that exact curl invocation in isolation to see both its stdout and exit code
at once. That last step is what actually revealed the contradiction.

**"Why does `act` matter here if you could just push and watch real GitHub Actions
fail?"** Cost of iteration — pushing, waiting for a real runner to spin up, reading
logs, fixing, pushing again is minutes per attempt; `act` against local Docker is
seconds, and never touches the real repo's Actions history with failed runs. It's
not a perfect substitute (see below), but for exactly this kind of "why did this
shell script exit early" debugging, it's the right tool.

**"What's the actual security model for `build-and-deploy`'s secrets?"** All four
(`DOCKERHUB_USERNAME`, `DOCKERHUB_TOKEN`, `SNIP_SSH_KEY`, `SNIP_HOST`) are GitHub
Actions repository secrets — encrypted at rest by GitHub, injected as environment
values only during a workflow run, never visible in logs (GitHub automatically
redacts known secret values if they appear in output) and never committed to the
repo. `SNIP_SSH_KEY` is the private half of the key pair Terraform's `aws_key_pair`
resource ([terraform.md](terraform.md)) uploaded the public half of — this is the
same secret-stays-local pattern as Alertmanager's Discord webhook
([alertmanager.md](alertmanager.md)), just using GitHub's secret store instead of
`.gitignore` since this value needs to be usable *by* CI, not just kept out of it.

## What is not done

- **`act` is not a perfect emulation of real GitHub Actions.** It runs against
  Docker images meant to approximate GitHub's hosted runners
  (`catthehacker/ubuntu:act-latest`), not the actual runner images — subtle
  environment differences are possible. The `test`/`smoke` jobs passing reliably
  under `act` is strong evidence, not absolute proof, that they'll behave identically
  on real GitHub Actions infrastructure.
- **`build-and-deploy` has never run at all**, locally or on GitHub — it needs real
  secrets and a real SSH target. Everything about it (the Docker Hub push, the SSH
  connection, the `kubectl set image` rollout) is unverified until the demo task.
- **No caching configured for Go module downloads** (`actions/setup-go@v5` supports
  automatic caching via `cache: true`, not enabled here) — every `test` and `smoke`
  job run re-downloads the full dependency tree from scratch (visible in the logs:
  15+ `go: downloading ...` lines every run). Fine at this project's scale and CI
  frequency; would be worth adding if build times or GitHub Actions minutes usage
  ever became a concern.
- **No workflow-level timeout set** — a hung step (e.g., `./snip &` somehow never
  becoming ready) would run until GitHub's own default job timeout (6 hours) rather
  than failing fast. The readiness-polling loop's own 10-second bound (20 × 0.5s)
  mitigates the specific case it guards, but nothing bounds the workflow as a whole.
