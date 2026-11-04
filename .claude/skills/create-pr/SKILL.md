---
name: create-pr
description: Use when creating or updating a herdr-plus PR with checks and behavior evidence.
---

# Create a PR

1. Read repository instructions when present.
   Read [the template](../../../.github/pull_request_template.md) and [the rubric](../../../REVIEW.md).
2. Compare the whole branch with `origin/main`. Describe every commit in the template's sections.
   Keep Testing last. Remove guidance comments before publication.
3. Use a plain title that states the changed behavior first. Keep it near 80 characters or fewer.
   Start development tool changes with `CI:` or `Dev:`. Omit conventional commit prefixes and issue suffixes.
4. Link the issue in Summary. For unfinished parts, write `Part of <issue-id>` in the body.
   Keep that ID out of the branch name until the final PR.
   A merged branch containing the issue ID can cause the issue tracker to mark the issue Done.
5. Search sibling paths and copies. Record the command, hit count, and each hit's outcome under Other entry points.
   Include Projects, Quick Actions, headless commands, and event handlers when the diff affects them.
6. Match each claim to evidence at the current head. Use `Not shown yet:` for missing proof.
7. Select checks from the table. Follow the test rules in `REVIEW.md`.

   | Change | Local checks | Source |
   | --- | --- | --- |
   | Go behavior | `make build`, `make vet`, `go test ./...`, `make test` | `Makefile`, `.github/workflows/test.yml` |
   | Fast iteration | `make test-short` | `Makefile`. This skips slow cases. |
   | Module files | `go mod tidy -diff` | CI requires tidy `go.mod` and `go.sum`. |
   | Worktree API | `go test -run 'TestEnsureWorktree' ./...` | `README.md`, `ensureworktree_test.go` |
   | Manifest or build | `go test -run 'TestManifest|TestBuild' ./...` | `manifest_test.go`, `buildscript_test.go` |
   | Markdown or skills | `git diff --check`. Run the prose gate below. | No repository Markdown lint config exists. |
   | Site | `make site` | `Makefile`, `.github/workflows/site.yml` |

   Record exact commands, results, and limits. Use evidence labels from `REVIEW.md`.
   Go tests use fake native services. Use [manual-qa](../manual-qa/SKILL.md) for native claims.
8. Before each push, fetch `origin/main` and `origin/<branch>` separately.
   A first push can lack a remote branch. Check that absence before continuing.
   Inspect `git log HEAD..origin/<branch>` when the remote exists. Rebase first when it contains commits you lack.
   Preserve concurrent work. Never force a push over unknown commits.
9. Inspect the actual title and body. When available, run `python3 ~/.agents/bin/ste-lint.py <body-file>`.
   Fix every hard prose finding. Preserve literal commands and paths.
10. Commit. Push the branch. Use a draft while work remains incomplete.
    Create with `gh pr create --repo aboynamedcrow/herdr-plus --base main --title "<title>" --body-file <body-file>`.
11. Read the saved record after each create or edit:
    `gh pr view <N> --repo aboynamedcrow/herdr-plus --json title,body,headRefOid`.
12. Run `harkness label model --pr <N> --repo aboynamedcrow/herdr-plus` after opening the PR.
    This command belongs to the operator's review tools. Report missing tools to the task root.
13. Use [review-pr](../review-pr/SKILL.md) for review and [iterate-fixups](../iterate-fixups/SKILL.md) for fixes.

`REVIEW.md` holds the repository's process rules and evidence limits.
The template holds the required body shape. The Makefile and workflow files define the check commands.
