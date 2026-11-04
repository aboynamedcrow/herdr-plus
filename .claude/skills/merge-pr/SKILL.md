---
name: merge-pr
description: Use before merging a herdr-plus PR to check authority, head identity, release effects, and consumer pins.
---

# Merge a PR

## Authority

A merge needs the operator's word for that PR or a recorded designation.
The operator's standing designation permits a merge without another ask when all four conditions hold:

1. Both review lanes are clean at the final head fingerprint.
2. Every CI check passes at that head.
3. The head is current with main, or CI ran again after a rebase.
4. The QA that the change needs passed.

The designation excludes production deploys, billing changes, and deletes.
Check the actual designation before using it. This skill does not grant authority.
An approved lane exemption must meet `REVIEW.md`. Green checks alone grant no authority.

## Preflight

1. Read the PR:

   ```sh
   gh pr view <N> --repo aboynamedcrow/herdr-plus --json state,isDraft,baseRefName,headRefOid,mergeable
   gh pr checks <N> --repo aboynamedcrow/herdr-plus
   gh api repos/aboynamedcrow/herdr-plus --jq '{rebase:.allow_rebase_merge,squash:.allow_squash_merge,merge:.allow_merge_commit}'
   ```

2. Require OPEN, no draft, and base `main`. Match the full 40-character head to review, CI, and QA evidence.
   Failed or pending required checks block. Resolve a missing declared check explicitly.
   For UNKNOWN mergeability, read again after five seconds. Stop after three reads. CONFLICTING blocks.
3. Fetch `origin/main` and `origin/<branch>`. Follow create-pr's remote-work preservation rule.
   Check that the head contains main with `git merge-base --is-ancestor origin/main <head>`. Exit 1 means main moved.
4. When main moved after the last clean round, rebase and push. Record the old and new bases and heads.
   A rebase changes the target, so it invalidates both verdicts.
   Send the new fingerprint to both lanes in the same seats. Merge only after both are clean there.
   `git range-diff <old-base>..<old-head> origin/main..<new-head>` can narrow the next round. It never replaces a verdict.
   Only a recorded designation that covers this PR can keep old verdicts.
   Source: the engine's `docs/PR-WORKFLOW.md`, "Review before merge". Refresh affected QA.
5. Check the current workflow states:
   `gh api repos/aboynamedcrow/herdr-plus/actions/workflows --jq '.workflows[] | {name,path,state}'`.
   Read `.github/workflows/release.yml`, `.goreleaser.yml`, and `.github/workflows/site.yml` for effects.
   A source file does not prove its workflow is enabled. Record disabled release or site delivery in Rollout.
6. Read recent merges before choosing the method:
   `gh pr list --repo aboynamedcrow/herdr-plus --state merged --limit 10 --json number,title,mergeCommit`.
   This fork uses merge commits to retain commits that a consumer pins.
   Sources: [PR 9](https://github.com/aboynamedcrow/herdr-plus/pull/9) and [PR 10](https://github.com/aboynamedcrow/herdr-plus/pull/10).
   Use the checked merge method. Never substitute rebase or squash while a consumer pins the branch head.

## Merge

Run the ancestor check as a gate immediately before the merge. `<branch>` is the PR branch name. Replace the placeholders with checked values.
Keep the command on one line, so no runner can split it:

```sh
git fetch origin main <branch> && git merge-base --is-ancestor origin/main <head> && gh pr merge <N> --repo aboynamedcrow/herdr-plus --merge --match-head-commit <head>
```

The fetch also brings the PR head. `git merge-base --is-ancestor` exits 0 only when the head contains the current main.
It exits 1 for a stale head and 128 for a missing commit, so the merge stops in bash, sh and zsh.
Do not use a count test such as `[ "$(git rev-list --count ...)" -eq 0 ]`. zsh reads an empty count as 0.
The check must gate the merge. Never print it with `echo` before the merge.
A failed fetch stops the merge.
Read the result:
`gh pr view <N> --repo aboynamedcrow/herdr-plus --json state,mergedAt,mergeCommit`.

## After the merge

- Enabled Release runs on main pushes except site-only changes. It synchronizes the version and creates a release tag.
  GoReleaser creates archives and a Homebrew formula. `.goreleaser.yml` still targets the upstream Homebrew repository.
  Check that destination before any authorized release. Do not enable or dispatch Release as part of a merge.
- Enabled Site publishes matching website changes to Pages. A deploy needs its own authority.
  When either workflow is disabled, report the missing delivery. Do not claim that a tag or site update exists.
- A consumer plugin pin remains unchanged after this merge. Update its source lock in a separate consumer change.
  Pin an exact accepted commit. Require a clean source checkout at that commit.
  Check the candidate on one approved machine before advancing the other consumers.
  Follow the consumer's restore procedure. Do not replace active workers to refresh a version label.
- Retain native QA evidence for the installed candidate. Installed metadata alone does not prove that running processes changed.
- Roll back through a reviewed revert and the prior accepted consumer pin. Preserve local work before restoring a checkout.
  A revert does not undo external actions that Quick Actions already performed.
- Archive both review seats through the operator's archive command with their last reports.
  Report the PR URL, merged commit, UTC merge time, workflow results, consumer pin, and remaining acceptance checks.

Sources: root `REVIEW.md`, the workflow files, `.goreleaser.yml`, `herdr-plugin.toml`, and `README.md`.
Consumer restore and pin procedures belong to the consumer's configuration repository.
