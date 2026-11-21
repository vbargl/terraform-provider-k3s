---
page_title: "k3s_node Resource"
description: "Initialize a K3s server or reconcile an individual K3s member."
---

# k3s_node

Create the first embedded-etcd server with `role = "bootstrap"`. Use the
`server` or `agent` role for other nodes. For HA, add enough servers to
reach an odd-sized etcd quorum. The provider never selects a replacement
bootstrap server. The registration URL is supplied by Console and already
reachable from the joining nodes.

## Arguments

- `name` is the unique Kubernetes node name and a DNS label.
- `role` is `bootstrap`, `server`, or `agent`.
- `host` and `host_key_sha256` specify the management host and port and its
  verified Ed25519 SSH host-key fingerprint.
- `ssh_user` is the provisioned restricted SSH account on the node and anchor.
- `registration_url` is the private HTTPS origin served by the cluster.
- `anchor_host` and `anchor_key_sha256` identify a pinned, ready K3s server
  for cluster operations.
- `cluster_uid` is the kube-system namespace UID. Reference the bootstrap
  resource attribute from joining resources. Bootstrap computes this value.
- `reconcile_revision` is an optional integer. Change it to request another
  apply on a tracked node, such as after correcting a stopped service.

Bootstrap omits the registration and anchor fields. The provider reports the
Kubernetes Node UID as `id` and verified API and node health as `ready`.
Plan and refresh never request a join token.

The host helper writes its intent before starting K3s and rejects unmanaged
K3s data. Repeat a failed apply with the same inputs after fixing host or
network problems. Keep the intent and K3s data together on restore. An SSH
failure or another node appearing under the same name cannot erase Terraform
state. On a known-good restored server, update the pinned management address
if the original anchor becomes permanently unavailable. Recover the
datastore and matching server token before trying to attach nodes.

Destroy of an agent stops its service and deletes its Kubernetes Node using
the recorded UID. Workload eviction and fencing policy belong to the
operator. The host retains K3s data and a retirement marker. Reprovision or
explicitly clean the retired host before reuse. Destroy of a bootstrap
node or additional server is currently blocked. Retire servers through a
quorum-aware operator procedure and remove their Terraform bindings after
verifying the remaining cluster.

See the [host setup and recovery guide](https://github.com/vbargl/terraform-provider-k3s/blob/main/host/README.md)
for required service definitions and credential handling.
