---
name: manual-qa
description: Use when checking herdr-plus behavior through a real Herdr session and reporting seven QA sections.
---

# Manual QA

Read [the rubric](../../../REVIEW.md), `README.md`, `CREW-LABELS.md`, and `PROJECT-CHECKOUT-RECOVERY.md`.
Record the full head and base SHAs, OS, Go version, Herdr version, candidate binary, and plugin source revision.
Use synthetic configuration and a disposable Git repository. Use shell commands that cannot start provider agents or send messages.
Account for every reachable startup command and Quick Action before running it.
Never send to a real person or external target unless the task brief lists that target.

Build with `make build`. Use the candidate in a real Herdr session.
An installed link change needs the operator's word or a recorded designation.
`make plugin-link` changes the installed link. Do not run it as a read-only check.
If setup cannot run safely, report `blocked` with the missing environment or authority.
Source reads and fake sockets cannot produce a native QA pass.

## Entry points

`main.go` and `herdr-plugin.toml` define commands and native actions.
Use the claimed entry point, with these sources as the procedure:

| Behavior | Entry point and observation |
| --- | --- |
| Plugin registration | Check the candidate link, action list, `ping`, and plugin log in Herdr. |
| Projects | Invoke Projects from a known pane. Select a synthetic project. Check cwd, layout, and native IDs. |
| Worktree | Use Projects Ctrl+G, the Worktree action, or `ensure-worktree` with explicit fixture paths. |
| Shared plan | Use `plan-worktree`, then `apply-worktree` with its candidate and fingerprint. Try a stale selection. |
| Quick Actions | Run harmless command, select, and form fixtures. Check the invoking cwd and quoted values. |
| Events | Create or reopen the fixture through Herdr. Rename only its workspace. Check IDs and derived labels. |

Keep the primary workspace open until native worktree creation finishes.
Never retry a native mutation with an unknown outcome. Read native and Git state first.
Record before and after IDs, branch tips, paths, logs, and cleanup of owned workspaces and files.
Check reopens preserve existing layouts and commands. Do not change another session's panes.
Windows needs native PowerShell and named-pipe evidence for claims about that runtime.
A Unix socket fake or cross-build cannot supply it.

## PASS gate

- Define the expected result before testing. PASS needs an observed result through the claimed entry point.
- For a bug, reproduce the source-supported failure at base. Replay the same case at head.
- For a feature, compare the run with the predefined result.
- For stochastic behavior, require two consecutive successful replays.
- A dependency that fails every attempt makes the case blocked.
- An analog does not prove the exact reported failure. State its limit and grade fix confidence separately.
- A missing production condition reduces merge safety only when it could hide a regression.

## Seven report sections

1. **Reproduce the bad behavior.** Give base, input, entry point, expected result, and observed failure.
   Use `already-fixed` if base fixes it. State any reproduction gap or analog.
2. **Check the fix.** Check the candidate is running at head. Replay the complete operation with the same input.
   Retain the native result, log, IDs, and Git state that show success.
3. **Try variations.** Drive at least three distinct cases. Include another instance of the failure class.
   Cover cancellation, missing context, stale identity, branch reuse, and quoted or Unicode input when relevant.
   For prompt rules or text heuristics, test a required trigger and a near miss.
4. **Check adjacent behavior.** Exercise a nearby entry path through the shared code.
   Check existing work, duplicate events, rename retry, and placement limits when relevant. Name unreachable error paths.
5. **Check unit tests.** Use the check table in [create-pr](../create-pr/SKILL.md).
   Give commands, test files, counts, results, and fake boundaries. Zero tests provides no test evidence.
6. **Check evidence links and identity.** Open retained references. Check environment, candidate SHA, and workspace identity independently.
   Redact private paths and user data. Use `ticket-needs-revision` for a wrong source link.
7. **Give the verdict.** State the result and both confidence grades. Name remaining human checks.
   Medium fix confidence needs a signal, location, expected direction, and observation period after installation.

## Confidence grades

| Grade | Merge safety (`Confidence`) | Proof of fix (`Fix-confidence`) |
| --- | --- | --- |
| very-high | All required cases ran. No regression gap remains. | Exact base failure passes at head, or feature meets its expected result. |
| high | Caveats cannot hide a regression. | Reported inputs pass, or the same analog fails at base and passes at head. |
| medium | A stated gap could hide a regression. | After-only analog, uncertain model choice, or missing production scale or timing. |
| low | The result is not a pass. | Evidence does not show success. |

Start both grades at very-high on a pass. Explain each reduction on its own axis.
Use low on both axes for a non-pass result. An unchanged deferred issue reduces neither grade unless the PR claims it.

## Publication format

Write the report to the caller's path. Publish a PR comment only when the task authorizes publication.
After a blocked setup, publish only a short status that lists the sections that never ran.
Keep the PR body's claim evidence current. Read the saved comment after publication.
Run `python3 ~/.agents/bin/ste-lint.py <report>` when available. Fix every hard prose finding.

```text
## Manual QA: <verdict> | Merge safety: <grade> | Fix confidence: <grade>
Merge call: <what the run proves at this head>
Human check: <missing check and reason, or none>
Monitor after install: <signal, location, direction, period, or none>
Why merge safety: <reason>
Why fix confidence: <reason>
Head tested: <40-char SHA>
Base tested: <40-char SHA>
Environment: <OS, Herdr version, plugin revision, fixture identity>
Run record: <cases, results, evidence labels, references, and cleanup>
1. Reproduce the bad behavior: <base result>
2. Check the fix: <head result>
3. Try variations: <three cases and results>
4. Check adjacent behavior: <result and gaps>
5. Check unit tests: <commands, files, counts, results>
6. Check evidence links and identity: <results>
7. Give the verdict:
Verdict: pass|fail-with-fix-needed|already-fixed|ticket-needs-revision|blocked
Confidence: very-high|high|medium|low
Fix-confidence: very-high|high|medium|low
```

Choose one value for each field. A new head needs fresh evidence for affected cases.
