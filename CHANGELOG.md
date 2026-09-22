# Changelog

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
