---
page_title: "livellm_vms Data Source - livellm"
description: |-
  Every VM in the workspace with its live state.
---

# livellm_vms (Data Source)

Lists every VM in the workspace with its live state — for building maps of
ids to URLs, feeding `for_each`, or asserting fleet health, without
hardcoding ids.

## Example Usage

```terraform
data "livellm_vms" "all" {}

output "vm_urls" {
  value = { for vm in data.livellm_vms.all.vms : vm.id => vm.url }
}

output "all_ready" {
  value = alltrue([for vm in data.livellm_vms.all.vms : vm.ready])
}
```

## Schema

### Read-Only

- `vms` (List of Object) One entry per VM:
  - `id` (String) Workload id.
  - `type` (String) `vm-ubuntu`, `vm-ubuntu-desktop` or `vm-windows`.
  - `ready` (Boolean) Whether the VM is up.
  - `phase` (String) Lifecycle phase.
  - `ssh` (String) `host:port` for SSH. Empty until assigned.
  - `url` (String) First exposed HTTP port's URL, if any.
  - `endpoints` (List of Object) Every exposed port (`name`, `url`, `addr`, `tcp`, `udp`).
