# Create a draft PR or MR as the agent identity

[Documentation home](../README.md) · [Provider identities](../06-agent-and-source-control-identities.md) · [Validation](../08-validation-and-operations.md)

Use a disposable repository or approved branch for the first test. Never target a protected branch directly.

## Preconditions

- The agent source-control identity has only required repository access.
- Default/release branches are protected.
- Human review and required checks are enabled.
- `gh` and `glab` are installed.
- The credential is injected by an approved helper or secrets manager and will not appear in history or process arguments.
- Git author name/email identify the functional agent workspace.

## GitLab

1. Run `glab auth status --hostname HOST` and inspect sanitized output.
2. Create a branch named according to project policy.
3. Commit and push the branch.
4. Run `glab mr create --draft --fill` and add the initiating human/session to the description.
5. Confirm the service account cannot approve or merge the MR and cannot push to the protected target branch.

## GitHub

1. Provide a short-lived GitHub App installation token to the child process through the approved helper as `GH_TOKEN`, or use the approved machine-user login.
2. Run `gh auth status --hostname HOST` and inspect sanitized output.
3. Create a policy-compliant branch, commit, and push.
4. Run `gh pr create --draft --fill` and add the initiating human/session to the description.
5. Confirm the identity cannot approve or merge its own PR and cannot push to the protected target branch.

## Cleanup

After human review of the test, close the draft and delete only the disposable branch through the normal project workflow. Do not revoke a production credential solely as test cleanup; follow its rotation policy.
