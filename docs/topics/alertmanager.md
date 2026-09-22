# Alertmanager

## What it is

Alertmanager is a separate component from Prometheus that receives alerts (Prometheus
decides *when* to fire one, based on rule evaluation — [prometheus.md](prometheus.md)
— and pushes it to Alertmanager) and handles what happens *after* that: deduplicating
near-identical alerts, grouping related ones into a single notification, waiting a
bit before sending (in case the underlying condition resolves on its own), and
routing to an actual notification channel. Splitting this from Prometheus itself
means the "when does something count as a problem" logic (PromQL rules) and the
"how do humans get told about it" logic (routing, grouping, notification channels)
are independently configurable.

## How it is used here

`k8s/alertmanager/` — a Secret holding Alertmanager's config (containing a real
Discord webhook URL, kept out of git), a Deployment running
`prom/alertmanager:v0.27.0`, a Service on port 9093 — the same address Task 11's
Prometheus config already points at (`alertmanagers: - static_configs: - targets:
["alertmanager:9093"]`), wired before Alertmanager itself existed.

**Routing and timing**, from `secret.yaml.example`'s `alertmanager.yml`:
```yaml
route:
  receiver: discord
  group_wait: 10s
  group_interval: 5m
  repeat_interval: 3h
```
`group_wait: 10s` — when the first alert in a new group fires, wait 10 seconds
before sending, in case related alerts arrive in that window and can be batched into
one notification instead of several. `group_interval: 5m` — once a group's already
been notified, wait at least 5 minutes before sending an update about *additional*
alerts joining that same group. `repeat_interval: 3h` — if an alert is still firing
with nothing new to report, don't re-notify more often than every 3 hours (prevents
an ongoing, already-acknowledged problem from spamming the channel indefinitely).

**Discord via Alertmanager's Slack receiver — a genuine, not-a-hack integration
technique**, not a full-blown custom Discord integration:
```yaml
receivers:
  - name: discord
    slack_configs:
      - api_url: "https://discord.com/api/webhooks/REPLACE_ME/slack"
        channel: "#alerts"
        send_resolved: true
        title: '{{ .CommonAnnotations.summary }}'
        text: '{{ .CommonAnnotations.description }}'
```
Alertmanager has no native `discord_configs` receiver type. Discord's own webhook
endpoint happens to accept the same JSON payload shape Slack's incoming webhooks use,
*if* you append `/slack` to the webhook URL Discord gives you — Discord built this
compatibility layer deliberately, precisely so tools that only know how to talk to
Slack (like Alertmanager) can be pointed at Discord instead. `send_resolved: true`
means a second notification fires when the alert condition clears, not just when it
starts — without this, Alertmanager only ever tells you something broke, never that
it's fixed. `title`/`text` use Go template syntax (`{{ .CommonAnnotations.summary
}}`) pulling directly from the `summary`/`description` annotations already defined on
each alert rule in [prometheus.md](prometheus.md)'s `rules.yml` — the alert message
content is authored once, in the Prometheus rule, and Alertmanager's config just
decides how to format/deliver it.

**Kept out of git properly, verified structurally**: the real `secret.yaml` (with an
actual webhook URL) is listed in the newly-created root `.gitignore`
(`k8s/alertmanager/secret.yaml`) and doesn't exist yet — only `secret.yaml.example`
(with a `REPLACE_ME` placeholder) is committed. `git status` after staging confirmed
only the `.example` file, `deployment.yaml`, `service.yaml`, and `.gitignore` itself
were staged — no real secret ever touched git.

**Validated at three levels**, same escalating pattern as the rest of the
observability stack: `kubectl apply --dry-run=client` on `deployment.yaml` +
`service.yaml` (the plan explicitly scopes this to just those two, since the real
secret doesn't exist yet) confirmed k8s schema correctness; a separate dry-run on
`secret.yaml.example` alone confirmed the Secret's own schema; a direct
`yaml.safe_load` of the embedded `alertmanager.yml` confirmed it's syntactically
valid YAML; and finally `amtool check-config` (run via `docker run --entrypoint
amtool prom/alertmanager:v0.27.0`, the exact pinned image version) confirmed it's a
*semantically* valid Alertmanager config — real route/receiver structure, not just
YAML that happens to parse:
```
Checking '/etc/alertmanager/alertmanager.yml'  SUCCESS
Found:
 - global config
 - route
 - 0 inhibit rules
 - 1 receivers
 - 0 templates
```

## What someone would ask

**"How would someone else set this up with their own Discord server?"** Copy
`secret.yaml.example` to `secret.yaml`, replace `REPLACE_ME` in the webhook URL with
their own Discord webhook's actual ID/token (Discord: channel settings → Integrations
→ Webhooks → Copy Webhook URL, then append `/slack`), then `kubectl apply -f
k8s/alertmanager/secret.yaml`. The `.example` suffix convention (rather than, say, a
`.env.example` pattern) is what makes "here's the shape, fill in your own secret"
discoverable without any separate documentation.

**"What happens if two alerts fire within the same 10-second group_wait window?"**
They get batched into a single Discord message rather than two separate ones — this
is the entire point of `group_wait` existing. Without it (`group_wait: 0s`), a node
going down and simultaneously triggering both `NodeMemoryPressure` and
`NodeDiskPressure` would page as two separate notifications instead of one
correlated one.

**"Walk me through why `send_resolved: true` matters."** Without it, Alertmanager
only notifies on the transition into a firing state — if `SnipDown` fires and then
resolves five minutes later, the Discord channel shows "snip is down" and then
silence, with no visible confirmation the problem went away. Anyone reading the
channel later has no way to tell "still down" from "was down, now fine" without
separately checking Grafana. `send_resolved: true` sends a second, clearly-marked
resolution message on that same alert.

## What is not done

- **No real webhook has ever been tested end-to-end.** Every validation above is
  structural/semantic (config is well-formed, references resolve) — nothing has
  confirmed that a real alert actually reaches a real Discord channel, since that
  requires an actual webhook URL and an actually-firing alert, neither of which
  exist yet. First real test happens during the README/demo task, deliberately
  (per the plan) by generating load high enough to trigger one of the alert rules.
- **No inhibition rules** (`amtool`'s output confirms: `0 inhibit rules`) — e.g.
  `NodeMemoryPressure` and `SnipDown` could both fire from the same underlying root
  cause (node under memory pressure kills the `snip` pod), producing two separate
  notifications for one real problem. Inhibition (suppress a symptom-level alert when
  a cause-level one is already firing) isn't configured — acceptable for the
  monitoring set's current small size, more valuable as alert count/interdependency
  grows.
- **No templates directory** (`0 templates` in the `amtool` output) — Discord
  notification formatting is entirely inline (`title`/`text` fields), not using
  Alertmanager's separate templating file support, which would matter for more
  elaborate or reused-across-receivers formatting.
- **Only one receiver, no severity-based routing.** Every alert (`critical` and
  `warning` both) goes to the same `discord` receiver with the same timing — a real
  production setup often routes `critical` differently (e.g. paging) from `warning`
  (e.g. a lower-urgency channel or longer `repeat_interval`). Not done here, since a
  single-operator demo project has no meaningful distinction between "page someone"
  and "post to a channel" — there's only ever one person to notify, via one channel.
