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

## Authentication

A single workspace API key is all the configuration there is. The key **is**
the workspace identity — there is no workspace name, project id or region to
wire up; each key manages exactly the workspace it was minted in. Set it via
the `LIVELLM_API_KEY` environment variable (recommended) or the provider's
`api_key` attribute.

## Status

Early preview. Shipped today:

- Provider configuration with a fail-fast credential check at plan time
- `livellm_workspace` data source (workspace facts)

Typed resources — VMs, managed Postgres/Redis, container apps, browsers,
write-only secrets and AI agents — are in active development. The
[`examples/preview`](examples/preview) directory shows the planned
configuration shapes, one file per domain.

## Examples

- [`examples/basic`](examples/basic) — provider setup plus the
  `livellm_workspace` data source.
- [`examples/preview`](examples/preview) — the planned resource shapes: VMs
  (terminal/desktop, AI daemons), browsers, container apps (image or
  build-from-git), managed databases with backups, secrets, AI provider
  connections and the workspace AI Master.

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
