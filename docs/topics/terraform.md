# Terraform

## What it is

Terraform is a declarative infrastructure tool: you write what you want to exist (an
EC2 instance with these properties, a security group with this rule), not the
step-by-step commands to create it, and Terraform figures out the actual API calls
needed. It tracks everything it has created in a **state file**
(`terraform.tfstate`) — a JSON snapshot of "what exists and what Terraform thinks it
looks like" — so subsequent runs only change what's actually different from the
current config, instead of recreating everything every time.

Config is written in **HCL** (HashiCorp Configuration Language): `resource`/`data`/
`variable`/`output`/`provider` blocks in `.tf` files. Every `.tf` file in a directory
is parsed together as one configuration — file boundaries here are purely
organizational, not a Terraform concept (unlike, say, Go packages).

## How it is used here

`infra/` — 7 files, split by concern rather than by any Terraform requirement:
`main.tf` (provider/version constraints), `variables.tf` (inputs), `network.tf`
(default VPC lookup + security group), `compute.tf` (key pair, AMI lookup, the actual
instance), `outputs.tf`, `user_data.sh` (not Terraform — a bash cloud-init script),
`.gitignore`.

**`resource` vs. `data` is the core distinction to internalize:** a `resource` block
is something Terraform creates and owns — it'll appear as "to be created" in a plan,
gets tracked in state, and `destroy` removes it. A `data` block is a read-only lookup
against something that already exists — `data "aws_vpc" "default" { default = true }`
finds the AWS-provided default VPC every account gets, never creates or touches it.
This project deliberately uses the default VPC/subnets instead of creating a custom
VPC (`network.tf`) — one less thing to provision and tear down for a single-instance
demo.

**The security group is the entire security model**, per the design spec's "no public
ingress" decision:
```hcl
ingress {
  description = "SSH"
  from_port   = 22
  to_port     = 22
  protocol    = "tcp"
  cidr_blocks = [var.ssh_allowed_cidr]
}
```
Port 22 only, from a required (no-default) variable — `ssh_allowed_cidr` has no
`default` in `variables.tf`, forcing every `apply` to explicitly supply the
operator's current IP rather than silently defaulting to something permissive.
Everything else — Grafana, Prometheus, `snip` itself — is reached via `kubectl
port-forward` tunneled over this one SSH connection, never a directly opened port.

**AMI lookup pins to a specific owner, not just a name pattern:**
```hcl
data "aws_ami" "ubuntu" {
  most_recent = true
  owners      = ["099720109477"] # Canonical
  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd/ubuntu-jammy-22.04-amd64-server-*"]
  }
}
```
AMI names aren't unique or access-controlled — any AWS account can publish an image
named to look official. `owners` restricted to Canonical's real account ID is what
actually guarantees the resolved AMI is genuinely Canonical's Ubuntu 22.04, not a
same-named lookalike from an untrusted publisher. `most_recent = true` means this
config never needs updating when Ubuntu ships a new 22.04 point release — the AMI ID
itself isn't hardcoded anywhere.

**Validated for real, not just written:** `terraform init` (installed Terraform
1.15.9 for this, since it wasn't present) downloaded `hashicorp/aws v5.100.0`
(satisfying the `~> 5.0` constraint) and generated `.terraform.lock.hcl`. `terraform
fmt -check` passed with no diff (already canonically formatted). `terraform validate`
returned `Success! The configuration is valid.` — confirmed structurally correct and
internally consistent (all `var.x`/`resource.y.z` references resolve, types match).

**`validate` does not talk to AWS.** No credentials needed, no check that the AMI
filter actually matches a real image, no quota or permission check. It's the same
gate Task 16's CI workflow runs on every PR touching `/infra` — cheap and safe to run
without any AWS access at all, which is exactly why a syntax/reference mistake can be
caught in CI while `apply`/`destroy` (the parts that cost money and need real
credentials) stay manual, run only from the operator's own machine, matching the
design spec's Global Constraints.

## What someone would ask

**"Why local state instead of a remote backend like S3?"** This is a documented
trade-off in the plan, not an oversight: remote state (S3 + DynamoDB for locking)
buys safety when multiple people or CI runners might `apply` concurrently — locking
prevents two applies from racing and corrupting state. For a single-operator project
where `apply`/`destroy` only ever run from one person's laptop, that protection has
no failure mode to protect against, and adds setup cost (an S3 bucket + DynamoDB
table to provision *before* you can provision anything else) for zero benefit. The
trade-off flips the moment a second person needs to run Terraform against the same
infrastructure, or CI needs to run `apply` — at that point, concurrent-apply races
become a real risk and remote state with locking becomes the right call.

**"What stops CI from running `terraform apply`?"** Nothing prevents it technically —
it's a design decision, not a technical restriction. GitHub Actions runners start
with an empty filesystem every run; with state stored only locally, a CI-triggered
`apply` would have no record of prior state, and a CI-triggered `destroy` would have
no idea what to destroy. This is *why* local state and CI-driven apply/destroy are
incompatible choices — the state decision forces the CI decision, not the other way
around.

**"You ignored `.terraform.lock.hcl` — isn't that supposed to be committed?"** Yes,
normally — Terraform's own `init` output literally says to commit it, since it pins
exact provider versions/checksums for reproducibility across machines. Following the
plan's `.gitignore` here regardless, because this project's actual constraint (single
operator, `apply` only ever runs from one machine) means there's no second machine
that needs that reproducibility guarantee. Worth being honest that this is trading
away a real Terraform best practice for simplicity in a context where the trade-off
happens not to matter — not "the lockfile doesn't matter," but "it doesn't matter
*here*."

## What is not done

- **Never actually applied.** Everything above is `init`/`fmt`/`validate` only — no
  real AWS resources have been created. `terraform plan` (which *would* show exactly
  what would be created, without creating it, and does need AWS credentials) hasn't
  been run either. The first real signal that this configuration actually produces a
  working k3s instance comes at the README/demo task at the end of the plan.
- **No cost estimation tooling** (`infracost` or similar) — relying entirely on
  `t3.micro`/free-tier eligibility being correct by inspection, not measured against
  an actual bill.
- **No state locking at all**, by design (see above) — acceptable for one operator,
  a real gap the moment that's no longer true.
- **`user_data.sh` has no retry/idempotency handling.** If the k3s install step fails
  partway (network blip during `curl | sh`), the instance boots into a broken state
  with no automatic retry — the recovery path is manually SSHing in and re-running
  the install, or `terraform destroy` + `apply` again for a clean instance. Acceptable
  for a demo project where instances are short-lived and recreated per session, not
  for anything meant to stay up.
- **No tests of any kind** for this Terraform code — `terraform validate` is a syntax
  check, not a test that the resulting instance actually works. There's no automated
  verification that k3s actually comes up healthy, that the security group actually
  blocks what it's supposed to, or that the kubeconfig rewrite in `user_data.sh`
  actually produces a working `kubectl` config — all of that is manually verified
  during the demo workflow, not automated.
