---
page_title: "livellm_vm Data Source - livellm"
description: |-
  Live state of one VM: readiness, SSH address and the real URL of every
  exposed port.
---

# livellm_vm (Data Source)

Reads one VM's live state from the platform: whether it's up, its SSH
address, and the public URL of every exposed port. This is the supported way
to reference a VM's addresses in outputs and other resources — the platform
reports the real URLs, so never build hostnames by hand.

## Example Usage

```terraform
data "livellm_vm" "dev" {
  id = "vm-1"
}

output "dev_ssh" {
  value = data.livellm_vm.dev.ssh # e.g. "203.0.113.7:32148"
}

# The first exposed HTTP port's public HTTPS URL:
output "dev_url" {
  value = data.livellm_vm.dev.url
}

# Or a specific port by name:
output "api_url" {
  value = one([for e in data.livellm_vm.dev.endpoints : e.url if e.name == "api"])
}
```

## Schema

### Required

- `id` (String) The VM's workload id.

### Read-Only

- `type` (String) Workload type: `vm-ubuntu`, `vm-ubuntu-desktop` or `vm-windows`.
- `ready` (Boolean) Whether the VM is up and reachable.
- `phase` (String) Lifecycle phase (`Pending`, `Running`, …).
- `ssh` (String) `host:port` to SSH into the VM. Empty until assigned.
- `url` (String) The first exposed HTTP port's public HTTPS URL. Empty when no HTTP port is exposed.
- `endpoints` (List of Object) Every exposed port:
  - `name` (String) Port name.
  - `url` (String) Public HTTPS URL — HTTP ports.
  - `addr` (String) `host:port` — raw TCP/UDP ports.
  - `tcp` (Boolean) True for a raw TCP/UDP port.
