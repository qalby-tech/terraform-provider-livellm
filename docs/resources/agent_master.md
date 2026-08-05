---
page_title: "livellm_agent_master Resource - livellm"
description: |-
  The workspace's single orchestrator agent.
---

# livellm_agent_master (Resource)

Enables the workspace's AI Master — a single orchestrator agent that plans
across your workloads, drives every agent-enabled resource, and can provision
new ones through the same API Terraform uses. One per workspace.

Destroying the resource disables the master (its pod is torn down); your
workloads are untouched.

## Example Usage

```terraform
resource "livellm_agent_master" "master" {
  display_name = "Master 1"
  provider_id  = "anthropic"
  autonomous   = true

  instructions = <<-EOT
    Prefer small, reversible changes. Ask before deleting anything.
  EOT
}
```

## Schema

### Required

- `provider_id` (String) A connected AI provider id (Integrations page).

### Optional

- `id` (String) Master id; defaults to `master-1`. Changing it replaces the master.
- `display_name` (String) Name shown in the console.
- `model` (String) Model id; defaults to the provider's recommendation.
- `instructions` (String) Global standing guidance.
- `autonomous` (Boolean) Act without per-plan confirmation. Defaults to `false`.
- `auto_provision` (Boolean) Allow the master to provision new resources on its own. Defaults to `false`.

## Import

```shell
terraform import livellm_agent_master.master master-1
```
