# Validation and operations

## Acceptance tests

1. Reboot and recover through remote KVM.
2. Connect through Tailscale using SSH and NoMachine from two named users.
3. Confirm both users reach the same `agt-*` desktop without receiving its password.
4. Confirm `agentctl shell`, attach, detach, status, and authorization logging.
5. Confirm the agent account has no `sudo`/Docker-group membership and that effective sshd policy denies its direct login.
6. Verify `git`, `gh`, `glab`, VS Code, Ghostty, Herdr, tmux, runtimes, containers, and browsers.
7. Create a disposable branch and draft PR/MR using the non-human source-control identity.
8. Run headless and headed Playwright tests while observing the desktop.
9. Run four representative agent sessions with realistic subagents, builds, browsers, and containers.
10. Confirm the desktop and SSH remain usable during load and that alerts occur before failure.
11. Fill disk and memory only with controlled test fixtures; verify cleanup/pressure behaviour without risking user data.
12. Back up a test project and restore it to a separate path.

Run the automated evidence check from a privileged named-admin session after the user-space install:

```text
sudo ./scripts/validate-host.sh agt-ai-01
```

If NoMachine uses a reviewed non-default port, pass it as the second argument.

The host script checks machine controls and tooling. `fleetctl ... audit` first compares the same host to its approved account/profile declarations, then invokes that host script. Neither prints credentials. A pass remains necessary but insufficient: NoMachine authorization, GUI locking, Tailscale grants, KVM/LUKS recovery, provider billing, branch protection, headed tests, and restore evidence remain human acceptance tests.

## Burn-in

Run a 24-hour pilot followed by a multi-day workload representative of production. Capture CPU saturation, memory pressure, swap, browser count, process count, disk growth, temperature, throttling, network stability, agent failures, and UI responsiveness.

Use the results to select 64 GB versus 128 GB for additional nodes and to set `MemoryHigh`, emergency ceilings, concurrency guidance, and alert thresholds.

## Maintenance rhythm

- Daily: capacity and failed-service checks.
- Weekly: security updates, disk/cache review, backup success, and agent/CLI update review.
- Monthly: restore test, credential expiry review, access review, firmware check, and resource trend review.
- Quarterly: named-user/service-identity recertification, KVM recovery exercise, branch-policy audit, and clean-machine provisioning test.

Updates to agent CLIs and preview applications should be staged on one node before fleet rollout.

Record `MemoryHigh`, `MemoryMax`, `TasksMax`, peak tasks, memory pressure, and responsiveness. Promote measured values through the [fleet change process](12-fleet-rollout-and-change-management.md); do not silently rewrite the formula on running nodes.
