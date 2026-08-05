---
page_title: "livellm_workspace Data Source - livellm"
description: |-
  Facts about the workspace the configured API key belongs to.
---

# livellm_workspace (Data Source)

Reads the workspace the configured API key belongs to. Useful for building
hostnames in outputs, asserting the plan tier, and as a fail-fast check that
the credential is valid — an invalid key fails at plan time with a clear
message instead of midway through an apply.

It takes no arguments: the API key already names the workspace.

## Example Usage

```terraform
data "livellm_workspace" "this" {}

output "workspace" {
  value = data.livellm_workspace.this.name
}
```

For a VM's addresses — SSH, port URLs — use
[`livellm_vm`](vm.md) / [`livellm_vms`](vms.md): the platform reports the
real URLs, so there is never a reason to assemble hostnames by hand.

## Schema

### Read-Only

- `name` (String) Workspace (tenant) name.
- `plan` (String) Plan the workspace runs on.
- `owner` (String) Owning user id.
