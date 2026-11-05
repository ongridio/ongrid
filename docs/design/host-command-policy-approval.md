# Host command policy approval

The assistant setting `agent.command_policy_approval_enabled` defaults to
`false`. Together with the existing write-action permission, it lets
`host_bash` submit Edge command-policy denials to the existing human approval
flow. The setting uses the existing administrator-only settings API; it adds
no migration or Edge protocol requirement.

Ordinary calls still send `Unrestricted=false`. Only a successful Edge reply
with `Allowed=false` and a rejection reason is eligible: that response
guarantees the command did not start. Transport errors, timeouts and nonzero
process exits do not qualify. Batch approvals include only rejected hosts;
other hosts' results remain in the final batch and are never rerun.

The approval stores the exact command, device IDs, timeout and policy reasons.
The existing confirmation card shows the reasons and explains that approval
bypasses command policy. Approval runs the frozen payload through
`RunApproved`; rejection, cancellation or expiry does not execute it.
The final result retains approval evidence and identifies the approved subset.

The setting is injected into every active runtime's host tool and the tool
registry. The existing per-call write permission still excludes viewers and
read-only callers. A missing setting, read error or unwired proposer keeps the
existing denial behavior. The new setting does not change approvals for
commands already classified as writes. The setting is read directly from the
shared store for each check, so later calls observe updates across Manager
instances without relying on process-local cache invalidation.

Approval bypasses binary, argument, path and network policy checks. The
command still runs with the Edge process's OS permissions and existing time
and output limits. This does not override systemd, container or filesystem
permissions.

Disabling the setting prevents new fallback approvals. Already-pending
approvals retain the existing approval lifecycle and must be rejected
separately if they should not run. To roll back, disable the setting and
restore the prior Manager and web build; existing Edge agents need no change.
