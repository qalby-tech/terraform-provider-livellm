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

- **No backups.** Deleting a file, a bucket or the object storage is final. A
  `backup` block and a `version` other than `"1"` are refused at plan time.
- **1 server or 3.** Left out, `instances` is `1`: one server. `instances = 3`
  makes 3 servers, one on each of 3 hosts, that keep reading and writing when
  one host is lost (see below).
- **A new secret key** (bump `password_wo_version`) restarts the object storage
  for a few seconds; linked apps read it when they restart. Keys or users made
  in the console stay: check them there.
- **The admin console** is at
  `https://<name>-admin-<workspace>.cloud.live-llm.com/rustfs/console/` and
  signs in with the access key and secret key. Its address also answers S3
  requests signed with the keys. `allowlist` limits both addresses. An apply
  that removes the allowlist while the console stays on warns at plan time;
  `allowlist = []` opens them on purpose. A console that is on while the
  configuration leaves `admin_console` out (turned on in the dashboard, for
  one), with no `allowlist` and no `expose`, warns at every plan: set
  `admin_console = false` to turn it off, or set `allowlist`.
- `endpoints` lists `s3` (inside the workspace, `http://…:9000`) and, with
  `expose`, `s3-external`.

#### 3 servers

```terraform
resource "livellm_storage" "files" {
  name      = "files"
  engine    = "s3"
  instances = 3 # one server on each of 3 hosts; fixed once made
  disk_gi   = 20

  password_wo         = var.files_secret_key
  password_wo_version = 1
}
```

- It needs 3 hosts the workspace can use; on fewer, the apply is refused
  ("needs 3 hosts, one server on each … make it with 1 server") and nothing
  is made. It can't be pinned to one host (`placement_strategy = "host"` is
  refused at plan time); automatic or a region works.
- Each server has the `cpu` and `memory` given, so it counts 3 times the cpu
  and memory toward the plan, and 1.5 times `disk_gi` (each server's disk is
  half of `disk_gi`, rounded up to whole GiB).
- **Its servers are fixed when it is made.** Changing `instances` on an
  existing object storage, 1 to 3 or 3 to 1, is refused at plan time; it is
  never replaced, since that would delete its files. To move, make a new object
  storage under another name and copy the files over.
- `servers_desired` and `servers_ready` say how many servers it runs and how
  many are ready; it keeps serving with one down. Both are null on a store of
  one server and on any other engine.

### Admin console

`admin_console` turns on the database's admin console: pgAdmin for Postgres,
Redis Commander for Redis, the RustFS console for object storage. Left out,
the console keeps the state it has, also one switched on in the dashboard.
Turning it on sends `password_wo` in the same apply (the platform needs it,
since the console signs in with it), so keep `password_wo` set to the current
password: a different one changes the password (for object storage, its
secret key) although the plan shows only `admin_console`. A plan that turns
the console on without a new `password_wo_version` warns about it. pgAdmin and Redis Commander answer from any network and sign in
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

Like every resource, a database or an object storage needs room on a server it
may run on when it is new, started, grown or moved, else the apply stops with
"This server is full: it has no room for this database." (or "No server has
room for this database."; for object storage, "this object storage"): stop or
shrink another resource, or give this one fewer cores or less memory.

## Schema

### Required

- `name` (String) Workload id. Changing it replaces the database.
- `engine` (String) `postgres`, `redis` or `s3` (object storage). Changing it replaces the database.
- `password_wo` (String, Sensitive, Write-only) Database password, or an object storage's secret key — not stored in state. Sent again when `password_wo_version` changes and when `admin_console` is turned on.
- `password_wo_version` (Number) Rotation trigger for `password_wo`.

### Optional

- `version` (String) Engine major version, e.g. `16`. Object storage runs `"1"`; another is refused at plan time.
- `disk_gi` (Number) Disk size in GiB, 5 when unset. Growing is an in-place update; shrinking is refused.
- `instances` (Number) `1` (the default) or `3`. For Postgres, `3` is two standby copies, one of which takes over if the main one fails. For object storage, `3` is 3 servers, one on each of 3 hosts (it needs 3 hosts; never pinned to one), fixed when it is made: a change is refused at plan time. Redis runs as one instance; `3` is refused at plan time. Left out, the database keeps the instances it has (a configuration that drops `instances = 3` keeps 3; write `instances = 1` to go back to one on Postgres).
- `cpu` (String) CPU, e.g. `1` or `500m`; `1` when unset.
- `dedicated_cores` (Boolean) Whole cores of its server for it alone, all of `cpu`, instead of shared cores (the default): a quarter of `cpu` reserved, all of it usable, and slower when the server is busy. On servers whose cores have 2 threads, dedicated cores come in pairs (2, 4, 6…), and the platform refuses another count at apply, in its own words. A dedicated core counts as 2 of the plan's cores (and as 2 cores on pay as you go). Per instance, for Postgres and Redis; an object storage has none (`true` there is an error at plan time). Left out, it keeps what it has, the console's choice included (`false` on a new one); `false` makes them shared.
- `memory` (String) Memory, e.g. `1Gi`; `1Gi` when unset.
- `username` (String) Application username (Postgres), or an object storage's access key. Left out, the platform names it `app` (an object storage gets a generated access key), and leaving it out later keeps the name the database has. Changing it replaces the database.
- `expose` (Boolean) Expose the database externally over TLS. An object storage gets an HTTPS S3 address, path-style.
- `allowlist` (List of String) Client CIDRs/IPs allowed when exposed. Empty = no IP restriction. It covers the database's exposed address only, not pgAdmin or Redis Commander; for an object storage it covers its S3 address and its admin console's.
- `admin_console` (Boolean) The admin console (pgAdmin, Redis Commander, or the RustFS console for object storage). pgAdmin and Redis Commander answer from any network and sign in with `admin` and the database password. Left out, it keeps the state the database has. Turning it on sends `password_wo` in the same apply (a plan doing it without a new `password_wo_version` warns); for object storage that restarts it for a few seconds.
- `backup` (Block) Backups, Postgres only (object storage has none). With the block backups are on; without it they are off (a plan shows backups turned on or off elsewhere as a change).
  - `mode` (String) `daily` (the default): a full copy each night. `continuous`: the nightly copy plus every change in between, restorable to any minute inside `keep_days`. `manual`: nothing scheduled; backups taken by hand are kept.
  - `keep_days` (Number) How many days backups are kept, `1`..`365`; `10` when unset.
- `placement_strategy` (String) Where it runs: omit for automatic (the default; LiveLLM picks the host), `region` for any host in `placement_region`, `host` to pin `placement_host`. Changing it restarts the resource where it now belongs. A resource pinned to a host waits for that host while it is down. With `host`, every copy of a Postgres database (`instances = 3`) runs on that host; an object storage of 3 servers can't be pinned to one host (refused at plan time).
- `placement_region` (String) Region to run in (`placement_strategy = "region"`).
- `placement_host` (String) Host id to pin to (`placement_strategy = "host"`); ids come from the [`livellm_hosts`](../data-sources/hosts.md) data source.

### Read-Only

- `ready` (Boolean) Whether the database is up.
- `running_cpu_model` (String) The CPU model of the server the running database or object storage runs on, shared or dedicated cores (`"Intel Core i9-14900K"`). Null while it isn't running, and from a platform that doesn't say.
- `servers_desired` (Number) An object storage of 3 servers: how many servers it runs (`3`). Null for anything else.
- `servers_ready` (Number) An object storage of 3 servers: how many of its servers are ready. It keeps serving with one down. Null for anything else.
- `endpoints` (List of Object) Connection endpoints (`name`, `url`, `addr`, `tcp`, `udp`) — in-cluster and, when exposed, external (an object storage's are `s3` and `s3-external`).

## Import

```shell
terraform import livellm_storage.db app-db
```

An import reads every setting the platform holds. An object storage's
`version` reads back as the platform holds it: `"1"` for one made in the
dashboard, nothing for one made through the API without a version. Where the
two differ from the configuration (`version` left out, or `version = "1"`
for one that holds none), the first plan after the import shows one update
that changes nothing on the platform, then plans clean.
