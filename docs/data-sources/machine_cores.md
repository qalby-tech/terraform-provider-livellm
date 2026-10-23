---
page_title: "livellm_machine_cores Data Source - livellm"
description: |-
  The cores a new machine with dedicated cores can get: the servers' clock speeds and how many cores of each are free.
---

# livellm_machine_cores (Data Source)

Reads what a new machine with `dedicated_cores = true` can get on the servers
the workspace may run on: the clock speeds the servers' cores run at, the
values `core_speed` takes, and how many cores of each one server has free.

It takes no arguments: the API key already names the workspace.

## Example Usage

The fastest cores the servers have, or any cores of one speed where every
server runs at one speed:

```terraform
data "livellm_machine_cores" "this" {}

resource "livellm_vm" "build" {
  name      = "build-box"
  cpus      = 8
  memory_gi = 16
  # …credentials…

  dedicated_cores = true
  core_speed      = try(data.livellm_machine_cores.this.speeds[0].speed, 0)
}

output "core_speeds" {
  # Fastest first: [{ speed = 5700, fit = 12 }, { speed = 4400, fit = 16 }]
  value = data.livellm_machine_cores.this.speeds
}
```

A speed's `fit` counts the cores free on one server (cores any workspace's
machines hold are not free), no more than the server has room for. Memory
isn't counted: it is judged when the machine is saved. A machine with more
cores than its speed's `fit` is refused ("Only 6 cores at 5.7 GHz are
available. Continue with mixed core speeds?"), and one with `core_speed` left
out and more cores than `dedicated_fit` is refused ("No server has 20 free
cores of one speed. Continue with cores of mixed speeds?", naming the cores
asked), unless `mixed_speeds = true` (see [`livellm_vm`](../resources/vm.md)). Where
the servers run at one speed (`show_speed` is `false`), or no server has room
for that many cores at all, the refusal is "No server has room for this
machine." instead, and `mixed_speeds` doesn't change it.

The counts are for a new machine: once a machine is made its cores are no
longer free, and the next plan reads less. Write `cpus` yourself rather than
from `fit` or `dedicated_fit`, or every plan after the apply would change it.
`speeds` lists each speed whether or not it has room, so a `core_speed` taken
from it changes only when the servers do.

## Schema

### Read-Only

- `show_speed` (Boolean) Whether a core speed can be asked for: the servers'
  cores run at more than one clock speed. `false` where they all run at one
  speed: leave `core_speed` out.
- `speeds` (List of Object) One entry per clock speed the servers have,
  fastest first; empty where `show_speed` is `false`:
  - `speed` (Number) The clock in MHz every core of this speed reaches, for
    `core_speed`.
  - `fit` (Number) The most cores of this speed a new machine can get on one
    server.
- `dedicated_fit` (Number) The most cores of one speed a new machine can get
  on one server, any speed.

Needs a LiveLLM API with dedicated cores: on an older one the read fails
("Cannot read the cores").
