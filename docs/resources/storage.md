---
page_title: "livellm_storage Resource - livellm"
description: |-
  A managed Postgres or Redis database, or an object storage (S3), with optional backups, an admin console and external access.
---

# livellm_storage (Resource)

Creates a managed database — Postgres or Redis — or an object storage
(`engine = "s3"`: an S3 server of your workspace's own, with buckets). Terraform
waits until it is healthy, then reports its connection endpoints. The password is a write-only
argument. An app uses it through a `database` block on
[`livellm_container_app`](container_app.md): the app gets the address, the
login and the password as environment variables, read from the database's own
login, so the password never has to be passed to the app.

Inside the workspace a database is reached only by what links it: an app's
`database` block (with or without variables) or `starts_after`, and a
`database` block on a [`livellm_vm`](vm.md) or a
[`livellm_desktop_app`](desktop_app.md). It has no `reachable_from`. Its
external address (`expose`) has its own `allowlist`.

## Example Usage

```terraform
resource "livellm_storage" "db" {
  name    = "app-db"
  engine  = "postgres"
  version = "16"
  disk_gi = 20

  username            = "app"
  password_wo         = var.db_password
  password_wo_version = 1

  backup {
    mode      = "continuous" # daily | continuous | manual
    keep_days = 14
  }
}

output "db_endpoints" {
  value = livellm_storage.db.endpoints
}
```

Backups are Postgres only. `daily` takes a full copy each night; `continuous`
adds every change in between, so the database can be restored to any minute
inside `keep_days`; `manual` schedules nothing and keeps backups taken by hand.
`keep_days` is days, not a number of backups. A backup restores into a new
database (the console's Backups tab, the API's
`POST /v1/workloads/{id}/backups/{backup}/restore`, or `livellm restore`);
the database it came from keeps running.

Three instances — two standby copies, one of which takes over when the main one
fails — are Postgres only:

```terraform
resource "livellm_storage" "main" {
  name      = "main-db"
  engine    = "postgres"
  instances = 3
  # …credentials…

  backup {} # daily, kept 10 days
}
```

Exposed externally, restricted to your office IPs. The address is
`<name>-<workspace>.cloud.live-llm.com` — Postgres on port 5432, Redis on 6380 —
and it speaks TLS only (`sslmode=require`, `rediss://`):

```terraform
resource "livellm_storage" "cache" {
  name   = "cache"
  engine = "redis"

  password_wo         = var.redis_password
  password_wo_version = 1

  expose    = true
  allowlist = ["203.0.113.0/24"]
}
```

### Object storage

`engine = "s3"` makes an S3 server of the workspace's own on its own disk
(`disk_gi`, which can grow). It starts with one bucket, `app`; make more in its
admin console or with any S3 tool. `username` is its access key (left out, one
is generated) and `password_wo` its secret key, 8 to 128 characters with no
space, tab or line break at either end (wrap a key read from a file in
`trimspace()`). Apps link it with a `database` block like any database
and get its endpoint, keys, region and bucket as variables (see
[`livellm_container_app`](container_app.md)); clients must use path-style
addressing. The region is `us-east-1`.

```terraform
resource "livellm_storage" "files" {
  name    = "files"
  engine  = "s3"
  disk_gi = 20

  password_wo         = var.files_secret_key
  password_wo_version = 1

  admin_console = true # the RustFS console, signed in with the keys
  expose        = true # https://files-<workspace>.cloud.live-llm.com, path-style
  allowlist     = ["203.0.113.0/24"]
}
```

- **One copy, no backups.** Deleting a file, a bucket or the object storage is
  final. A `backup` block, `instances` other than `1` and a `version` other
  than `"1"` are refused at plan time.
- **A new secret key** (bump `password_wo_version`) restarts the object storage
  for a few seconds; linked apps read it when they restart. Keys or users made
  in the console stay: check them there.
- **The admin console** is at
  `https://<name>-admin-<workspace>.cloud.live-llm.com/rustfs/console/` and
  signs in with the access key and secret key. Its address also answers S3
  requests signed with the keys. `allowlist` limits both addresses. An apply
  that removes the allowlist while the console stays on warns at plan time;
  `allowlist = []` opens them on purpose.
- `endpoints` lists `s3` (inside the workspace, `http://…:9000`) and, with
  `expose`, `s3-external`.

### Admin console

`admin_console` turns on the database's admin console: pgAdmin for Postgres,
Redis Commander for Redis, the RustFS console for object storage. Left out,
the console keeps the state it has, also one switched on in the dashboard.
Turning it on sends `password_wo` in the same apply (the platform needs it,
since the console signs in with it), so keep `password_wo` set to the current
password. pgAdmin and Redis Commander answer from any network and sign in
with `admin` and the database password: `allowlist` covers only the database's
exposed address. For object storage it covers the console too.

### Placement

Where it runs is optional: by default LiveLLM picks the host.

```terraform
resource "livellm_storage" "eu_db" {
  name   = "eu-db"
  engine = "postgres"

  password_wo         = var.db_password
  password_wo_version = 1

  placement_strategy = "region" # or "host" with placement_host
  placement_region   = "eu-1"
}
```

## Schema

### Required

- `name` (String) Workload id. Changing it replaces the database.
- `engine` (String) `postgres`, `redis` or `s3` (object storage). Changing it replaces the database.
- `password_wo` (String, Sensitive, Write-only) Database password, or an object storage's secret key — not stored in state. Sent again when `password_wo_version` changes and when `admin_console` is turned on.
- `password_wo_version` (Number) Rotation trigger for `password_wo`.

### Optional

- `version` (String) Engine major version, e.g. `16`. Object storage runs `"1"`; another is refused at plan time.
- `disk_gi` (Number) Disk size in GiB, 5 when unset. Growing is an in-place update; shrinking is refused.
- `instances` (Number) `1`, or `3` for Postgres: two standby copies, one of which takes over if the main one fails. Redis and object storage run as one instance; `3` is refused at plan time.
- `cpu` (String) CPU, e.g. `1` or `500m`; `1` when unset.
- `memory` (String) Memory, e.g. `1Gi`; `1Gi` when unset.
- `username` (String) Application username (Postgres), or an object storage's access key. Left out, the platform names it `app` (an object storage gets a generated access key), and leaving it out later keeps the name the database has. Changing it replaces the database.
- `expose` (Boolean) Expose the database externally over TLS. An object storage gets an HTTPS S3 address, path-style.
- `allowlist` (List of String) Client CIDRs/IPs allowed when exposed. Empty = no IP restriction. It covers the database's exposed address only, not pgAdmin or Redis Commander; for an object storage it covers its S3 address and its admin console's.
- `admin_console` (Boolean) The admin console (pgAdmin, Redis Commander, or the RustFS console for object storage). pgAdmin and Redis Commander answer from any network and sign in with `admin` and the database password. Left out, it keeps the state the database has. Turning it on sends `password_wo` in the same apply; for object storage that restarts it for a few seconds.
- `backup` (Block) Backups, Postgres only (object storage keeps one copy and has none). With the block backups are on; without it they are off (a plan shows backups turned on or off elsewhere as a change).
  - `mode` (String) `daily` (the default): a full copy each night. `continuous`: the nightly copy plus every change in between, restorable to any minute inside `keep_days`. `manual`: nothing scheduled; backups taken by hand are kept.
  - `keep_days` (Number) How many days backups are kept, `1`..`365`; `10` when unset.
- `placement_strategy` (String) Where it runs: omit for automatic (the default; LiveLLM picks the host), `region` for any host in `placement_region`, `host` to pin `placement_host`. Changing it restarts the resource where it now belongs. A resource pinned to a host waits for that host while it is down. With `host`, every copy of the database (`instances = 3`) runs on that host.
- `placement_region` (String) Region to run in (`placement_strategy = "region"`).
- `placement_host` (String) Host id to pin to (`placement_strategy = "host"`); ids come from the [`livellm_hosts`](../data-sources/hosts.md) data source.

### Read-Only

- `ready` (Boolean) Whether the database is up.
- `endpoints` (List of Object) Connection endpoints (`name`, `url`, `addr`, `tcp`, `udp`) — in-cluster and, when exposed, external (an object storage's are `s3` and `s3-external`).

## Import

```shell
terraform import livellm_storage.db app-db
```

An import reads every setting the platform holds, an object storage's
`version = "1"` included: a configuration that leaves `version` out plans one
update that changes nothing on the platform, then plans clean.
