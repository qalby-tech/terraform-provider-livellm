---
page_title: "livellm_desktop_app Resource - livellm"
description: |-
  A Desktop App: Linux desktops in containers that start in seconds.
---

# livellm_desktop_app (Resource)

Creates a Desktop App: Linux desktops in containers, numbered from 0, that
start in seconds. Give each agent or task a desktop of its own: an agent works
one through the computer tool (`connect` with `tool = "computer"` and its
desktop number), and a person watches or takes it over in the console.

## Example Usage

```terraform
resource "livellm_desktop_app" "desks" {
  name     = "desks"
  replicas = 3
  cpu      = "2"
  memory   = "4Gi"
}

output "desktops_ready" { value = livellm_desktop_app.desks.desktops_ready }
```

Desktops that keep their home folder across restarts:

```terraform
resource "livellm_desktop_app" "studio" {
  name       = "studio"
  replicas   = 2
  resolution = "1920x1080"
  keep_files = true
  storage_gi = 20
}
```

## Schema

### Required

- `name` (String) Workload id. Changing it replaces the app.

### Optional

- `replicas` (Number) How many desktops, `1` to `20`. Defaults to `1`.
- `image` (String) A desktop image. Leave it out for the platform's (`ghcr.io/qalby-tech/livellm-desktop:xfce`); your own works if it follows that image's contract.
- `cpu` (String) CPU for each desktop, e.g. `2` (the default).
- `memory` (String) Memory for each desktop, e.g. `4Gi` (the default).
- `resolution` (String) Screen size, e.g. `1280x800` (the default).
- `keep_files` (Boolean) Give each desktop its own home folder that survives restarts. By default every desktop starts clean. Replaces the app when changed.
- `storage_gi` (Number) With `keep_files`: each desktop's home folder in GiB (10 when left out). Replaces the app when changed.
- `stopped` (Boolean) Stop every desktop without deleting the app. Home folders are kept and only they are billed.
- `timeouts` (Block) `create` (default 15m) / `delete` (default 10m).

### Read-Only

- `ready` (Boolean) Whether every desktop is up.
- `desktops_ready` (Number) How many desktops answer right now.

## Import

```shell
terraform import livellm_desktop_app.desks desks
```
