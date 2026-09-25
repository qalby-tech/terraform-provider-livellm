# Changelog

## Unreleased

- **Fixed** `livellm_storage` backups were never taken: `backup_schedule`
  sent a schedule without turning backups on, so the database had none and
  no error said so. It now turns them on.
- **Added** `livellm_storage` `backup { mode, keep_days }`. `mode` is `daily`
  (a full copy each night, the default), `continuous` (the nightly copy plus
  every change, so the database can be restored to any minute) or `manual`
  (nothing scheduled). `keep_days` is how many days backups are kept (10 by
  default). With the block backups are on, without it off, and a plan shows
  backups turned on or off in the console as a change. A block that leaves
  `mode` or `keep_days` out means `daily` and 10 days, so a mode or keep
  changed in the console is put back by the next apply.
- **Deprecated** `backup_schedule` and `backup_keep`. They still work: any
  schedule turns backups on (daily on a new database; a database switched to
  continuous in the console stays continuous), and `backup_keep` is the same number of days
  as `keep_days` (it always was days, not a number of backups; its
  description said otherwise). They can't be combined with `backup`.
- **Added** plan-time checks on `livellm_storage`: `instances` is 1 or 3,
  three instances and backups are Postgres only (Redis runs as one instance
  and has no backups).
- **Changed** `livellm_storage` `cpu`, `memory` and `disk_gi` read back the
  sizes the platform gives a database when they are left out (1, 1Gi, 5 GiB),
  and later changes send them back unchanged instead of dropping them. A
  database saved without sizes keeps having none: it no longer plans a change
  on every run, and is never given CPU or memory it didn't have.
- **Added** `livellm_vm` `backup { schedule, keep }`: scheduled backups of the
  disk, keeping the last `keep` (1..100 — a count, unlike a database's days).
  A schedule set in the console now shows in the plan; before, the next apply
  removed it without saying so.
- **Added** the `livellm_browser_api` resource: one address that drives
  several browsers. Name them in `browsers`, or drive every browser in the
  workspace with `all_browsers = true`; `remote_browser` blocks add browsers
  running somewhere else, with a write-only `auth_wo` header. A call that
  names no browser goes to the one with the fewest open tabs, a session stays
  on its browser, and `/browsers/<name>/…` or `X-Browser-Id` picks one.
  Import reads the browsers and the remote addresses. `remote_browser.has_auth`
  says whether a header is stored; it is planned from the configuration, so a
  header set or removed in the console shows as a change, and a plan that
  removes a stored header warns by remote id.
- **Added** to `livellm_container_app`:
  - `port { tcp = true }` and `port { udp = true }` — a raw port with a public
    `host:port` address instead of an HTTPS hostname, for anything that isn't
    HTTP (a game server, a VPN, DNS). The address is in `endpoints`.
  - `port { allow_cidrs = [...] }` — the source addresses allowed to reach a
    port. Unset, a port is open to anyone; a raw port takes no password, so
    this is its only protection.
  - `volume { name, size_gi, mount_path }` blocks, up to 8 — disks that keep
    their data across restarts, redeploys and stops. A volume grows in place;
    shrinking one is refused at plan time, and a plan that removes one warns
    by name, because removing it deletes its data. An app that had one disk
    before volumes existed has it as the volume `data`, and import reads it.
  - `stopped` — stop an app without deleting it: it runs nothing, keeps its
    volumes and is billed for their disk only. `false` starts it again.
  - Plan-time checks for all of these, matching the platform: a port is `tcp`
    or `udp`, not both and not `internal`; an internal port takes no
    `allow_cidrs`; `allow_cidrs` are real CIDRs; volume names are unique
    lowercase labels of at most 15 characters; mount paths are absolute and
    written plainly (letters, digits and `. _ @ + -`, at most 200 characters,
    no trailing slash), unique, not `/`, not in `/proc`, `/sys` or `/dev`, and
    not inside one another; the same number is not a tcp (or udp) port of
    one app twice.
  - Removing every `volume` block removes the app's volumes (the platform
    keeps them on a save that leaves them out, so the provider says so).
- **Fixed** `livellm_container_app` reads its ports back: a port changed
  outside Terraform (an allow-list lifted in the console) shows in the plan,
  and an import fills the `port` blocks.
- **Added** `udp` to every resource's and data source's `endpoints`: `true`
  for a raw UDP port, next to `tcp` for a raw TCP one.

## 0.7.0

- **Added** `os` to `livellm_vm`: `debian` (13) or `fedora` (44) servers next to
  Ubuntu. Changing it replaces the machine.

## 0.6.0

- **Added** to `livellm_container_app`: `stack`, `hostname` and `starts_after`
  — an app made of several services, which reach each other by hostname —
  and `port { internal = true }` for a port with no public address. Until now
  a service created in the console with these settings lost them on the next
  apply.
- **Added** `port { internal = true }` to `livellm_vm`: a machine port
  reachable from inside the workspace only, with no node port.

## 0.5.0 (breaking)

- **Removed** everything that managed AI features, which the platform no
  longer has: the `livellm_agent_master` and `livellm_ai_provider` resources,
  `ai_daemon` and `state_repo` on `livellm_vm`, and `ai_agent` on
  `livellm_browser`. Take them out of your configuration before upgrading.
- **Added** to `livellm_vm`:
  - `ssh_keys` — SSH public keys for that machine alone, installed next to the
    workspace's own. A change reaches a running machine within a minute or two.
    The platform keeps a machine's last keys, so an empty list is refused at
    plan time; replace a key, or leave `ssh_keys` out to stop managing it.
  - `stop_after` — have the platform stop the machine after a while (`"4h"`),
    keeping its disk. The clock starts when Terraform creates the machine,
    starts it again, or when the value changes — never on an apply that edits
    something else.
  - `expires_at` (read-only) — when the machine will be stopped.
- **Changed** the provider sends its changes to the workspace one at a time.
  Terraform applies up to ten resources at once, and they all belong to one
  workspace; sent together they queued behind each other on the platform and
  could come back as conflicts. Waiting for a resource to come up still
  happens in parallel.
- **Fixed** `livellm_container_app`: an app with no `image_auth` block (or no
  `source` block) was refused with "Missing Configuration for Required
  Attribute". What each block needs is now checked only when the block is
  there, at plan time, together with the image / source / image_auth rules
  that used to fail only at apply.

- **Changed** the provider calls the platform's shorter API paths
  (`/v1/workspace`, `/v1/status`, `/v1/workloads/…`), which never name the
  workspace: the API key already belongs to exactly one.
- **Removed** `livellm_secret`. The workspace secret store is gone; give an
  app its secret values directly with the new `secret_env` map on
  `livellm_container_app`.
- **Changed** `livellm_container_app`:
  - `source_repo` is replaced by a `source { git { url, ref, dockerfile,
    context } token }` block. Apps are built from any HTTPS Git repo with a
    Dockerfile; the platform no longer watches the repo — rebuild from the
    dashboard or the API.
  - `secret_env` is now a sensitive map of `NAME = value` (was a block list of
    `name` + workspace secret `path`).
  - **Added** an `image_auth { username, password }` block to pull private
    images. The password is sensitive and write-only on the platform; it is
    not allowed together with `source`.

## 0.4.0 and earlier

See the Git tags.
