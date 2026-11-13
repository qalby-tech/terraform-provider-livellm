---
page_title: "livellm_gpu_models Data Source - livellm"
description: |-
  The GPU models the workspace can rent, with their price an hour and what is free.
---

# livellm_gpu_models (Data Source)

Reads the GPU models the workspace can rent: the price an hour, how many
there are, how many are free now for a machine and for apps, how long a
rental starting now could last, and, with `from` and `to`, the free GPUs in
each hour. A model with no price yet isn't offered to a customer's workspace
(it is left out there).

## Example Usage

```terraform
data "livellm_gpu_models" "next_day" {
  use  = "machine"
  from = "2026-11-21T08:00:00Z"
  to   = "2026-11-21T20:00:00Z"
}

output "gpu_prices" {
  # e.g. { rtx5090 = 60 }
  value = { for m in data.livellm_gpu_models.next_day.models : m.model => m.cents_hour }
}
```

The counts change as GPUs are rented, so write a rental's model and hours
yourself rather than from them.

## Schema

### Optional

- `use` (String) Which use `slots` counts free GPUs for: `apps` (the default) or `machine`.
- `from`, `to` (String) Whole UTC hours, at most 14 days apart, both or neither: the hours `slots` covers.

### Read-Only

- `models` (List of Object) One entry per model:
  - `model` (String) What `livellm_gpu_rental`'s `model` takes (`rtx5090`).
  - `name` (String) `RTX 5090`.
  - `cents_hour` (Number) The price an hour in US cents; null for a model with no price yet (listed only to a platform workspace).
  - `total` (Number) How many there are.
  - `free_now_machine`, `free_now_apps` (Number) Free now for each use.
  - `free_until_machine`, `free_until_apps` (String) How long a rental starting now could last; null for no limit.
  - `slots` (List of Number) With `from` and `to`: the free GPUs in each hour, for `use`.

Any key and agent may read it. Where the server has no GPUs at all the read
fails with "GPU renting isn't offered on this server".
