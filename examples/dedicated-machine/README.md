# A machine with dedicated cores

Creates one machine, `build-box`, with 2 dedicated cores and 4 GiB of
memory, and prints the clock of the cores it got once it runs.

```sh
export LIVELLM_API_KEY=llc_…
# The machine's password; set it, or plan asks for it:
export TF_VAR_machine_password="…"
terraform init && terraform apply
```

`terraform destroy` deletes the machine and its disk.
