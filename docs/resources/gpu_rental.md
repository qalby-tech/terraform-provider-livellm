---
page_title: "livellm_gpu_rental Resource - livellm"
description: |-
  Rents a GPU, or a cluster of GPUs on one server, by the hour; or books them for later.
---

# livellm_gpu_rental (Resource)

Rents a GPU for the workspace: one GPU (`model` and `use`), or a cluster of
GPUs on one server (a `cluster` block), now or booked for later
(`starts_at`). A rental for a machine (`use = "machine"`) goes to one machine
through its `gpu`; a rental for apps (`use = "apps"`) is shared by the apps
and Desktop Apps whose `gpu` names it.

**Renting spends money.** A rental now is charged by the minute until it
ends; a booking every booked hour from its start, used or not. GPUs are pay as
you go. [`livellm_gpu_models`](../data-sources/gpu_models.md) lists the models
with their price an hour and what is free.

Terraform never rents again by itself:

- A rental that ended (its end came, an administrator ended it, it was
  released elsewhere) **stays in state** with `status = "ended"` (or
  `"cancelled"`), and plans no change while its configuration is as it was.
  The workspace lists ended rentals for 30 days; one that drops out of the
  list stays in state too, as ended.
- Nothing about a rental is replaced. What the platform can change in place
  is changed: a later `ends_at` extends it, another `use` changes its use,
  fewer GPUs of a model in a `cluster` takes them off. Anything else (another
  model, more GPUs, an earlier end, another start, location or target) is an
  error at plan time.
- To rent anew on purpose, taint it (`terraform taint` or `tofu taint`) and
  apply, or remove the resource and add another. `-replace` works too while
  the configuration is as it was; with a changed configuration it is refused.

Destroying the resource releases the rental (charging stops; a booking keeps
its booked hours) or cancels the booking. A rental already over has nothing to
release. A booking cancelled within 24 hours of its start is charged in full:
the destroy stops with an error and cancels nothing unless
`allow_charged_cancel = true` was applied first (the plan warns ahead of it).
The same holds for taking booked GPUs off a cluster. The destroy says what the
rental is charged in all.

## Example Usage

```terraform
# One GPU now, for a machine, for four hours.
resource "livellm_gpu_rental" "train" {
  model   = "rtx5090"
  use     = "machine"
  ends_at = "2026-11-20T14:00:00Z"
}

resource "livellm_vm" "train" {
  name      = "train"
  cpus      = 8
  memory_gi = 32
  # …credentials…

  gpu = livellm_gpu_rental.train.id
}

# Two GPUs on one server, shared by apps, booked for tomorrow.
resource "livellm_gpu_rental" "serve" {
  starts_at = "2026-11-21T08:00:00Z"
  ends_at   = "2026-11-21T20:00:00Z"

  cluster {
    use   = "apps"
    cards = { rtx5090 = 2 }

    location {
      mode   = "region"
      region = "eu-west"
    }

    target {
      apps = ["inference"]
    }
  }
}
```

Release a machine's GPU by taking it off the machine first: a rental in use
can't be released (the destroy says what uses it). When the machine's `gpu`
reference and the rental are removed in one apply, Terraform updates the
machine before it releases the rental.

When a rental ends, the platform may take it off the machine or apps that
used it. A machine whose configuration still names the ended rental then
plans `gpu` back to it, which the platform refuses: set `gpu = ""`, or point
it at another rental.

## Who may rent

Renting, extending, changing use and releasing need the workspace's owner or
an API key a person gave the billing permission (the workspace's API keys
page). An agent sign-in can never rent. Reading (this resource's refresh,
the data sources) is open to every key.

Where the server doesn't rent GPUs (a self-hosted install, a cloud with
renting off) every write stops with "GPU renting isn't offered on this
server". The platform's other refusals are said in its words with their code:
no free GPU for those hours (`gpu_none_free`), a GPU with no price yet
(`gpu_not_offered`), pay as you go off (`payg_off`) or past its limit
(`payg_limit`), no card (`card_required`), more than 168 booked hours ahead
(`gpu_booking_cap`), a cluster of several GPUs where clusters aren't offered
(`gpu_clusters_off`), a change of use within the hour
(`gpu_change_too_soon`).

## Schema

### Optional

- `model` (String) One GPU of this model (`rtx5090`), with `use`. Or write a `cluster` block instead. Fixed once rented.
- `use` (String) With `model`: `machine` (one machine gets the GPU) or `apps` (apps and Desktop Apps share it). Another value on a running rental changes its use in place, at most once an hour and only while nothing uses it; the apply waits while the GPU gets ready for it (`timeouts.update`, 20 minutes by default).
- `location` (Block) With `model`: where the GPU may go, when it is rented. `mode` is `auto` (any server this workspace may use; the default), `region` (with `region`) or `host` (with `host`). Fixed once rented.
- `target` (Block) A booking only: what gets the GPU at its start, `machine` (machine use) or `apps` (a list of apps and Desktop Apps; apps use). Fixed once booked.
- `cluster` (Block) Several GPUs on one server, rented together, instead of `model` and `use`:
  - `use` (String) `machine` or `apps`, as above; changed in place for every GPU of the cluster.
  - `cards` (Map of Number) How many GPUs of each model, 1 to 8 each: `{ rtx5090 = 2 }`. Fewer of a model takes GPUs off in place (one that isn't working first, else the last one), only while nothing runs on the cluster; more, or another model, is refused at plan time.
  - `location` (Block), `target` (Block) As above.
- `starts_at` (String) Book for later: the start, a whole UTC hour (`2026-11-20T10:00:00Z`), with `ends_at`. Left out, the GPU is rented now. Fixed once rented.
- `ends_at` (String) The end, a whole UTC hour. Left out on a rental now, it runs until released, or until a booking ahead needs the GPU (the platform then sets the end, read here). A later value extends the rental in place; an earlier one, or an end on a rental with none, is refused at plan time.
- `allow_charged_cancel` (Boolean) Let a destroy, or taking booked GPUs off a cluster, go ahead when the platform charges it in full (a booking within 24 hours of its start). Off by default. Apply it first, then destroy.
- `timeouts` (Block) `update`: how long to wait for a change of use (default `20m`).

### Read-Only

- `id` (String) The rental's id (`g-…`): what a machine's, an app's or a Desktop App's `gpu` takes.
- `name` (String) `RTX 5090`, `2 × RTX 5090`.
- `status` (String) `booked`, `delayed` (a booking waiting for a free GPU), `active`, `ended` or `cancelled`.
- `phase` (String) `ready`, `waiting`, `switching-to-machine`, `switching-to-apps` or `failed`.
- `booked` (Boolean) Booked for later.
- `host`, `region` (String) The server the GPUs are on and its region.
- `started_at`, `ended_at`, `ended_reason` (String) When it started and ended, and why (`released`, `end`, `cancelled`, `admin`, …).
- `used_by` (List of String) The resources using it now.
- `cards` (List of Object) Its GPUs: `id`, `model`, `name`, `state`, `answering` (false while it isn't working).
- `hours_this_month` (Number), `cents_this_month` (Number) Its hours and charge this month in US cents; `cents_this_month` is null where it isn't charged.

Needs LiveLLM API 0.98 or newer for renting and 0.101 for clusters of
several GPUs. Import isn't offered.
