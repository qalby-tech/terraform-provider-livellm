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

  backup_schedule = "0 3 * * *"
  backup_keep     = 10
}

output "db_endpoints" {
  value = livellm_storage.db.endpoints
}
```

Exposed externally, restricted to your office IPs:

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
- `disk_gi` (Number) Disk size in GiB. Growing is an in-place update; shrinking is not supported.
- `instances` (Number) Instance count (Postgres HA).
- `cpu` (String) CPU request.
- `memory` (String) Memory request.
- `username` (String) Application username (Postgres). Changing it replaces the database.
- `expose` (Boolean) Expose the database externally over TLS.
- `allowlist` (List of String) Client CIDRs/IPs allowed when exposed. Empty = no IP restriction.
- `backup_schedule` (String) Postgres backups: `@daily` etc. or a 5-field cron expression.
- `backup_keep` (Number) How many scheduled backups to retain.

### Read-Only

- `ready` (Boolean) Whether the database is up.
- `endpoints` (List of Object) Connection endpoints (`name`, `url`, `addr`, `tcp`, `udp`) — in-cluster and, when exposed, external.

## Import

```shell
terraform import livellm_storage.db app-db
```
