---
page_title: "livellm_browser_api Resource - livellm"
description: |-
  A Browser API: one address that drives several browsers.
---

# livellm_browser_api (Resource)

A Browser API gives several browsers one address. A call that names no browser
goes to the one with the fewest open tabs, a session stays on the browser it
started on, and `/browsers/<name>/…` or the `X-Browser-Id` header picks one.
Add browsers to handle more work; the address and the clients don't change.

Pick its browsers by name, or drive every browser in the workspace with
`all_browsers = true`. A workspace browser belongs to at most one Browser API.
Browsers running somewhere else join with `remote_browser` blocks.

How to call it is on the docs site's Browser API page
(<https://docs.live-llm.com/browser-api>).

## Example Usage

```terraform
resource "livellm_browser" "a" {
  name = "agent-1"
}

resource "livellm_browser" "b" {
  name = "agent-2"
}

resource "livellm_browser_api" "scrapers" {
  name     = "scrapers"
  browsers = [livellm_browser.a.name, livellm_browser.b.name]
}
```

Every browser in the workspace, plus one of your own:

```terraform
resource "livellm_browser_api" "all" {
  name         = "all-browsers"
  all_browsers = true

  remote_browser {
    id      = "office"
    ws_url  = "wss://browser.example.com/devtools/browser/main"
    auth_wo = var.office_browser_auth   # "Bearer …", or "X-Token: …"
  }
  remote_auth_version = 1
}
```

## Schema

### Required

- `name` (String) Resource id; it is part of the address. Changing it replaces
  the Browser API.

### Optional

- `browsers` (Set of String) The workspace browsers it drives, by name. A
  browser can be in one Browser API only; adding one that another Browser API
  drives is refused until it is taken out there.
- `all_browsers` (Boolean) Drive every browser in the workspace, including ones
  made later. Defaults to `false`. Can't be combined with `browsers`.
- `remote_browser` (Block List) A browser running somewhere else:
  - `id` (String) The name calls use for it. Lowercase letters, digits and
    dashes; not the name of a workspace browser.
  - `ws_url` (String) Its CDP websocket address, `ws://` or `wss://`.
  - `auth_wo` (String, Sensitive, Write-only) A header the remote browser
    needs, never stored in state. `Name: value` sends that header; anything
    else (`Bearer abc`) is sent as `Authorization`. It is sent whenever
    Terraform creates or updates the Browser API. A remote browser without
    it has its stored header removed on that update: the plan shows
    `has_auth` going to `false` and warns.
- `remote_auth_version` (Number) Change it to send new `auth_wo` values when
  nothing else changed: a write-only value alone makes no plan.
- `cpu` (String) CPU request, e.g. `500m`.
- `memory` (String) Memory request, e.g. `1Gi`.
- `timeouts` (Block) `create` / `delete` (default 10m each).

A Browser API needs at least one of `browsers`, `all_browsers = true` or a
`remote_browser` block.

### Read-Only

- `ready` (Boolean) Whether the Browser API is up.
- `remote_browser.has_auth` (Boolean) Whether a header is stored for that
  remote browser. The header itself is never read back. It is planned from the
  configuration (`true` with `auth_wo`, `false` without), so a header set or
  removed in the console shows as a change on the next plan.

## Import

```shell
terraform import livellm_browser_api.scrapers scrapers
```

Import reads the browsers, the remote browsers' addresses and whether each has
a header stored; the headers themselves are never read back. Add `auth_wo` to
the configuration before the next apply: a remote browser with a header stored
and no `auth_wo` plans `has_auth` to `false`, with a warning, and the apply
removes its header.
