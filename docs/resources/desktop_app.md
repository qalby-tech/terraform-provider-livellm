---
page_title: "livellm_desktop_app Resource - livellm"
description: |-
  A Desktop App: a Linux desktop in a container that starts in seconds.
---

# livellm_desktop_app (Resource)

Creates a Desktop App: one Linux desktop in a container that starts in
seconds. Give each agent or task a Desktop App of its own: an agent works it
through the computer tool (`connect` with `tool = "computer"`), and a person
watches or takes it over in the console.

## Example Usage

```terraform
resource "livellm_desktop_app" "desk" {
  for_each = toset(["desk-a", "desk-b", "desk-c"])
  name     = each.key
  cpu      = "2"
  memory   = "4Gi"
}

output "desks_ready" { value = { for k, d in livellm_desktop_app.desk : k => d.ready } }
```

A desktop that keeps its home folder across restarts:

```terraform
resource "livellm_desktop_app" "studio" {
  name       = "studio"
  resolution = "1920x1080"
  keep_files = true
  storage_gi = 20
}
```

## Schema

### Required

- `name` (String) Workload id. Changing it replaces the app.

### Optional

- `image` (String) A desktop image. Leave it out for the platform's (`ghcr.io/qalby-tech/livellm-desktop:xfce`); your own works if it follows that image's contract.
- `cpu` (String) CPU, e.g. `2` (the default).
- `memory` (String) Memory, e.g. `4Gi` (the default).
- `resolution` (String) Screen size, e.g. `1280x800` (the default).
- `keep_files` (Boolean) Give the desktop a home folder that survives restarts. By default it starts clean every time. Replaces the app when changed.
- `storage_gi` (Number) With `keep_files`: the home folder in GiB (10 when left out). Replaces the app when changed.
- `stopped` (Boolean) Stop the desktop without deleting the app. The home folder is kept and only it is billed.
- `timeouts` (Block) `create` (default 15m) / `delete` (default 10m).

### Read-Only

- `ready` (Boolean) Whether the desktop is up.

## Import

```shell
terraform import livellm_desktop_app.studio studio
```
