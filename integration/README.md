# K3s integration test

Provision three disposable Incus VMs on a private bridge using the
[host setup](../host/README.md). Install the same K3s version on all three
and pre-stage the corresponding air-gap images if needed. Leave K3s stopped.
This fixture was verified with K3s v1.33.6+k3s1 on Debian 12.

Load a dedicated test SSH key into an agent, export the `LAB_*` variables
listed in [the test script](k3s-vm.nu), and run `nu --no-config-file integration/k3s-vm.nu` with
Nushell 0.116.1, Terraform, Go, Incus and unzip on PATH. The test revokes its worker bootstrap
token, restarts and removes the worker, and checks plans, state and logs for
test credential values. Dispose of the VMs after the run.
