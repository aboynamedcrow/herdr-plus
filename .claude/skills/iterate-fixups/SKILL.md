---
name: iterate-fixups
description: Use when resolving herdr-plus PR findings or CI failures within three review rounds.
---

# Iterate PR fixes

1. Read the current head, lane reports, inline comments, and check logs.
   Record the full head SHA, fingerprint, and review round.
2. Classify each failure: PR defect, existing base failure, stale check, infrastructure failure, or demonstrated flake.
   Use logs at the matching revision. A rerun alone does not prove a flake.
3. Fix each S1 or S2 finding. Answer a disputed finding with code or test evidence.
   Keep S3 findings as follow-ups. They never become merge conditions.
   Stop a repeated disagreement that produces no new evidence. Give the task root both positions.
4. Run checks for the changed paths from [create-pr](../create-pr/SKILL.md).
   Refresh the PR evidence and rollout notes. Read the saved title and body after each edit.
5. Commit the fixes. Follow create-pr's fetch and rebase rules before each push.
   Preserve work from other writers before continuing.
6. Record a fresh base SHA, head SHA, and diff hash after the push.
   Both older verdicts are stale. Restart both lanes at that fingerprint in the same seats.
   Follow [review-pr](../review-pr/SKILL.md). Refresh [manual-qa](../manual-qa/SKILL.md) for affected behavior.
7. Watch CI with a 30-minute command timeout:

   ```sh
   gh pr checks <N> --repo aboynamedcrow/herdr-plus --watch --fail-fast
   ```

   Use the runner's timeout facility. Read the yielded output until the command exits.
   At timeout, report the pending checks and last result. A disabled declared check requires explicit resolution.
8. Read the head again after checks. If another writer changed it, resolve ownership before another edit.
9. End when both lanes are clean and required checks pass at the final head.
   After three review rounds without two clean lanes, stop. Give the operator open findings and their labels.
   Propose a fix, a waiver with a recorded designation, or a split. Do not start round four.

Root `REVIEW.md` sets severity, convergence, and the three-round cap.
The operator's archive command publishes lane reports. Reviewers do not edit PR comments themselves.
A fix loop grants no merge authority. Use [merge-pr](../merge-pr/SKILL.md) after the gates pass.
