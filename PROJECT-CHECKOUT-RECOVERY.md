# Project checkout recovery

This update replaces the legacy primary-checkout refusal described in the README.

A project is a launch recipe. A workspace can contain panes in several checkouts.
Its native checkout record defines its association for worktree operations and grouping.
That record does not restrict the directories of its panes.

With `reuse_checkout = true`, selecting a primary project can register an existing workspace.
Plus uses the exact checkout and workspace ID from Herdr's worktree registry.
It refuses conflicting records or changed ownership.
Herdr must report the same path, the same workspace, and `already_open`.
Plus then checks the recorded repository identity before it continues.

Registration does not rebuild the workspace or run its startup commands.
Tabs, panes, and running agents remain in place.
The shared worktree picker uses the same recovery before creating a selected worktree.
It still checks the accepted Git branch, path, and base before creation.

An existing linked checkout without a recorded identity still requires native registration.
Plus does not infer repository relationships from workspace names.

## Workspace rename behavior

The `workspace.renamed` event updates `crew_task` and labels derived from its old value.
The handler uses the event's workspace ID. It reads the current label under a per-workspace lock.
This covers native keyboard, menu, and API renames.

The handler preserves custom tab labels and pane labels for other tasks.
It changes no pane ID, process, account, or agent name.
A missing or empty task causes no changes.
A failed native call stops the handler. Rename the workspace again to retry.

Native label reads and writes are separate calls.
The handler rechecks each label before writing. Herdr has no conditional rename API.
A simultaneous manual rename in the final read/write interval cannot be protected atomically.
