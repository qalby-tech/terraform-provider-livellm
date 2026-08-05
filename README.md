# terraform-provider-livellm

Terraform/OpenTofu provider for [LiveLLM Cloud](https://cloud.live-llm.com) —
manage workspace resources (VMs, container apps, managed databases, browsers,
secrets) as code against the public API.

Design doc: `docs/terraform-provider.md` in the `qalby-tech/cluster` repo.

## Status

v0 scaffold. Working today:

- provider config (`api_key` / `LIVELLM_API_KEY`, optional `endpoint`) with an
  auth check at configure time
- `data.livellm_workspace`

v0.1 roadmap (in order): `livellm_secret`, `livellm_storage`,
`livellm_container_app`, `livellm_vm` — each with async Ready-polling and
plan-pool (402) diagnostics.

## Usage

```hcl
terraform {
  required_providers {
    livellm = { source = "qalby-tech/livellm" }
  }
}

provider "livellm" {} # export LIVELLM_API_KEY=llc_…
```

The key is all the configuration there is — no workspace name, nothing else
to wire up. Mint keys on the workspace's API keys page.

## Examples

- [`examples/basic`](examples/basic) — provider setup + the optional
  `livellm_workspace` data source (workspace facts, fail-fast auth check).
- [`examples/preview`](examples/preview) — every planned resource shape, one
  file per domain: VMs (terminal/desktop, AI daemons, stop-without-destroy),
  browsers (agent-driven, Browser API, BYO), container apps (image and
  build-from-git, volumes, secret_env), managed Postgres/Redis (backups,
  public endpoints, DB-engineer agent), write-only secrets, AI provider
  connections, and the workspace AI Master.

## Development

```sh
go build ./...

# run against a local override (no registry needed):
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

## Publishing (Terraform Registry)

Releasing is one command: `git tag vX.Y.Z && git push origin vX.Y.Z`. The
GitHub Actions release workflow builds per-platform zips, SHA256SUMS, a
detached GPG signature and the registry manifest; the Registry ingests every
tag automatically once the provider is published.

One-time setup (done for v0.1.0, recorded for the next provider):

1. Repo **public** — the Registry only indexes public repos.
2. Repo secret `GPG_PRIVATE_KEY` = ASCII-armored private signing key
   (RSA 4096, no passphrase — the workflow expects none).
3. Registry publishing now lives in HCP Terraform: registry.terraform.io →
   sign in with GitHub → "Start managing with HCP Terraform" →
   Registry → **Public Namespaces** → claim the `qalby-tech` namespace
   (GitHub-org ownership check) → add the **public** key under GPG Keys.
4. Publish → Provider → pick this repo.

Gotcha that cost a release: the key signing the release (repo secret) and
the key registered in the namespace must be the same pair. A mismatched
signature fails registry ingestion — verify locally with
`gpg --verify …SHA256SUMS.sig …SHA256SUMS` before publishing.
