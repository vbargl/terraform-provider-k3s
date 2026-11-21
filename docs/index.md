---
page_title: "K3s Provider"
description: "Coordinate K3s membership on Console-managed hosts."
---

# K3s Provider

Console owns the complete system and its shared inventory. Its nxf area
manages the OS and installs the host-side service definitions. Its Terraform
RouterOS area manages networking. The K3s provider starts and verifies K3s
membership over that private network. Workloads and services have their own
lifecycle.

Use one resource per node. Set the bootstrap node explicitly and reference
its observed cluster UID from each join resource. Any Terraform backend may
be used. Use a backend with locking when multiple operators share state.
An independent state must never manage the same host or node.

The provider needs an SSH agent and a trusted SHA256 fingerprint of the Ed25519 host key
for each host. The agent stays on the Terraform runner. Kubeconfigs and SSH
private keys have no provider arguments or outputs. Join tokens pass through
provider memory and encrypted SSH streams to root-only runtime files on nodes.
K3s itself persists credentials in its private data directories. Anyone with
root on a member server or control of the Terraform runner can access join
credentials. Protect these systems and their backups accordingly.
