---
page_title: "livellm_vm Resource - livellm"
description: |-
  An Ubuntu VM — terminal or desktop — with ports and placement.
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

A GUI desktop, reachable over VNC from the console:

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
}
```

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

### Read-Only

- `ready` (Boolean) Whether the VM is up.
- `ssh` (String) `host:port` to SSH into the VM.
- `url` (String) The first exposed HTTP port's public HTTPS URL.
- `endpoints` (List of Object) Every exposed port (`name`, `url`, `addr`, `tcp`).

## Import

```shell
terraform import livellm_vm.dev dev-box
```
