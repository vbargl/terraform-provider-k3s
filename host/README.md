# Host setup

Console supplies identities and private routes from its inventory. Its nxf
area installs the K3s binary, Nushell 0.116.1 and the systemd service definitions.
Install `k3s-coordinate.nu` as root at `/usr/local/sbin/k3s-coordinate` with mode
0755, with all parent directories writable only by trusted administrators.
On NixOS, package the helper with an absolute Nushell path and preserve
the `--no-config-file --stdin` arguments. Use an `env -S` shebang with
store paths or a launcher that passes the flags as separate arguments.
Put K3s on the system PATH and use the packaged helper and sudo paths in
the SSH restriction and sudo rule. The helper also requires
util-linux `flock`, GNU coreutils and GNU findutils.
Provision an SSH account with a dedicated authorized key. Force the command
at each key, allowing no forwarding, shell, or PTY.

```text
restrict,command="/usr/bin/sudo -n /usr/local/sbin/k3s-coordinate" ssh-ed25519 PUBLIC_KEY
```

The account's sudo policy grants the exact helper with no arguments
(`/usr/local/sbin/k3s-coordinate ""` in sudoers). Restrict SSH ingress to
the Terraform runner. Pin the Ed25519 server key from a trusted provisioning channel.
The authorized key is a cluster-administrator equivalent on an anchor
because it can request the server token. Restrict it to one cluster and use
one dedicated key per cluster.

Provide an enabled `k3s.service` or `k3s-agent.service` unit for the host's
intended role. Its ExecStart must use the correct K3s subcommand and
`--config=/var/lib/k3s-coordinate/k3s.yaml`. Add
`ConditionPathExists=/var/lib/k3s-coordinate/k3s.yaml` under Unit and leave
initial start to the provider. Add
`ExecCondition=/usr/local/sbin/k3s-coordinate --check-service-start` under
Service to block restarts if recorded bootstrap data has disappeared.
Use the distro's standard K3s systemd
settings for restart, mount preparation, delegation and limits. Additional
OS-owned K3s configuration must be compatible and must not override node
name, init, server, token or data directory. Disable HTTP request or response
body logging and avoid verbose service tracing. The runtime config directory
must never be a Nix store path or be collected by configuration management.

Local operations use an exclusive intent lock with a five-second wait.
Anchor observation, token issuance and Kubernetes deletion use the cluster
API without taking that lock. Remote commands have a 12-second timeout
inside a host deadline of 50 seconds for API operations and 55 seconds for
local operations, followed by up to five seconds for forced termination.
The provider budgets up to 20 seconds for SSH setup and 65 seconds
for the remote operation. A worker stop waits for systemd to report the service inactive or
failed before the provider attempts node deletion. A timed-out stop keeps the
retirement marker and can be retried.

## Recovery and rotation

Snapshot embedded etcd using K3s tooling and store the corresponding server
token alongside each snapshot in encrypted, access-controlled backup
storage. Keep the previous token for snapshots created before a rotation.
Restore the cluster through the K3s recovery procedure before applying node
changes. Never reset etcd or remove local data as an automatic retry. If an initial
bootstrap failed before creating its datastore, check the service and its
persistent data. Only after confirming that it never initialized a cluster
may an operator clear the unsealed startup marker and retry. A snapshot can
predate a node's enrollment. If a recorded node UID is absent after restore,
fence the stale member and reconcile its Terraform binding through an
operator-led replacement.

Agents receive short-lived bootstrap tokens. The installed K3s version must
support restarting an already-joined agent using its existing node
credentials after the bootstrap token expires. If a node has lost those
credentials, fence it and replace it with a freshly managed host and name.

Rotate server tokens during an explicitly scheduled maintenance operation.
Use the K3s server token workflow and securely replace each joined server's
root-only `/var/lib/k3s-coordinate/join-token` atomically before restarting
it as directed by K3s. Keep quorum, verify all nodes and obtain a fresh
snapshot and matching token after completion. The provider has no automatic
token-rotation resource.
