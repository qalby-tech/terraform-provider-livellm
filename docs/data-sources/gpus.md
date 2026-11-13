---
page_title: "livellm_gpus Data Source - livellm"
description: |-
  The workspace's GPUs: rentals booked, waiting and running, and those that ended in the last 30 days.
---

# livellm_gpus (Data Source)

Reads the workspace's GPUs: its rentals booked, waiting and running, those
that ended in the last 30 days, and each GPU an administrator set for the
workspace (`no_end`). It takes no arguments: the API key already names the
workspace.

## Example Usage

```terraform
data "livellm_gpus" "this" {}

output "running_gpus" {
  value = [for r in data.livellm_gpus.this.rentals : r.id if r.status == "active"]
}
```

## Schema

### Read-Only

- `rentals` (List of Object) One entry per rental (a cluster of one or more GPUs):
  - `id` (String) What a machine's, an app's or a Desktop App's `gpu` takes.
  - `name` (String) `RTX 5090`, `2 × RTX 5090`.
  - `model` (String) Its (first) model.
  - `use` (String) `machine` or `apps`.
  - `status` (String) `booked`, `delayed`, `active`, `ended` or `cancelled`.
  - `phase` (String) `ready`, `waiting`, `switching-to-machine`, `switching-to-apps` or `failed`.
  - `starts_at`, `ends_at` (String) Its hours; `ends_at` null for none.
  - `ended_reason` (String) Why it ended.
  - `booked` (Boolean) Booked for later.
  - `no_end` (Boolean) A GPU an administrator set for the workspace, with no end.
  - `host`, `region` (String) The server its GPUs are on and its region.
  - `used_by` (List of String) The resources using it now.
  - `cards` (List of Object) Its GPUs: `id`, `model`, `name`, `state`, `answering`.
  - `hours_this_month` (Number), `cents_this_month` (Number) Its hours and charge this month (US cents; null where it isn't charged).

Any key and agent may read it. Where the server has no GPUs at all the read
fails with "GPU renting isn't offered on this server".
