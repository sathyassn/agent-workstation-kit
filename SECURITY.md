# Security policy

## Supported versions

Only the latest tagged version and the current default branch receive security
fixes. Version 0.x is a pilot series and must be validated against the target
operating system before use.

## Reporting a vulnerability

This repository must remain private until GitHub private vulnerability
reporting is enabled and tested, or a monitored private security address is
published here. Do not include exploit details, credentials, hostnames, or
other sensitive data in a public issue.

Once public, use the repository's **Report a vulnerability** action. If that
channel is ever disabled or unavailable, do not publish a report; contact the
repository owner privately and ask for the current security channel.

Include the affected version, operating system, reproduction conditions,
impact, and a minimal safe proof of concept. Maintainers should acknowledge the
report privately, agree on a disclosure plan, and publish a security advisory
when a fix is available.

## Deployment boundary

These scripts do not make a machine secure by themselves. Disk encryption,
endpoint controls, identity policy, network grants, vendor authentication,
backup, recovery, and live-machine verification remain deployment obligations.
