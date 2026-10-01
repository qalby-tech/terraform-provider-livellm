---
page_title: "livellm_browser Resource - livellm"
description: |-
  A headless Chromium browser with a live view and a CDP endpoint.
---

# livellm_browser (Resource)

Creates a headless Chromium browser. Watch it live from the console, or drive
it from your own automation over its CDP endpoint.

## Example Usage

```terraform
resource "livellm_browser" "scraper" {
  name   = "scraper"
  memory = "2Gi"
}
```

Pinned to one host (ids come from the `livellm_hosts` data source):

```terraform
resource "livellm_browser" "pinned" {
  name = "pinned"

  placement_strategy = "host"
  placement_host     = "host-1"
}
```

## Schema

### Required

- `name` (String) Workload id. Changing it replaces the browser.

### Optional

- `cpu` (String) CPU request, e.g. `1`.
- `memory` (String) Memory request, e.g. `2Gi`.
- `timeouts` (Block) `create` / `delete` (default 10m each).
- `placement_strategy` (String) Where it runs: omit for automatic (the default; LiveLLM picks the host), `region` for any host in `placement_region`, `host` to pin `placement_host`. Changing it restarts the resource where it now belongs. A resource pinned to a host waits for that host while it is down.
- `placement_region` (String) Region to run in (`placement_strategy = "region"`).
- `placement_host` (String) Host id to pin to (`placement_strategy = "host"`); ids come from the [`livellm_hosts`](../data-sources/hosts.md) data source.

### Read-Only

- `ready` (Boolean) Whether the browser is up.

## Import

```shell
terraform import livellm_browser.scraper scraper
```
