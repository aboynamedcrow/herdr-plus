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
