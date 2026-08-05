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

# e.g. build the URL a VM's HTTP port will be served on
output "app_url" {
  value = "https://vm-1-${data.livellm_workspace.this.name}.cloud.live-llm.com"
}
```

## Schema

### Read-Only

- `name` (String) Workspace (tenant) name.
- `plan` (String) Plan the workspace runs on.
- `owner` (String) Owning user id.
