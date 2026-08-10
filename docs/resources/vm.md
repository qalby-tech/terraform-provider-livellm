---
page_title: "livellm_vm Resource - livellm"
description: |-
  An Ubuntu VM — terminal or desktop — with ports, placement and an optional AI agent.
---

# livellm_vm (Resource)

Creates an Ubuntu VM. Terraform waits until the VM is up, then reports its
SSH address and the public URL of every exposed port. The SSH password is a
write-only argument.

## Example Usage

```terraform
resource "livellm_vm" "dev" {
  name      = "dev-box"
  cpus      = 2
  memory_gi = 4
  disk_gi   = 40

  username            = "dev"
  password_wo         = var.vm_password
  password_wo_version = 1

  # Reachable from anywhere; omit for workspace-internal only.
  allow_cidrs = ["0.0.0.0/0"]

  port {
    name = "api"
    port = 3000
  }
}

output "ssh" { value = livellm_vm.dev.ssh }
output "api" { value = livellm_vm.dev.url }
```

A GUI desktop with an AI agent that operates it:

```terraform
resource "livellm_vm" "workstation" {
  name      = "workstation"
  desktop   = true
  cpus      = 4
  memory_gi = 8
  disk_gi   = 60

  username            = "dev"
  password_wo         = var.vm_password
  password_wo_version = 1

  ai_daemon {
    provider = "anthropic" # a provider connected on the Integrations page
    sudo     = true
  }
}
```

## Reproducible machines

A VM with an agent also gets a **machine state** repo: the packages, services
and config that make it what it is, kept as code. Terraform declares the
machine's shape; that repo holds its insides. Together they mean a machine is
reproducible rather than precious — destroy it, apply again, and the platform
replays the repo onto the new machine as it boots.

```terraform
resource "livellm_vm" "builder" {
  name      = "builder"
  cpus      = 4
  memory_gi = 8
  disk_gi   = 40

  username            = "dev"
  password_wo         = var.vm_password
  password_wo_version = 1

  ai_daemon {
    provider = "anthropic"
    sudo     = true
  }
}

# Where the machine's insides are described. Clone it, read it, review changes
# to it — a push converges the machine.
output "blueprint" { value = livellm_vm.builder.state_repo_url }
```

The repo outlives the VM on purpose: `terraform destroy` removes the machine
and keeps its blueprint, so the next `apply` brings the same machine back.
Nothing in Terraform state carries the machine's insides — the repo does.

Placement is optional — by default LiveLLM picks the host:

```terraform
resource "livellm_vm" "eu" {
  name = "eu-box"
  # …credentials…

  placement_strategy = "region" # or "host" with placement_host
  placement_region   = "eu-1"
}
```

## Schema

### Required

- `name` (String) Workload id. Changing it replaces the VM.
- `username` (String) SSH username. Changing it replaces the VM.
- `password_wo` (String, Sensitive, Write-only) SSH password — not stored in state.
- `password_wo_version` (Number) Rotation trigger for `password_wo`.

### Optional

- `desktop` (Boolean) GUI Linux Desktop instead of a terminal VM. Replaces the VM when changed. Defaults to `false`.
- `cpus` (Number) vCPU count.
- `memory_gi` (Number) Memory in GiB.
- `disk_gi` (Number) Root disk in GiB.
- `stopped` (Boolean) Halt the VM without destroying it — the disk is kept and billing drops to disk-only.
- `allow_cidrs` (List of String) Source CIDRs allowed to reach SSH and raw ports. Omit for workspace-internal only; `0.0.0.0/0` for public.
- `placement_strategy` (String) `region` or `host`. Omit for automatic placement (the default).
- `placement_region` (String) Region to schedule into.
- `placement_host` (String) Host id to pin to.
- `port` (Block List) Exposed ports:
  - `name` (String, Required) Port name — part of the hostname for HTTP ports.
  - `port` (Number, Required) Listener port inside the VM.
  - `tcp` (Boolean) Expose as a raw TCP address instead of HTTPS.
  - `udp` (Boolean) Expose as a raw UDP address.
- `ai_daemon` (Block, Single) Attach an AI agent that operates the VM over SSH:
  - `provider` (String) A connected AI provider id.
  - `model` (String) Model id; defaults to the provider's recommendation.
  - `sudo` (Boolean) Allow the agent passwordless sudo.
  - `instructions` (String) Standing guidance for the agent.

### Read-Only

- `ready` (Boolean) Whether the VM is up.
- `ssh` (String) `host:port` to SSH into the VM.
- `url` (String) The first exposed HTTP port's public HTTPS URL.
- `endpoints` (List of Object) Every exposed port (`name`, `url`, `addr`, `tcp`).
- `state_repo` (String) Repo describing what this machine is. Null unless the VM has an agent.
- `state_repo_url` (String) Browse URL for `state_repo`.

## Import

```shell
terraform import livellm_vm.dev dev-box
```
