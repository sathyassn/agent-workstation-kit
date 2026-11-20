"""The shared scope order and fixed executable allowlist for the setup workflow."""
SCOPES = {
    "linux": {"packages": ["base"], "machine": ["identity", "accounts", "remote-hardening", "agentctl", "workloads"], "agent": ["shell", "user-tooling"], "resources": ["resources"], "audit": ["audit"]},
    "macos": {"packages": ["base"], "machine": ["identity", "accounts"], "agent": ["shell", "user-tooling", "workloads"], "resources": ["resources"], "audit": ["audit"]},
}
SCRIPTS = {"base": {"bootstrap-linux.sh", "bootstrap-macos.sh"}, "identity": {"install-machine-identity.py"}, "accounts": {"setup-accounts-linux.sh", "setup-accounts-macos.sh"}, "remote-hardening": {"harden-remote-access-linux.sh"}, "agentctl": {"install-agentctl-linux.sh"}, "shell": {"install-shell-baseline.sh"}, "user-tooling": {"install-user-tooling.sh"}, "workloads": {"install-workloads-linux.sh", "check-project-workloads.sh"}, "resources": {"apply-resource-policy-linux.sh"}, "audit": {"validate-host.sh"}}


def expected_owner(name, platform):
    if name in {"shell", "user-tooling"} or name == "workloads" and platform == "macos":
        return "agent"
    if name == "base" and platform == "macos":
        return "package-owner"
    return "root"
