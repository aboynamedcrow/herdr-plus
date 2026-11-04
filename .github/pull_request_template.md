## Summary

<!-- State the changed behavior in one or two sentences. Add the issue link here.
For an unfinished part, write `Part of <issue-id>`. Keep its ID out of the branch name. -->

## Changes

<!-- Cover the whole branch diff. Group changes by file or area. -->

## Other entry points

<!-- Search callers, copies, and related actions. Record `Searched: <command> (<n> hits)`.
Give each hit its path and outcome: changed, unchanged with reason, or deferred with a link.
For docs or tooling, write `None: docs or tooling only.` Still search for copies.
For example, trace Projects Ctrl+G and the Worktree action through the shared planner. -->

## Rollout

<!-- State what runs after merge, on which machine or service, and how to undo it.
Check whether Release and Site are enabled. A merge does not change a consumer's plugin pin.
For plugin changes, state the accepted commit, consumer pin update, native QA, and rollback pin.
For docs, state whether anything beyond the merge must run. -->

## Review record

<!-- Give R1 and R2 rounds, fingerprints, verdicts, and report links.
If a rule skips lanes, cite the rule and show that this diff meets its limits. -->

<!-- Add these sections only when needed. Keep Testing last.

## Security

State input trust, shell quoting, data access, and external targets. Include no secret values.

## Migration

State compatibility, manifest changes, consumer order, and rollback limits.

## Retries and races

State guards, worst duration, cancellation, and cleanup.
Cover stale worktree plans, native mutations with unknown outcomes, and concurrent workspace changes.
-->

## Testing

- [ ] Relevant Go tests pass locally
- [ ] Build, vet, and module checks pass
- [ ] Native Herdr QA completed for changed behavior
- [ ] Not verifiable locally (state why)

**Inputs tried:**

<!-- List each input and result. Include cancellation, stale plans, and existing work when relevant. -->

**Evidence for each claim:**

<!-- Give one line per claim: `"<claim>": <command or retained result> at <SHA and environment>`.
Use an evidence label: mocked check, source inspection, native smoke, manual reconciliation, or unattended CLI.
For missing proof, write `Not shown yet: <claim> (<check needed>)`.
A socket fake does not prove native manifest acceptance or installed picker behavior. -->
