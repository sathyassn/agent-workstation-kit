# Security policy

## Supported versions

Only the latest tagged version and the current default branch receive security
fixes. Version 0.x is a pilot series and must be validated against the target
operating system before use.

## Reporting a vulnerability

Use GitHub's private
[Report a vulnerability](https://github.com/sathyassn/agent-workstation-kit/security/advisories/new)
channel. Do not include exploit details, credentials, hostnames, or other
sensitive data in a public issue. If private reporting is unavailable, do not
publish the report; contact the repository owner privately and ask for the
current security channel.

Include the affected version, operating system, reproduction conditions,
impact, and a minimal safe proof of concept. Maintainers should acknowledge the
report privately, agree on a disclosure plan, and publish a security advisory
when a fix is available.

## Deployment boundary

These scripts do not make a machine secure by themselves. Disk encryption,
endpoint controls, identity policy, network grants, vendor authentication,
backup, recovery, and live-machine verification remain deployment obligations.
