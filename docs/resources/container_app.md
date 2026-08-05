---
page_title: "livellm_container_app Resource - livellm"
description: |-
  A container app from a prebuilt image or a repo the platform builds for you.
---

# livellm_container_app (Resource)

Runs a container app. Give it a prebuilt `image`, or point `source_repo` at a
repo in your workspace's Git org and the platform builds and rolls it on every
push. Terraform waits until the app is serving, then reports the public URL of
every exposed port.

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

Wired to a database and a secret — no secret value passes through Terraform:

```terraform
resource "livellm_container_app" "api" {
  name        = "api"
  source_repo = "https://git.live-llm.com/acme/api" # built on every push

  env = {
    DB_HOST = one([for e in livellm_storage.db.endpoints : e.addr if e.name == "postgres"])
  }

  secret_env {
    name = "DB_PASSWORD"
    path = livellm_secret.db_password.path
  }

  port {
    name = "http"
    port = 8080
  }
}
```

## Schema

### Required

- `name` (String) Workload id. Changing it replaces the app.

### Optional

- `image` (String) Prebuilt image reference. Set `image` or `source_repo`, not both.
- `source_repo` (String) Build-from-source: a repo URL in the workspace Git org.
- `command` (List of String) Entrypoint override.
- `cpu` (String) CPU request, e.g. `500m`.
- `memory` (String) Memory request, e.g. `512Mi`.
- `env` (Map of String) Plain environment variables. Use `secret_env` for secrets.
- `secret_env` (Block List) Env vars backed by workspace secrets:
  - `name` (String, Required) Env var name inside the container.
  - `path` (String, Required) Workspace secret path providing the value.
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
