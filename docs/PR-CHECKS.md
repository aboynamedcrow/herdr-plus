# PR checks: herdr-plus

Read the shared steps first: `~/.agents/shared/docs/PR-WORKFLOW.md`.
Each section below names its engine section. It lists only this repository's checks.
`REVIEW.md` holds the review priorities. `.github/pull_request_template.md` holds the PR body sections.

## Create a PR

Engine section: "Create a PR".

- Follow `REVIEW.md` for public content. Use public GitHub issue links and IDs in place of private issue references.
- Search Projects, Quick Actions, headless commands, and event handlers when the diff affects them.
- Use the check recipes below. `Makefile` and `.github/workflows/test.yml` define the build and test commands.
- Run `~/.agents/bin/skill-lint` for changed skills and their pointed docs. Require exit 0.
- No repository Markdown lint configuration exists.
- Go tests fake native services. Use "Manual QA" below for native claims.
- Report missing external model-label tools to the task root.

| Change | Local checks | Source |
| --- | --- | --- |
| Go behavior | `make build`, `make vet`, `go test ./...`, `make test` | `Makefile`, `.github/workflows/test.yml` |
| Fast iteration | `make test-short`. State the skipped slow cases | `Makefile` |
| Module files | `go mod tidy -diff` | `.github/workflows/test.yml` requires tidy module files |
| Worktree API | `go test -run 'TestEnsureWorktree' ./...` | `README.md`, `ensureworktree_test.go` |
| Manifest or build | `go test -run 'TestManifest|TestBuild' ./...` | `manifest_test.go`, `buildscript_test.go` |
| Site | `make site` | `Makefile`, `.github/workflows/site.yml` |

## Review a PR

Engine section: "Review a PR".

- Use the operator's external prompt generator, review watcher, and archive command.
  Do not substitute a manual PR comment for a lane archive.

## Fix review findings

Engine section: "Fix review findings".

- Use root `REVIEW.md` for severity and convergence, subject to the engine rules.

## Manual QA

Engine section: "Manual QA".

- Read `README.md`, `CREW-LABELS.md`, and `PROJECT-CHECKOUT-RECOVERY.md` for the affected behavior.
- Record OS, Go version, Herdr version, candidate binary, and plugin source revision.
- Use synthetic configuration and a disposable Git repository. Use commands that launch no provider agents and send no messages.
- Inspect every reachable startup command and Quick Action before running it.
- Build with `make build`. Use the candidate in a real Herdr session.
- An installed link change needs the operator's word or a recorded designation.
  `make plugin-link` changes that link. It is not a read-only check. Source: `Makefile`.
- Use `main.go` and `herdr-plugin.toml` to select the claimed command or native action.
- Keep the primary workspace open until native worktree creation finishes.
- Read native and Git state before retrying a native mutation with an unknown outcome.
- Record before and after IDs, branch tips, paths, logs, and cleanup of owned workspaces and files.
- Check that reopens preserve existing layouts and commands. Do not change another session's panes.
- Windows claims need real PowerShell and named-pipe evidence. Unix socket fakes and cross-builds do not supply that proof.
- Include cancellation, missing context, stale identity, branch reuse, quoted input, and Unicode input when relevant.
- Check existing work, duplicate events, rename retry, and placement limits when relevant.
- Keep private paths and user data out of shared evidence, as `REVIEW.md` requires.

| Behavior | Entry point and observation |
| --- | --- |
| Plugin registration | Check the candidate link, action list, `ping`, and plugin log in Herdr |
| Projects | Invoke from a known pane. Select a synthetic project. Check cwd, layout, and native IDs |
| Worktree | Drive Projects Ctrl+G, the Worktree action, or `ensure-worktree` with explicit fixture paths |
| Shared plan | Run `plan-worktree`, then `apply-worktree` with its candidate and fingerprint. Include a stale selection |
| Quick Actions | Run harmless command, select, and form fixtures. Check invoking cwd and quoted values |
| Events | Create or reopen the fixture through Herdr. Rename only its workspace. Check IDs and derived labels |

Native acceptance for these entry points remains `untested` by the fake-service checks.
`ensureworktree_test.go`, `manifest_test.go`, and `buildscript_test.go` cover their stated local boundaries.

## Merge a PR

Engine section: "Merge a PR".

- Check the actual standing designation before using it.
  Require its four conditions: clean final lanes, passing CI, current head or rerun CI, and passing required QA.
  Its exclusions cover production deploys, billing changes, and deletes.
- Apply only a recorded lane exemption that meets `REVIEW.md`.
- Read live workflow states with the command below. YAML triggers alone do not prove enabled delivery.
- Read `.github/workflows/release.yml`, `.goreleaser.yml`, and `.github/workflows/site.yml` for effects.
  Record disabled Release or Site delivery in Rollout.
- Use `--merge` in the engine's guarded merge command to preserve consumer-pinned commit identity.
  Never substitute squash or rebase while a consumer pins the branch head.
  Sources: [PR 9](https://github.com/aboynamedcrow/herdr-plus/pull/9) and [PR 10](https://github.com/aboynamedcrow/herdr-plus/pull/10).

```sh
gh api repos/aboynamedcrow/herdr-plus/actions/workflows --jq '.workflows[] | {name,path,state}'
```

- Use `release.yml` for eligible main pushes, version updates, and release tags.
  Check GoReleaser archives, formula, and its upstream Homebrew destination before an authorized release.
  Do not enable or dispatch Release as part of merging.
- Use `site.yml` for matching Pages changes under separate deploy authority.
  Report disabled workflow delivery without claiming a tag or site update. Live delivery remains `untested` by source checks.
- Update the consumer plugin source lock in a separate consumer change.
  Pin an exact accepted commit. Require a clean source checkout at that commit.
  Check the candidate on one approved machine before advancing other consumers.
- Follow the consumer's restore procedure. Do not replace active workers to refresh metadata.
  Require native evidence for installed candidates. Installed metadata does not prove that running processes changed.
- Rollback uses a reviewed revert and the prior accepted consumer pin.
  Preserve local work before restoration. A revert cannot undo prior external Quick Action effects.
- Report workflow results, the consumer pin, and remaining acceptance checks.
