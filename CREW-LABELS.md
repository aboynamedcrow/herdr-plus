# Task labels and role IDs

Use `{task}` in a tab name or pane label to include its task.
Project starters use the project name. Worktree events use the workspace label,
then the branch, then the repository name. Commands receive no substitution.

Tabs accept the optional roles `crew`, `usage`, and `workers`.
Panes accept `orchestrator`, `memory`, and `issue`.
Each role must be unique within the layout.

```toml
[[tabs]]
name = "Crew · {task}"
role = "crew"

[[tabs.panes]]
label = "Orchestrator · {task}"
role = "orchestrator"
```

Plus publishes workspace tokens before it starts commands:
`crew_task`, `crew_<role>_tab`, and `crew_<role>_pane`.
These tokens contain the resolved task and native IDs.
Crew return uses `crew_crew_tab` when present.
Renaming the tab does not change its identity.
A missing bound tab causes refusal. It cannot redirect to a matching label.
Layouts without roles keep the existing label-based return behavior.

## Rename

A workspace rename event renames the labels that the task wrote.
A label is a task label in one of these cases:

- It equals `<role> · <crew_task>`.
- It has the form `<role> · <issue ID> <name>`, with an optional ` · <suffix>`.
  The issue ID must start the workspace label.

The second case is necessary for three reasons:

- Herdr stores `crew_task` cut at 80 characters.
- Older task starts named some labels from the raw ask.
- Seat panes end with ` · <provider> <model> <effort>`.

The role is a role token, or one of these label prefixes:

- Tabs: `Workers`, `Archive`, `Review`.
- Panes: `Orchestrator`, `Memory`, `Issue / Utility`, `Usage`, or `<Role> <n>`.

The rename keeps the suffix. A bare `<role> · <issue ID>` label stays unchanged.

`Workers` and `Archive` tabs get `<role> · <short task>`, with the optional suffix.
All other tabs and all panes get the full task.
The short task follows dot `bin/task_tab_title.py`:

- The issue ID stays in full. So does a marker after it, `🧌` or `🧙‍♂️`.
- The rest stays when it has 25 grapheme clusters or fewer.
- Otherwise the rename keeps the first 25 clusters and removes trailing space.
  Then it adds `…`.
- A task with no issue ID has no marker. Its whole name is the rest.

A `Workers` or `Archive` label that equals `<role> · <short crew_task>` is a task label.
This rule also finds the label of a task with no issue ID.
A label for another issue also stays unchanged.
A rename to the same name repairs labels that an earlier rename missed.
