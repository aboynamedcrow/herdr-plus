# Review priorities for herdr-plus

This file is the repository's review rubric. The review prompt must include it verbatim.
Use [PR checks](docs/PR-CHECKS.md#review-a-pr) for reports and the exact verdict line.

## Operator model

- The operator controls local configuration, project recipes, and Quick Action commands. Those files can run shell commands.
- Native events, invoking-pane context, Git state, and remote metadata can change during an operation.
  Check their identity and freshness at the point that controls the effect.
- Preserve user files, branches, running panes, and native identities. Judge changed behavior against the base.
- This repository is public. Include no private host names, account aliases, private paths, or secret values.
  Include no Linear or Slack links or IDs.

## Priorities, highest risk first

1. **Data loss and wrong worktrees.** Inspect `ensureworktree.go`, `worktreepolicy.go`, and the lifecycle helpers.
   Preserve local commits, edits, branch names, and registered paths. Check the accepted base commit and repository identity.
   Check stale fingerprints, remote-only branches, ambiguous choices, cancellation, and preparation races.
   A timed-out native mutation has an unknown outcome. Do not retry it automatically.
2. **Wrong workspace or pane.** Inspect `context.go`, `projectreuse.go`, and picker launchers.
   Use the invoking pane's verified identity and directory. Global focus can belong to another client.
   Check primary checkout provenance, explicit parent workspace, and native result IDs before accepting success.
   Reopening work must preserve layout, processes, and commands. Existing custom labels must survive a task rename.
3. **Secrets and command execution.** Inspect `action.go` and `shell.go` for each changed command path.
   Check template substitution, quoting, option output, working directories, and environment propagation.
   Explicit template values need their own quoting. Appended values use the platform quote helper.
   Check controlled inputs and outbound targets. Never include a secret value in a tracked file, log, or PR.
   Repeat the security pass across the whole diff in every round.
4. **Native plugin acceptance.** Check `herdr-plugin.toml`, `scripts/build.sh`, and the binary's subcommands in `main.go`.
   Installation must build the checked-out source. It must fail when Go or the build fails.
   Check action, pane, and event commands against the minimum Herdr version.
   Check placement rules in the real server. A parsed TOML file does not prove that Herdr accepts the manifest.
5. **Unix and Windows compatibility.** Check `.exe` paths, Windows entry points, PowerShell quoting, locks, and named-pipe dialing.
   Read platform files and test skips. A Unix socket fake does not cover Windows runtime behavior.
   A cross-build proves compilation only. Check actual platform support per action rather than assuming complete parity.
6. **Partial effects and races.** Inspect layout inventory guards, role tokens, and the Crew rename journal.
   Check repeated events, partial native failures, retry recovery, suffix retention, and custom labels.
   Read `CREW-LABELS.md` and `PROJECT-CHECKOUT-RECOVERY.md` for the documented behavior and rename race limit.
7. **Release and consumer pins.** Check workflow state as well as YAML triggers.
   Read `.goreleaser.yml` destinations before release. A merge does not update an installed consumer pin.
   Preserve accepted commits through the merge method. State rollout, rollback, and native acceptance steps.
8. **Entry paths and truthful procedures.** Search siblings every round. Trace each claim through its public entry point.
   Cover Projects, Ctrl+G, Worktree, headless commands, Quick Actions, and native events when affected.
   Check procedure claims against source. State `untested` when native evidence is absent.
   Review removed paths and existing helpers before accepting duplicate code.

## What CI covers, and what it does not

`.github/workflows/test.yml` runs on PRs and main pushes, on Linux and macOS.
It checks module tidiness, `go build ./...`, `go vet ./...`, and `go test -race ./...`.
`manifest_test.go` parses the manifest and checks Windows entries by group.
`buildscript_test.go` checks the source build contract. Native IPC tests use fake sockets and disposable Git repositories.

CI does not install the candidate in a real Herdr session. It does not prove live manifest acceptance or picker placement.
It does not run Windows native QA. It does not prove consumer pin restoration or replacement of a running plugin process.
Release and Site have separate workflows. Their enabled state must be read before claiming delivery.
No repository Markdown lint configuration exists.

## Evidence the PR must show

Only require evidence for affected areas. Give the exact command, SHA, environment, result, and boundary.

| Area | Required evidence |
| --- | --- |
| Go behavior | Relevant existing tests, `make build`, `make vet`, and race-enabled CI at the reviewed head. |
| Worktree selection | Branch tips, accepted base, path, and native identity before and after the claimed entry point. |
| Workspace or action | Native smoke in a disposable Herdr workspace, including invoking cwd, IDs, cancellation, and cleanup. |
| Manifest or shell | Source build checks, per-platform command review, and native acceptance for runtime claims. |
| Crew rename | Custom-label preservation, suffixes, stable IDs, and partial-failure recovery. |
| Release or consumer pin | Workflow state, destination, accepted commit, restore result, and rollback procedure. |
| Docs or skills | Source references for process facts. Run the prose gate when available. No new test is required. |

Label evidence: `mocked check`, `source inspection`, `native smoke`, `manual reconciliation`, or `unattended CLI`.
A static file does not prove live behavior. Mark missing proof `Not shown yet:` with the check needed.
Never send to a real person or external target during QA unless the task brief lists that target.

## Tests

- Add tests for data loss, secret exposure, wrong external targets, money, wrong merges or installs, and failures that block all starts.
- A defect that appears at once in a pane and needs a one-line fix does not need a new test.
- Docs and refactors add no tests. Existing behavior tests supply refactor evidence.
- Test through the public command or function. Assert results and external targets. Keep fixtures isolated from live user state.
- Inject clocks and delays. Do not add wall-clock assertions or real waits to tests.
- Do not add tests that search docs, skills, or source for a phrase. Do not test a fake or an external tool itself.
- Do not compare full messages, argument lists, or subprocess traces when one behavior assertion suffices.
- A missing test for a harmful changed line is S2 when no cited evidence covers it. Name the line and harm.
- A disallowed test shape is S3. A missing test for low-risk behavior is not a finding.

## Verdict and convergence

- S1 means Critical. S2 means High. Both require a concrete changed line, realistic trigger, and complete harm mechanism.
  Label each `merge condition`. A false statement in shipped docs can meet this floor.
- S3 means Medium or Low. Label each `follow-up`. It never blocks merge.
- A lane is clean when no S1, S2, or required review gap remains. Two independent lanes must judge one exact fingerprint.
- On later rounds, check prior blockers and new commits first. Unchanged code blocks again only for S1.
  Check disputed findings against code. Record withdrawals and do not repeat them without new evidence.
- After three rounds without two clean lanes, stop. Give the operator open findings and labels.
  Propose fix, waive with a recorded designation, or split.
- An approved docs-only exemption needs a recorded rule. Skills that change agent actions are not automatically exempt.
- A pin-only change can skip lanes when every upstream PR in its range merged with two clean lanes.
  Permit only pin changes and their explanatory docs. Show upstream reports, source checks, CI, and required QA.

## What is not a finding

- Hardening against an actor who already controls the operator's trusted configuration or shell commands.
- A pre-existing defect, a speculative harm chain, or a rare input with no documented producer. Record a follow-up instead.
- A style, naming, or comment preference as a merge condition.
- A repeated check that CI already covers. Spend review effort on boundaries that CI cannot show.
- A request for a new test that merely proves a doc sentence or a refactor.

Secrets remain findings even when a scan exists. Never copy an exposed value into a report.
The PR body records evidence. A stale number is S3. Concealed defects or invented evidence are S1.
Merge authority belongs to [PR checks](docs/PR-CHECKS.md#merge-a-pr), separately from review acceptance.
