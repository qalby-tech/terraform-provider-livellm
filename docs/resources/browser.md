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

## Schema

### Required

- `name` (String) Workload id. Changing it replaces the browser.

### Optional

- `cpu` (String) CPU request, e.g. `1`.
- `memory` (String) Memory request, e.g. `2Gi`.
- `timeouts` (Block) `create` / `delete` (default 10m each).

### Read-Only

- `ready` (Boolean) Whether the browser is up.

## Import

```shell
terraform import livellm_browser.scraper scraper
```
