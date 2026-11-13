---
page_title: "livellm_alert_settings Resource - livellm"
description: |-
  Which alerts one resource gets (Monitoring) and at what level.
---

# livellm_alert_settings (Resource)

Sets which alerts one resource of the workspace gets on the Monitoring page,
and at what level: a down alert, CPU and memory alerts (open after 5 minutes
at or over the level), a disk alert (opens at once) and, for a resource that
uses a GPU, a GPU alert. Each kind is the platform's usual, off, or (CPU,
memory and disk) the resource's own level, a whole percent from 50 to 99.

Only the kinds written here are managed: a kind left out keeps whatever it
has, the console's choice included, and never shows as a change.

## Example Usage

```terraform
resource "livellm_vm" "build" {
  name      = "build-box"
  cpus      = 4
  memory_gi = 8
  disk_gi   = 80

  username            = "dev"
  password_wo         = var.vm_password
  password_wo_version = 1
}

# A build machine runs its CPU hot on purpose: alert only at 95%, and no disk
# alerts for its scratch disk.
resource "livellm_alert_settings" "build" {
  resource = livellm_vm.build.name
  cpu      = "95"
  disk     = "off"
}
```

The settings belong to the resource on the platform. When the resource is
made again (a replacement, `terraform apply -replace`), the platform starts it
with the usual alerts: the next plan shows the change, and the apply after it
puts these settings back.

Only the workspace's owners, or a workspace key, can change alert settings: a
member's key and an agent sign-in only read them, and an apply through one is
refused in the platform's words. A kind that doesn't apply to the resource
(`disk` on an app without volumes, `gpu` on a resource without a GPU) is
refused at apply, naming it.

Needs LiveLLM API 0.101.5 or newer; an older server stops the apply with
"Alert settings aren't available on this server".

## Schema

### Required

- `resource` (String) The resource's name (its workload id), e.g. `livellm_vm.build.name`. Changing it replaces this.

### Optional

- `down` (String) An alert when the resource goes down: `on` (the platform's usual) or `off`.
- `cpu` (String) An alert when CPU use stays at or over the level for 5 minutes: `usual` (the platform's level), `off`, or a whole percent from 50 to 99 written as a string (`"95"`).
- `memory` (String) An alert when memory use stays at or over the level for 5 minutes: `usual`, `off` or a level, as `cpu`.
- `disk` (String) An alert when a disk is at or over the level: `usual`, `off` or a level, as `cpu`. Only for a resource that keeps disks: a machine, a database or object storage, a browser, an app with volumes, a Desktop App that keeps its files.
- `gpu` (String) An alert when a GPU the resource uses stops working: `on` or `off`. Only for a resource that uses a GPU.

Each is checked at plan time. Left out, Terraform leaves that kind as it is;
removing one from the configuration later puts it back to the usual. Deleting
this resource puts every kind of the resource back to the usual.

## Import

```shell
terraform import livellm_alert_settings.build build-box
```

An import takes every kind that isn't the usual.
