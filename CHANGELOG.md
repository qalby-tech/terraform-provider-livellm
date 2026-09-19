# Changelog

## Unreleased (breaking)

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
