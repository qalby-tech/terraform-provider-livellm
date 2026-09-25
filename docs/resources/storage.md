---
page_title: "livellm_storage Resource - livellm"
description: |-
  A managed Postgres or Redis database with optional backups and external TLS access.
---

# livellm_storage (Resource)

Creates a managed database — Postgres or Redis. Terraform waits until it is
healthy, then reports its connection endpoints. The password is a write-only
argument.

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

## Schema

### Required

- `name` (String) Workload id. Changing it replaces the database.
- `engine` (String) `postgres` or `redis`. Changing it replaces the database.
- `password_wo` (String, Sensitive, Write-only) Database password — not stored in state.
- `password_wo_version` (Number) Rotation trigger for `password_wo`.

### Optional

- `version` (String) Engine major version, e.g. `16`.
- `disk_gi` (Number) Disk size in GiB, 5 when unset. Growing is an in-place update; shrinking is refused.
- `instances` (Number) `1`, or `3` for Postgres: two standby copies, one of which takes over if the main one fails. Redis runs as one instance; `3` is refused at plan time.
- `cpu` (String) CPU, e.g. `1` or `500m`; `1` when unset.
- `memory` (String) Memory, e.g. `1Gi`; `1Gi` when unset.
- `username` (String) Application username (Postgres). Changing it replaces the database.
- `expose` (Boolean) Expose the database externally over TLS.
- `allowlist` (List of String) Client CIDRs/IPs allowed when exposed. Empty = no IP restriction.
- `backup` (Block) Backups, Postgres only. With the block backups are on; without it they are off (a plan shows backups turned on or off elsewhere as a change).
  - `mode` (String) `daily` (the default): a full copy each night. `continuous`: the nightly copy plus every change in between, restorable to any minute inside `keep_days`. `manual`: nothing scheduled; backups taken by hand are kept.
  - `keep_days` (Number) How many days backups are kept, `1`..`365`; `10` when unset.
- `backup_schedule` (String, Deprecated) Use `backup`. Any value turns on daily backups; the schedule itself is no longer used.
- `backup_keep` (Number, Deprecated) Use `backup.keep_days`. Days backups are kept (it always was days); only with `backup_schedule`.

The same database can't use `backup` and the deprecated attributes together.

### Read-Only

- `ready` (Boolean) Whether the database is up.
- `endpoints` (List of Object) Connection endpoints (`name`, `url`, `addr`, `tcp`, `udp`) — in-cluster and, when exposed, external.

## Import

```shell
terraform import livellm_storage.db app-db
```
