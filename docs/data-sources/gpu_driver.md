---
page_title: "livellm_gpu_driver Data Source - livellm"
description: |-
  A machine's NVIDIA driver: whether it works, is installing, needs a restart or failed.
---

# livellm_gpu_driver (Data Source)

Reads a machine's NVIDIA driver. Every machine made on LiveLLM API 0.101.9 or
newer (Ubuntu, Debian, Fedora and Windows) installs the driver by itself when
it has an NVIDIA card, at its first start and after each kernel update; this
reads where that is. Nothing here installs it: a machine made before the
driver program gets it from the console (Install NVIDIA driver) or with
`livellm driver install ID`. The platform never restarts a machine for it.

## Example Usage

```terraform
resource "livellm_vm" "train" {
  name = "train"
  gpu  = livellm_gpu_rental.one.id
  # …
}

data "livellm_gpu_driver" "train" {
  vm = livellm_vm.train.id
}

output "driver" {
  value = "${data.livellm_gpu_driver.train.state} ${data.livellm_gpu_driver.train.detail}"
}
```

The read asks the running machine, so it is made at each plan: a machine
being created in the same plan is read after it exists.

## Schema

### Required

- `vm` (String) The machine's id (`livellm_vm`'s `id`).

### Read-Only

- `state` (String) `none` (no NVIDIA card), `off` (turned off inside the
  machine), `working`, `restart-needed` (installed: restart the machine to
  finish), `installing`, `pending` (not run yet), `failed`, `unsupported` (its
  system gets no automatic driver), `not-installed` (a machine made before
  the driver program), `unknown`; or `machine-stopped` and
  `machine-unreachable` when the machine can't be read now, so a plan never
  fails for a stopped machine (`detail` says why).
- `detail` (String) More about the state: the driver's version when working,
  the reason when failed. Empty when there is none.
- `log` (List of String) The last lines of the driver program's log in the
  machine.

Needs LiveLLM API 0.101.9 or newer: on an older one the read fails ("Cannot
read the NVIDIA driver"). A machine whose system gets no automatic driver
fails the read too, in the API's words.
