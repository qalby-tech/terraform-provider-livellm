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

The release pipeline is ready: pushing a `v*` tag builds, signs and publishes
release assets via GitHub Actions, and the Registry ingests every future tag
automatically. One-time setup (account-level, can't be automated):

1. Make this repo **public** — the Registry only indexes public repos.
2. Generate a signing key and add it to the repo secrets:
   ```sh
   gpg --full-generate-key            # RSA 4096, no expiry, e.g. releases@live-llm.com
   gpg --armor --export-secret-keys <KEY_ID>   # → secret GPG_PRIVATE_KEY
   # the key's passphrase                      # → secret PASSPHRASE
   gpg --armor --export <KEY_ID>               # public key, used in step 3
   ```
3. Sign in at registry.terraform.io with the qalby-tech GitHub org →
   Settings → Signing keys → add the public key.
4. Registry → Publish → Provider → pick this repo.
5. Tag the release: `git tag v0.1.0 && git push origin v0.1.0`.

Repeat releases are just step 5.
