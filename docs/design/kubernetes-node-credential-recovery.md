# Kubernetes node credential recovery

Issue: [#505](https://github.com/ongridio/ongrid/issues/505)

## Failure

Inventory removes a Node's managed Edge when Kubernetes replaces the Node UID.
The replacement Pod can retain the host-mounted credential file. Older files do
not contain a Node UID. Loading that file without checking the Manager binding
causes the Agent to retry tunnel authentication with a deleted identity.

## Decision

At startup, a node Agent reads the current Node UID from the Kubernetes API.
When a cached credential and node bootstrap token are available, it calls
`POST /internal/k8s/reconcile-node`. The Manager validates the bootstrap token,
physical cluster UID, node name, and current node record under the enrollment
and inventory locks.

- If that node still refers to the cached Edge ID, return `reuse_credentials`.
  Do not disclose or rotate a secret. The tunnel still authenticates the cached
  key. This also keeps a manually revoked credential revoked.
- If the current node has no registered device, use the existing enrollment
  path. Create a new Edge for an unbound node, or retry its pending enrollment
  after a failed credential write.
- If another Edge has already registered the node, reject recovery. Do not
  replace its identity or device binding.

The Agent writes the UID and recovered credential with the existing atomic,
mode-0600 file replacement. A healthy legacy credential gains its UID without
changing keys. A healthy file that already has the UID is left unchanged.
If only this UID update fails, log the failure and keep the confirmed credential
running. A replacement credential must still be persisted before it can be used.
Kubernetes Secret storage can also read the legacy key based on node name.

The separate route is required for rolling upgrades: an older Manager must not
interpret a reconciliation request as a regular enrollment and rotate a pending
credential. On an unavailable or older Manager, an Agent with an unchanged or
unknown cached UID retains its credential and registration metadata. A known
UID mismatch fails startup until recovery succeeds. Errors do not delete the
cached credential.

## Deployment and rollback

Upgrade the Manager and the Nginx configuration before restarting upgraded node
Agents. Both bundled Nginx configurations expose this route with the existing
bootstrap enrollment rate limit. The shipped v0.18.0 chart already grants the
node service account `get` access to Nodes; no new RBAC permission is required.

Recovery starts at Agent startup. If the Manager is unavailable, rate-limited,
or has not inventoried the Node yet, the Agent keeps running with its cached
identity and retries reconciliation in the background. Requests have timeouts;
the retry delay doubles from one second to a one-minute cap, with jitter, and
cancellation stops the loop. Authentication and binding rejections are not
retried. A missing or invalid bootstrap token cannot issue new credentials.

The retry uses a private config copy. If the Manager confirms the cached
credential, the retry stops without restarting the Agent. If it issues a
replacement, the Agent first persists that credential, then exits so Kubernetes
restarts it with the new identity. This also works when the old tunnel connected
before inventory deleted its Edge. The tunnel's reconnect loop is unchanged.

Credential recovery does not change the database schema. Reverting the code leaves newly issued Edge
credentials and the credential file format readable by older Agents. An older
Agent still needs a valid cached credential; rollback does not restore a deleted
Edge identity.

### Legacy Host fingerprint upgrade

PR #521 (`a90d4b19`, 2026-10-16) added Node UID discovery but did not migrate
the Host fingerprint of an existing name-based node. PR #528 (`9b0999f0`,
2026-10-19) made the existing Edge-to-Host binding immutable, so the mismatch
became a registration failure. v0.19.0 is the first release containing both.

Before the generic Host lookup, Manager validates the authenticated node against
its enrolled cluster, name and UID. For a current, mutually linked installation,
it changes only the original Host's name-based fingerprint to the UID-based
fingerprint in one transaction. Host ID, topology, metadata and plugin settings
stay attached to the original row. Repeated registration is a no-op.

A failed v0.19.0 registration can already have created a duplicate Host. The
transaction may soft-delete that row only when it has no current installation,
historical Edge or node references, plugin settings or user edits. Manager
startup can have backfilled its topology node. That node is removed in the same
transaction only if it still has the generated type/name, empty properties,
unchanged timestamps, and no current or historical references from relations,
other Hosts or clusters. Edited or used topology is retained.
Reference checks use locking reads so MySQL's repeatable-read snapshots cannot
hide a relation committed before the topology lock was acquired.
An occupied fingerprint or inconsistent binding fails closed; the migration does
not move an Edge to another Host. A database failure rolls back all changes.

Upgrade Manager before restarting the node Agent. This is an on-registration
data migration, without a schema change. After it succeeds, downgrading to an
Agent that reports only a node name requires a coordinated identity rollback;
do not downgrade the binary alone. Preserve the pre-upgrade database backup.

## Verification

The Edge tests cover legacy and current files, changed and unchanged Node UIDs,
Manager failure, an older Manager, missing or invalid bootstrap tokens, and a
failed atomic write. Manager tests cover reuse, deleted-node recovery, retry of
a pending enrollment, registered-node protection, and revocation.
The retry tests cover repeated temporary failures, healthy reuse, cancellation,
and a failed UID-only write that must not prevent a healthy node from starting.

`TestKubernetesNodeCredentialRecoveryChain` uses the real SQLite stores,
Manager HTTP handler, Edge enrollment code, credential authenticator, and node
registration code. It deletes the old Edge through inventory, verifies the old
credential is unauthorized, recovers and registers the new identity, restarts
the Agent, and checks that a neighboring healthy node keeps its Edge, secret
hash, and device binding. The Kubernetes API is a local TLS test server.
The same chain runs with an initial Manager 503 and with inventory arriving
after Agent startup, including the restart request and loading the new file.
Its healthy neighboring node starts with the Agent's actual name-based Host
fingerprint and acquires its UID while preserving its Host ID. Store tests cover
an earlier failed-registration orphan, repeated migration, conflicting bindings,
an occupied fingerprint, preserved user settings and transaction rollback.
The restart scenario first invokes the old registration order to create the
orphan, reruns the production device/Edge/Kubernetes/topology migrations, then
registers the node through the fixed path. It verifies that the original Host
survives and both the duplicate Host and its generated topology are removed.
A MySQL regression commits a topology relation while migration is in progress
and verifies that the referenced Host and node are retained. CI runs it through
the existing `ONGRID_TEST_MYSQL_DSN` test database configuration.
