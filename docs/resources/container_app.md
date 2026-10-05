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
across restarts, and `stopped` keeps them while the app runs nothing. A
`database` block links a managed database: the app may reach it, and its
connection details can reach the app as environment variables, the password
without ever passing through Terraform.

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

An app linked to its managed databases. Each `database` block names a
database and which of its details go into which variables; the password and
the URL are read from the database's own login when the app starts, so they
are never in the app's settings or in state. The app starts once its
databases accept connections. The link is also what lets the app reach each
database: a database is reached only by what links it. A key that made the app
and the databases needs no Network permission for it.

```terraform
resource "livellm_storage" "db" {
  name                = "cloud-db"
  engine              = "postgres"
  disk_gi             = 10
  password_wo         = var.db_password
  password_wo_version = 1
}

resource "livellm_storage" "cache" {
  name                = "cloud-cache"
  engine              = "redis"
  disk_gi             = 1
  password_wo         = var.cache_password
  password_wo_version = 1
}

resource "livellm_container_app" "cloud" {
  name   = "cloud"
  image  = "nextcloud:stable-apache"
  cpu    = "1"
  memory = "2Gi"

  database {
    name = livellm_storage.db.name
    env = {
      POSTGRES_HOST     = "host"
      POSTGRES_DB       = "database"
      POSTGRES_USER     = "username"
      POSTGRES_PASSWORD = "password"
    }
  }

  database {
    name = livellm_storage.cache.name
    env = {
      REDIS_HOST          = "host"
      REDIS_HOST_PORT     = "port"
      REDIS_HOST_PASSWORD = "password"
    }
  }

  port {
    name = "http"
    port = 80
  }

  volume {
    name       = "html"
    size_gi    = 20
    mount_path = "/var/www/html"
  }
}
```

A reach-only link: the app may connect to the database and takes no
variables from it (it has its own settings), so the block adds no wait, and
adding or removing it never restarts the app.

```terraform
resource "livellm_container_app" "worker" {
  name  = "worker"
  image = "ghcr.io/acme/worker:2.1"

  database {
    name = livellm_storage.cache.name
  }
}
```

Built from a Git repo, with a secret value that the platform stores
write-only:

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

  secret_env = {
    STRIPE_KEY = var.stripe_key
  }

  port {
    name = "http"
    port = 8080
  }
}
```

An app made of several services: they reach each other by their short names,
on any port, whatever `reachable_from` says. A port marked `internal` has no
public address. Here the machine `worker` may reach the app too; one value
covers every service of the stack, so it is written on one service only.

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

  reachable_from = [livellm_vm.worker.name]

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

Where it runs is optional: by default LiveLLM picks the host. A region
runs it on any of the region's hosts:

```terraform
data "livellm_hosts" "all" {}

resource "livellm_container_app" "near" {
  name  = "near"
  image = "nginx:1.27-alpine"

  placement_strategy = "region" # or "host" with placement_host
  placement_region   = [for h in data.livellm_hosts.all.hosts : h.region if h.ready && h.schedulable && h.region != null][0]
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
- `stack` (String) The app this service belongs to, when an app is made of several services. The services of one stack are one resource: they always reach each other by `hostname` on any port (a `hostname` means something only inside its stack, so two stacks may both have a `db`); other resources reach a service as its `reachable_from` says, at `<workspace>-<name>`. Through a key, putting a service into a stack needs the **Network** permission unless the key made that service and every service already in the stack. Lowercase letters, digits and hyphens, starting with a letter.
- `hostname` (String) This service's name inside its stack. Defaults to `name`.
- `starts_after` (List of String) Names of the apps and databases this service needs first. It starts once each one's first port accepts a connection, it reaches each of them whatever their `reachable_from` says, and none of them can be deleted while it lists them. (`depends_on` is Terraform's own word, hence the name.)
- `stopped` (Boolean) Stop the app without deleting it: it runs nothing, its
  volumes are kept and only their disk is billed. Defaults to `false`; setting
  it back to `false` starts the app again.
- `port` (Block List) Exposed ports. A port is HTTP unless it says otherwise:
  - `name` (String, Required) Port name — becomes part of the hostname. Lowercase letters, digits and hyphens, at most 15 characters.
  - `port` (Number, Required) Container port.
  - `tcp` (Boolean) A raw TCP port instead of HTTP: a public `host:port` address (in `endpoints`) rather than an HTTPS hostname — a game server, a mail server, anything that isn't HTTP.
  - `udp` (Boolean) A raw UDP port with a public `host:port` address — a VPN, DNS, voice. A port is `tcp` or `udp`, not both; add a second port for the other protocol. One number can be a tcp port and a udp port of the same app, but not the same protocol twice.
  - `internal` (Boolean) No public address: the port is only an address inside the workspace, `<workspace>-<name>:<port>` (and `<hostname>:<port>` for the services of its stack), for the resources `reachable_from` lets in; any TCP protocol. Not with `tcp`, `udp` or `allow_cidrs`.
  - `allow_cidrs` (List of String) Source addresses allowed to reach the port, as CIDRs (`203.0.113.0/24`; one address is `203.0.113.7/32`). Unset = anyone. A raw port takes no password, so this is its only protection: set it unless the port is meant for the public.

  Ports are read back on every refresh, so a port changed outside Terraform
  (an allow-list lifted in the console) shows in the plan. A password on an
  HTTP port is set in the console only; an apply writes the ports as
  configured, without it.
- `database` (Block List, at most 8) A managed database of the workspace
  this app uses:
  - `name` (String, Required) The database's name, e.g. `livellm_storage.db.name` (which also has Terraform create the database first and delete it last).
  - `env` (Map of String) Environment variable name → the detail it carries:

    | Detail | PostgreSQL | Redis |
    |---|---|---|
    | `host` | its private address | its private address |
    | `port` | `5432` | `6379` |
    | `database` | `app` | — |
    | `username` | the login's name | — |
    | `password` | the password | the password |
    | `url` | `postgres://user:password@host:5432/app` | `redis://:password@host:6379` |

    0 to 12 variables per block. A variable name is letters, digits and `_`,
    not starting with a digit, and is used once in the app, across `env`,
    `secret_env` and every `database` block; the plan checks all of it.
    Left out (or `{}`), the link is **reach only**: the app gets no
    variables and doesn't wait for the database, so adding or removing the
    block never restarts the app. For start order without variables use
    `starts_after`, which reaches too.

  The link lets the app, with every service of its `stack`, reach the
  database: a database has no `reachable_from` and is reached only by what
  links it. The password and `url` come from the database's stored login:
  nothing secret is written to the app's settings or to state. A database the
  link takes variables from is waited for before the app starts (no
  `starts_after` needed), and a linked database can't be deleted while an app
  links it. Through an API key the link needs the **Network** permission
  unless the key made both this app and the database. A database whose
  password was set before
  links existed can't give `url` until its password is set once more: bump
  `password_wo_version` on its `livellm_storage` (the apply says so).
  Linked apps read a new password when they restart. Links are read back on
  every refresh.
- `volume` (Block List, at most 8) Disks that keep their data when the app
  restarts, is redeployed or is stopped:
  - `name` (String, Required) Lowercase letters, digits and hyphens, at most 15 characters. A new name is a new, empty volume.
  - `size_gi` (Number, Required) Size in GiB. It grows in place; a smaller size is refused at plan time.
  - `mount_path` (String, Required) Where it appears in the container: an absolute path such as `/data`, folder names of letters, digits and `. _ @ + -`, at most 200 characters, no trailing slash. Not `/`, not in `/proc`, `/sys` or `/dev`, and not inside another volume's path.

  **Removing a `volume` block deletes that volume and everything on it.** The
  plan warns about it by name before you apply. An app with volumes runs one
  copy of itself.
- `reachable_from` (List of String) Which other resources of the workspace may connect to this one: their names, or `["*"]` for the whole workspace, also resources made later (`"*"` goes alone). Left out when creating: none. Removing it later keeps the value the resource has; `[]` closes it. A replacement is a new resource: it starts from this attribute, and other resources lose its name (see [Replacing a resource](../index.md#replacing-a-resource)). Through a key, letting more in needs the **Network** permission, except between resources the key made itself or when this resource already lets the whole workspace in; setting `["*"]` always needs it (see [Inside the workspace](../index.md#inside-the-workspace)). The services of one `stack` are one resource: they always reach each other and share one value, so set it on one service and leave it out of the others, or write the same value on each. A service's name or its `stack` in another resource's list stands for all the services. A new service of an app that is already there takes the app's value when this is left out, and naming another service of its own `stack` is refused at apply.
- `placement_strategy` (String) Where it runs: omit for automatic (the default; LiveLLM picks the host), `region` for any host in `placement_region`, `host` to pin `placement_host`. Changing it restarts the resource where it now belongs. A resource pinned to a host waits for that host while it is down. An app with volumes and a location stops before its new copy starts, so each change briefly takes it offline.
- `placement_region` (String) Region to run in (`placement_strategy = "region"`).
- `placement_host` (String) Host id to pin to (`placement_strategy = "host"`); ids come from the [`livellm_hosts`](../data-sources/hosts.md) data source.

### Read-Only

- `ready` (Boolean) Whether the app is running (`false` while stopped).
- `url` (String) The first HTTP port's public HTTPS URL.
- `endpoints` (List of Object) Every exposed port (`name`, `url`, `addr`, `tcp`, `udp`). An HTTP port has a `url`; a raw port has `addr` (`host:port`) with `tcp` or `udp` set.

## Import

```shell
terraform import livellm_container_app.web web
```

Secret values, the repo token and the image password cannot be read back;
set them in configuration after importing. An import reads the app's ports,
database links and volumes:
write each one as a `volume` block before the first apply, or the plan warns
that it would be deleted.
