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
reports the public address of every exposed port: an HTTPS URL for an HTTP
port, a `host:port` for a raw TCP or UDP port. Volumes keep the app's data
across restarts, and `stopped` keeps them while the app runs nothing.

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

An app made of several services: they reach each other by their short names,
on any port. A port marked `internal` has no public address.

```terraform
resource "livellm_container_app" "db" {
  name     = "shop-db"
  stack    = "shop"
  hostname = "db"
  image    = "postgres:17"

  port {
    name     = "pg"
    port     = 5432
    internal = true
  }
}

resource "livellm_container_app" "web" {
  name         = "shop-web"
  stack        = "shop"
  hostname     = "web"
  image        = "ghcr.io/acme/shop:1.4"
  starts_after = [livellm_container_app.db.name]

  env = {
    DATABASE_HOST = "db"
  }

  port {
    name = "http"
    port = 3000
  }
}
```

A game server: a raw TCP port open to one network, a raw UDP port open to
everyone, and two volumes. Its addresses are in `endpoints`:

```terraform
resource "livellm_container_app" "mc" {
  name  = "minecraft"
  image = "itzg/minecraft-server"

  env = {
    EULA = "TRUE"
  }

  port {
    name        = "game"
    port        = 25565
    tcp         = true
    allow_cidrs = ["203.0.113.0/24"]
  }

  port {
    name = "voice"
    port = 9987
    udp  = true
  }

  volume {
    name       = "world"
    size_gi    = 20
    mount_path = "/data"
  }

  volume {
    name       = "backups"
    size_gi    = 10
    mount_path = "/backups"
  }
}

output "minecraft_address" {
  value = one([for e in livellm_container_app.mc.endpoints : e.addr if e.name == "game"])
}
```

Set `stopped = true` to stop an app you don't need right now: it runs nothing,
its volumes are kept, and only their disk is billed. Set it back to `false` (or
remove it) to start the app again.

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
- `stack` (String) The app this service belongs to, when an app is made of several services. Services of one stack reach each other by `hostname` on any port, and only they can; two stacks may both have a `db`. Lowercase letters, digits and hyphens, starting with a letter.
- `hostname` (String) This service's name inside its stack. Defaults to `name`.
- `starts_after` (List of String) Names of the apps and databases this service needs first. It starts once each one's first port accepts a connection, and none of them can be deleted while it lists them. (`depends_on` is Terraform's own word, hence the name.)
- `stopped` (Boolean) Stop the app without deleting it: it runs nothing, its
  volumes are kept and only their disk is billed. Defaults to `false`; setting
  it back to `false` starts the app again.
- `port` (Block List) Exposed ports. A port is HTTP unless it says otherwise:
  - `name` (String, Required) Port name — becomes part of the hostname. Lowercase letters, digits and hyphens, at most 15 characters.
  - `port` (Number, Required) Container port.
  - `tcp` (Boolean) A raw TCP port instead of HTTP: a public `host:port` address (in `endpoints`) rather than an HTTPS hostname — a game server, a mail server, anything that isn't HTTP.
  - `udp` (Boolean) A raw UDP port with a public `host:port` address — a VPN, DNS, voice. A port is `tcp` or `udp`, not both; add a second port for the other protocol. One number can be a tcp port and a udp port of the same app, but not the same protocol twice.
  - `internal` (Boolean) No public address: reachable from inside the workspace only, at `<workspace>-<name>:<port>` (and at `<hostname>:<port>` for the services of its stack); any TCP protocol. Not with `tcp`, `udp` or `allow_cidrs`.
  - `allow_cidrs` (List of String) Source addresses allowed to reach the port, as CIDRs (`203.0.113.0/24`; one address is `203.0.113.7/32`). Unset = anyone. A raw port takes no password, so this is its only protection: set it unless the port is meant for the public.

  Ports are read back on every refresh, so a port changed outside Terraform
  (an allow-list lifted in the console) shows in the plan. A password on an
  HTTP port is set in the console only; an apply writes the ports as
  configured, without it.
- `volume` (Block List, at most 8) Disks that keep their data when the app
  restarts, is redeployed or is stopped:
  - `name` (String, Required) Lowercase letters, digits and hyphens, at most 15 characters. A new name is a new, empty volume.
  - `size_gi` (Number, Required) Size in GiB. It grows in place; a smaller size is refused at plan time.
  - `mount_path` (String, Required) Where it appears in the container: an absolute path such as `/data`, folder names of letters, digits and `. _ @ + -`, at most 200 characters, no trailing slash. Not `/`, not in `/proc`, `/sys` or `/dev`, and not inside another volume's path.

  **Removing a `volume` block deletes that volume and everything on it.** The
  plan warns about it by name before you apply. An app with volumes runs one
  copy of itself.

### Read-Only

- `ready` (Boolean) Whether the app is running (`false` while stopped).
- `url` (String) The first HTTP port's public HTTPS URL.
- `endpoints` (List of Object) Every exposed port (`name`, `url`, `addr`, `tcp`, `udp`). An HTTP port has a `url`; a raw port has `addr` (`host:port`) with `tcp` or `udp` set.

## Import

```shell
terraform import livellm_container_app.web web
```

Secret values, the repo token and the image password cannot be read back;
set them in configuration after importing. An import reads the app's ports
and volumes:
write each one as a `volume` block before the first apply, or the plan warns
that it would be deleted.
