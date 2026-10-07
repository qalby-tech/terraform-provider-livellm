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

A desktop that may reach a database or an object storage (reach only: nothing
is put into the desktop, and nothing restarts):

```terraform
resource "livellm_desktop_app" "analyst" {
  name = "analyst"

  database {
    name = livellm_storage.db.name
  }
}
```

A desktop in one region (by default LiveLLM picks the host):

```terraform
resource "livellm_desktop_app" "eu_desk" {
  name = "eu-desk"

  placement_strategy = "region" # or "host" with placement_host
  placement_region   = "eu-1"
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
- `database` (Block List, at most 8) A database or object storage of the workspace this Desktop App may reach. Reach only: nothing is put into the desktop (no variables) and nothing restarts. A database is reached only by what links it, and it can't be deleted while a Desktop App links it. Through an API key the link needs the **Network** permission unless the key made both this Desktop App and the database. Links are read back on every refresh, and an apply warns when the platform didn't keep them.
  - `name` (String, Required) The database's name, e.g. `livellm_storage.db.name`.
- `reachable_from` (List of String) Which other resources of the workspace may connect to this one: their names, or `["*"]` for the whole workspace, also resources made later (`"*"` goes alone). Left out when creating: none. Removing it later keeps the value the resource has; `[]` closes it. A replacement is a new resource: it starts from this attribute, and other resources lose its name (see [Replacing a resource](../index.md#replacing-a-resource)). Through a key, letting more in needs the **Network** permission, except between resources the key made itself or when this resource already lets the whole workspace in; setting `["*"]` always needs it (see [Inside the workspace](../index.md#inside-the-workspace)). Its screen asks no password inside the workspace, so whatever reaches it can work on the desktop.
- `placement_strategy` (String) Where it runs: omit for automatic (the default; LiveLLM picks the host), `region` for any host in `placement_region`, `host` to pin `placement_host`. Changing it restarts the resource where it now belongs. A resource pinned to a host waits for that host while it is down.
- `placement_region` (String) Region to run in (`placement_strategy = "region"`).
- `placement_host` (String) Host id to pin to (`placement_strategy = "host"`); ids come from the [`livellm_hosts`](../data-sources/hosts.md) data source.

### Read-Only

- `ready` (Boolean) Whether the desktop is up.

## Import

```shell
terraform import livellm_desktop_app.studio studio
```
