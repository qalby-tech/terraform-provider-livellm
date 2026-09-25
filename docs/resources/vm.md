---
page_title: "livellm_vm Resource - livellm"
description: |-
  A machine — an Ubuntu terminal or desktop, a Debian or Fedora server, or Windows 11 or Windows Server — with ports, a stop time and placement.
---

# livellm_vm (Resource)

Creates a machine: Ubuntu by default, Debian or Fedora with `os`, or Windows
with `os = "windows"`. Terraform waits until the VM is up, then reports its
SSH address and the public URL of every exposed port. The SSH password is a
write-only argument; give the machine an SSH key of its own with `ssh_keys`,
and a stop time with `stop_after` when it is made for one job.

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
  ssh_keys            = [file("~/.ssh/id_ed25519.pub")]

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

A Debian server (or `"fedora"`). Debian and Fedora come as servers only:

```terraform
resource "livellm_vm" "worker" {
  name      = "worker"
  os        = "debian"
  cpus      = 2
  memory_gi = 4

  username            = "dev"
  password_wo         = var.vm_password
  password_wo_version = 1
}
```

Windows 11 (`windows_edition = "server"` for Windows Server 2025, Server Core:
a command line and no desktop). Windows installs itself on first start, which
takes 15 to 35 minutes; open it from the console's screen or with its Remote
Desktop file. It needs a disk of at least 64 GiB, gets 64 when `disk_gi` is
left out, and takes no `ssh_keys`:

```terraform
resource "livellm_vm" "win" {
  name      = "win-lab"
  os        = "windows"
  cpus      = 4
  memory_gi = 8
  disk_gi   = 64

  username            = "admin" # not "Administrator": that name is Windows' own
  password_wo         = var.win_password
  password_wo_version = 1

  timeouts {
    create = "45m" # the default for Windows
  }
}
```

A machine for one job, stopped by the platform four hours after Terraform
creates it. Stopping keeps the disk; nothing is deleted:

```terraform
resource "livellm_vm" "ci" {
  name      = "ci-box"
  cpus      = 4
  memory_gi = 8
  disk_gi   = 40

  username            = "agent"
  password_wo         = var.vm_password
  password_wo_version = 1
  ssh_keys            = [var.ci_public_key]

  stop_after = "4h"
}

output "ci_stops_at" { value = livellm_vm.ci.expires_at }
```

The clock starts when Terraform creates the machine, starts it again, or when
`stop_after` changes — not on every apply, so editing a port doesn't push the
stop time back. Once the platform has stopped the machine, Terraform sees
`stopped` drift to `true`: the next apply starts it for another `stop_after`.
Set `stopped = true` to leave it off.

Placement is optional — by default LiveLLM picks the host:

```terraform
resource "livellm_vm" "eu" {
  name = "eu-box"
  # …credentials…

  placement_strategy = "region" # or "host" with placement_host
  placement_region   = "eu-1"
}
```

With a backup each night, the last seven kept:

```terraform
resource "livellm_vm" "build" {
  name = "build-box"
  # …credentials…

  backup {
    schedule = "@daily"
    keep     = 7
  }
}
```

## Schema

### Required

- `name` (String) Workload id. Changing it replaces the VM.
- `username` (String) The login's username: SSH on Linux, the administrator on Windows (not `Administrator`, which is Windows' own). Changing it replaces the VM.
- `password_wo` (String, Sensitive, Write-only) The login's password, at least 8 characters — not stored in state.
- `password_wo_version` (Number) Rotation trigger for `password_wo`.

### Optional

- `os` (String) The system: `ubuntu` (24.04), `debian` (13), `fedora` (44) or `windows`. Debian and Fedora are servers only. Replaces the VM when changed. Defaults to `ubuntu`.
- `windows_edition` (String) With `os = "windows"`: `desktop` (Windows 11 Pro, the default) or `server` (Windows Server 2025, Server Core). Replaces the VM when changed.
- `desktop` (Boolean) An Ubuntu desktop instead of a terminal VM (not with `os = "windows"`). Replaces the VM when changed. Defaults to `false`.
- `cpus` (Number) vCPU count.
- `memory_gi` (Number) Memory in GiB.
- `disk_gi` (Number) Root disk in GiB. Windows needs at least 64.
- `ssh_keys` (List of String) Linux only. SSH public keys for this machine alone, one `.pub` line each. They are installed for `username` next to the workspace's own keys (set on the console's Keys page), and a change reaches a running machine within a minute or two. Leave it out to keep whatever the machine has. The platform keeps a machine's last keys, so replace a key rather than emptying the list — an empty list is refused at plan time.
- `stopped` (Boolean) Halt the VM without destroying it — the disk is kept and billing drops to disk-only.
- `stop_after` (String) Have the platform stop this machine after a while: a length of time such as `4h`, `90m` or `2h30m` (a minute to 30 days). See the example above for when the clock starts. Remove it to clear the stop time.
- `allow_cidrs` (List of String) Source CIDRs allowed to reach SSH and raw ports. Omit for workspace-internal only; `0.0.0.0/0` for public.
- `placement_strategy` (String) `region` or `host`. Omit for automatic placement (the default).
- `placement_region` (String) Region to schedule into.
- `placement_host` (String) Host id to pin to.
- `timeouts` (Block) `create` (default 15m, 45m for Windows) / `delete` (default 10m).
- `backup` (Block) Scheduled backups of the disk. Without the block none are scheduled; backups taken by hand are kept either way. A backup restores in place, with the machine stopped.
  - `schedule` (String, Required in the block) `@hourly`, `@daily`, `@weekly`, `@monthly` or a 5-field cron expression (UTC).
  - `keep` (Number, Required in the block) How many scheduled backups are kept, `1`..`100` — a count, unlike a database's `keep_days`.
- `port` (Block List) Exposed ports:
  - `name` (String, Required) Port name — part of the hostname for HTTP ports.
  - `port` (Number, Required) Listener port inside the VM.
  - `tcp` (Boolean) Expose as a raw TCP address instead of HTTPS.
  - `udp` (Boolean) Expose as a raw UDP address.
  - `internal` (Boolean) No public address and no node port: reachable from inside the workspace only, at `<workspace>-<name>-internal:<port>`.

### Read-Only

- `ready` (Boolean) Whether the VM is up.
- `expires_at` (String) When the platform will stop the machine (RFC 3339). Empty when it has no stop time.
- `ssh` (String) `host:port` to SSH into the VM.
- `url` (String) The first exposed HTTP port's public HTTPS URL.
- `endpoints` (List of Object) Every exposed port (`name`, `url`, `addr`, `tcp`, `udp`).

## Import

```shell
terraform import livellm_vm.dev dev-box
```
