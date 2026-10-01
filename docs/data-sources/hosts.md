---
page_title: "livellm_hosts Data Source - livellm"
description: |-
  The hosts resources can run on, with their region.
---

# livellm_hosts (Data Source)

Lists the hosts resources can run on, with their region: the values
`placement_host` and `placement_region` take on any resource.

## Example Usage

```terraform
data "livellm_hosts" "all" {}

locals {
  regions = distinct([for h in data.livellm_hosts.all.hosts : h.region if h.ready && h.schedulable && h.region != null])
}

resource "livellm_container_app" "near" {
  name  = "near"
  image = "nginx:1.27-alpine"

  placement_strategy = "region"
  placement_region   = local.regions[0]
}
```

## Schema

### Read-Only

- `hosts` (List of Object) One entry per host:
  - `id` (String) Host id, for `placement_host`.
  - `region` (String) Its region, for `placement_region`; null if it has none.
  - `zone` (String) Its zone within the region; null if it has none.
  - `ready` (Boolean) Whether it is up.
  - `schedulable` (Boolean) Whether it takes new resources: a host set aside
    is up but takes none.

A new or changed placement is accepted only on a host that is `ready` and
`schedulable`; a placement that does not change is not checked again.
