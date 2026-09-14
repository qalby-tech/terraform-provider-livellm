---
page_title: "livellm_container_app Resource - livellm"
description: |-
  A container app from a prebuilt image or a Git repo the platform builds for you.
---

# livellm_container_app (Resource)

Runs a container app. Give it a prebuilt `image`, or a `source` block pointing
at a Git repo with a Dockerfile: the platform clones the repo, builds the image
and runs it. Rebuild whenever you like from the dashboard or the API (the
platform never polls your repo). Terraform waits until the app is serving, then
reports the public URL of every exposed port.

## Example Usage

```terraform
resource "livellm_container_app" "web" {
  name   = "web"
  image  = "nginx:1.27-alpine"
  cpu    = "200m"
  memory = "256Mi"

  env = {
    NODE_ENV = "production"
  }

  port {
    name = "http"
    port = 80
  }
}

output "web_url" {
  value = livellm_container_app.web.url
}
```

Built from a Git repo, wired to a managed database, with a secret value that
the platform stores write-only:

```terraform
resource "livellm_container_app" "api" {
  name = "api"

  source {
    git {
      url        = "https://github.com/acme/api.git"
      ref        = "main"          # branch, tag or commit; unset = default branch
      dockerfile = "Dockerfile"    # relative to the build context
      context    = "services/api"  # subdirectory of the repo; unset = repo root
    }
    token = var.github_token       # only for private repos
  }

  env = {
    DB_HOST = one([for e in livellm_storage.db.endpoints : e.addr if e.name == "postgres"])
  }

  secret_env = {
    DB_PASSWORD = var.db_password
  }

  port {
    name = "http"
    port = 8080
  }
}
```

A private image, pulled with registry credentials:

```terraform
resource "livellm_container_app" "grafana" {
  name  = "grafana"
  image = "ghcr.io/acme/grafana:11"

  image_auth {
    username = "acme-bot"
    password = var.ghcr_token
  }

  port {
    name = "http"
    port = 3000
  }
}
```

## Schema

### Required

- `name` (String) Workload id. Changing it replaces the app.

### Optional

- `image` (String) Prebuilt image reference. Set `image` or a `source` block, not both.
- `source` (Block) Build the image from a Git repo:
  - `git` (Block, Required) The repo to build:
    - `url` (String, Required) HTTPS clone URL.
    - `ref` (String) Branch, tag (`refs/tags/v1`) or commit to build. Unset = the repo's default branch.
    - `dockerfile` (String) Dockerfile path relative to the build context. Defaults to `Dockerfile`.
    - `context` (String) Build context, a subdirectory of the repo. Defaults to the repo root.
  - `token` (String, Sensitive) Access token for a private repo. Stored write-only by the platform; re-sent only when it changes.
- `image_auth` (Block) Credentials for pulling a private `image`. Not allowed with `source`:
  - `username` (String, Required) Registry username.
  - `password` (String, Required, Sensitive) Registry password or access token. Stored write-only by the platform; sent from configuration on every apply.
- `command` (List of String) Entrypoint override.
- `cpu` (String) CPU request, e.g. `500m`.
- `memory` (String) Memory request, e.g. `512Mi`.
- `env` (Map of String) Plain environment variables.
- `secret_env` (Map of String, Sensitive) Environment variables with secret
  values. The platform stores the values write-only — API responses carry the
  names only, so a value changed outside Terraform is re-asserted from your
  configuration on the next apply.
- `port` (Block List) Exposed ports:
  - `name` (String, Required) Port name — becomes part of the hostname.
  - `port` (Number, Required) Container port.

### Read-Only

- `ready` (Boolean) Whether the app is running.
- `url` (String) The first exposed port's public HTTPS URL.
- `endpoints` (List of Object) Every exposed port (`name`, `url`, `addr`, `tcp`).

## Import

```shell
terraform import livellm_container_app.web web
```

Secret values, the repo token and the image password cannot be read back;
set them in configuration after importing.
