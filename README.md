# Terraform Provider for LiveLLM Cloud

Manage your [LiveLLM Cloud](https://cloud.live-llm.com) workspace as code —
reviewable, versioned, repeatable — with Terraform or OpenTofu. The provider
drives the same public API as the dashboard.

- Registry: [`qalby-tech/livellm`](https://registry.terraform.io/providers/qalby-tech/livellm)
- Provider documentation: [docs](docs/) (rendered on the Terraform Registry)
- Platform guide: [Terraform on LiveLLM Cloud](https://docs.live-llm.com/terraform)

## Requirements

- [Terraform](https://developer.hashicorp.com/terraform/downloads) ≥ 1.0
  (protocol 6) or [OpenTofu](https://opentofu.org)
- A LiveLLM Cloud workspace API key (`llc_…`) — mint one on your workspace's
  [API keys page](https://cloud.live-llm.com/api-keys)

## Quick start

```hcl
terraform {
  required_providers {
    livellm = {
      source = "qalby-tech/livellm"
    }
  }
}

provider "livellm" {}

data "livellm_workspace" "this" {}

output "workspace" {
  value = data.livellm_workspace.this.name
}
```

```sh
export LIVELLM_API_KEY="llc_…"
terraform init
terraform plan
```

> **OpenTofu:** use the fully-qualified source
> `registry.terraform.io/qalby-tech/livellm` until the provider is listed in
> the OpenTofu registry — everything else works identically.

## Authentication

Authenticate with a workspace API key — each key manages the workspace it
was minted in. Set it via the `LIVELLM_API_KEY` environment variable
(recommended) or the provider's `api_key` attribute.

## Status

Manage the core of a workspace as code today:

| Resource | What it creates |
|---|---|
| `livellm_vm` | Ubuntu VM (terminal or desktop), SSH keys, ports, a stop time, placement |
| `livellm_container_app` | An app from an image or a Git repo the platform builds — one service of a Composable App (`stack`, `hostname`, `starts_after`), HTTP, raw TCP/UDP and internal ports, volumes, stop and start |
| `livellm_storage` | Managed Postgres or Redis, backups, external TLS access |
| `livellm_browser` | Chromium with a live view and a CDP endpoint |

Data sources: `livellm_workspace`, `livellm_vm`, `livellm_vms`.

Creates and updates wait until the resource is actually serving, and plan-pool
exhaustion surfaces as a clear "raise your plan" diagnostic rather than a raw
HTTP error. Create/update timeouts are configurable per resource via the standard `timeouts` block.

~> Write-only arguments (`password_wo`) require Terraform 1.11+ or
OpenTofu 1.11+.

## Examples

- [`examples/basic`](examples/basic) — provider setup plus the
  `livellm_workspace` data source. Full per-resource examples live in the
  [registry documentation](https://registry.terraform.io/providers/qalby-tech/livellm/latest/docs).

## Developing the provider

```sh
go build ./...

# Point Terraform at your local build (skips the registry):
cat > ~/.terraformrc <<EOF
provider_installation {
  dev_overrides {
    "qalby-tech/livellm" = "$(pwd)"
  }
  direct {}
}
EOF
go build -o terraform-provider-livellm && cd examples/basic && terraform plan
```
