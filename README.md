# herdr-plus

herdr-plus is an add-on for [herdr](https://herdr.dev), built as a first-class
[herdr plugin](https://herdr.dev/docs/plugins/). It adds two things:

- **[Projects](#projects)** — declarative herdr-workspace templates you fuzzy-pick
  to spin up a whole workspace (every tab and pane, every startup command) in one
  keypress.
- **[Quick Actions](#quick-actions)** — a fuzzy launcher for one-off
  actions/scripts, run in the directory you launched from.

## Install

herdr-plus is a herdr plugin (requires **herdr ≥ 0.7.0**). Installing it registers
the plugin's actions with herdr — no editing of your `config.toml`.

```bash
herdr plugin install cloudmanic/herdr-plus
```

herdr clones the repo, runs the manifest's `[[build]]` step, and registers the
actions. That step builds **the source herdr just checked out**, so **Go must be
on your `PATH`** — there is no fallback to a prebuilt release binary, which would
not contain the code in that checkout. A missing toolchain fails the install with
instructions rather than quietly installing something else. Manage it with
`herdr plugin list`,
`herdr plugin action list --plugin cloudmanic.herdr-plus`, and
`herdr plugin uninstall cloudmanic.herdr-plus`.

> **Windows** (herdr's Windows support is in preview): the plugin installs and
> runs on Windows the same way — its build step compiles straight from source, so
> **Go must be on your `PATH`**. The plugin talks to herdr over a named pipe there
> instead of a unix socket; it's validated against the herdr Windows beta.

**Local development:** build the binary and link your checkout in place:

```bash
make build
herdr plugin link /path/to/herdr-plus     # or: make plugin-link
```

### Just the binary

If you'd rather have `herdr-plus` on your `PATH` (e.g. to run `herdr-plus version`),
prebuilt binaries are published on every release:

```bash
# Homebrew (the repo is its own tap)
brew tap cloudmanic/herdr-plus https://github.com/cloudmanic/herdr-plus
brew install cloudmanic/herdr-plus/herdr-plus

# or the install script (Linux/macOS, no Homebrew)
curl -fsSL https://raw.githubusercontent.com/cloudmanic/herdr-plus/main/install.sh | sh
```

The binary on its own doesn't register the plugin with herdr — use
`herdr plugin install` (above) for that. Every merge to `main` cuts a new release
with cross-compiled binaries.

## Configuration

herdr-plus keeps its config in herdr's managed plugin directory — find it with:

```bash
herdr plugin config-dir cloudmanic.herdr-plus
# → ~/.config/herdr/plugins/config/cloudmanic.herdr-plus
```

Inside it, `projects/` holds your [project templates](#projects) and
`quick-actions/` your [actions](#quick-actions). herdr provisions this directory
and keeps it across uninstall/upgrade. (Running the binary *outside* herdr falls
back to `~/.config/herdr-plus/`, honoring `$XDG_CONFIG_HOME`.)

Optional global settings live in `config.toml` in that same directory:

```toml
[worktree]
branch_prefix = "your-name/" # used verbatim; include your own trailing /

[projects]
placement = "zoomed"     # overlay, popup, split, tab, or zoomed; default is zoomed
crew_tab = ""            # empty (default) off; a tab label turns on "return to my task tab"
reuse_checkout = false   # default false; true focuses an already-open checkout

[quick_actions]
placement = "overlay" # overlay, popup, split, tab, or zoomed; default is overlay
```

`crew_tab` and `reuse_checkout` are both off by default, and both are described
under [Returning to work you already have open](#returning-to-work-you-already-have-open).

`placement` controls how herdr opens each picker — see herdr's
[`plugin pane open --placement`](https://herdr.dev/docs/plugins/#panes) for what
each value does. `popup` is a good fit for either picker if you want a small
floating window that leaves your tiled layout untouched, rather than the default
zoomed/overlay takeover. An empty or invalid value falls back to the built-in
default and prints a warning to stderr rather than being passed through to herdr.

## Projects

Pick a project from a full-screen fuzzy browser and herdr-plus builds its whole
workspace. Trigger it from herdr's plugin action menu, or
[bind a key](#binding-a-key) — the action is `cloudmanic.herdr-plus.projects`.
(With `[projects].crew_tab` configured the action returns to your current task
tab instead of opening the browser — see
[Returning to work you already have open](#returning-to-work-you-already-have-open).)
Inside the browser, **Enter** opens the highlighted project as a normal workspace;
**ctrl+g** opens it as a git worktree. The worktree prompt accepts an optional
branch name: empty lets herdr generate `worktree/...`, bare names get the optional
`[worktree] branch_prefix`, and names containing `/` are used as-is.

Opening as a worktree fills its tabs from a matching
[worktree auto-layout](#worktree-auto-layout) — a file in `worktrees/` whose `repo`
matches — **not** the project's own `[[tabs]]`. Without a matching layout the
worktree opens with herdr's default single pane, so add a `worktrees/` file for any
repo you open this way.

A project is one TOML file in the `projects/` subdir of
[herdr-plus's config dir](#configuration). The file name doesn't matter; add a
file to add a project, delete it to remove it. With no files there, the browser
shows an onboarding card.

```toml
name = "Options Cafe"
description = "The main options.cafe monorepo"
working_dir = "~/Development/options-cafe/options.cafe"   # ~ and $VARS expand

[[tabs]]
name = "claude"
command = "claude --dangerously-skip-permissions --chrome"

[[tabs]]
name = "lazygit"
command = "lazygit"

[[tabs]]
name = "terminal"   # no command — just an empty shell
```

Tabs open in file order. The first tab reuses the workspace's root tab; the rest
are created behind it. A tab with no `command` is just an empty shell.

### A directory per tab

The project's `working_dir` is where every tab starts. A tab — or a single pane —
can override it with its own `working_dir`, which is what a monorepo usually wants:

```toml
name = "Shop"
working_dir = "~/dev/shop"

[[tabs]]
name = "web"
working_dir = "frontend"   # relative to the project → ~/dev/shop/frontend
command = "npm run dev"

[[tabs]]
name = "api"
working_dir = "backend"    # → ~/dev/shop/backend
command = "make run"

[[tabs]]
name = "shell"             # no working_dir → the project's ~/dev/shop
```

A relative path is resolved against the project's `working_dir`, so `"frontend"`
means what it looks like. Absolute paths, `~`, and `$VARS` work the same as they do
at the project level. Panes inherit their tab's directory unless they set their own:

```toml
[[tabs]]
name = "stack"
working_dir = "frontend"

[[tabs.panes]]
command = "npm run dev"          # frontend/

[[tabs.panes]]
command = "psql shop"
split = "right"
working_dir = "../backend/db"    # its own directory instead
```

Every directory must exist. A missing one is reported before anything opens, so a
typo never leaves you a half-built workspace to clean up.

### Opening by name (headless)

The picker is interactive, but a project can also be opened directly — no browser,
no fuzzy-picking — with the `open` subcommand:

```sh
herdr-plus open harbor-sysadmin
```

It loads the same templates, resolves the one whose `name` matches (exactly, or
case-insensitively as a fallback), and builds its workspace through the **identical
code path** the picker uses — so there is a single source of truth for the layout.
A mistyped name fails with the list of available projects.

Run it from **inside herdr** (a pane shell, a keybinding, another tool): like every
herdr-plus command it reaches the running herdr over its socket, and it asks herdr
where your project templates live, so it finds the same ones the picker shows. This
is the scriptable entry point for spinning up a known workspace in one shot — handy
for shell aliases, scripts, and AI agents. (To get `herdr-plus` on your `PATH`, see
[Just the binary](#just-the-binary).)

### Ensuring a worktree (headless API)

```sh
herdr-plus ensure-worktree --cwd /absolute/repo --branch feature/a \
  --base main --path '/absolute/worktrees/new checkout' [--focus]
```

`--cwd` and `--branch` are required. The command resolves symlinks and the Git
repository root from the explicit cwd; it does not select another client's
focused pane or load project configuration. Inherited Git repository, index,
and object selectors cannot override that cwd; Git transport, authentication,
and configuration settings remain available. Branch names are literal Git branch
names. `--base` is a remote branch tail under `origin` (for example, `main` or
`release/stable`), not a revision expression or fetch refspec; a leading `+` is
rejected. `--path` must be absolute. Supplied values are validated even when
existing state makes them unnecessary. Duplicate string flags and positional
arguments are rejected.

| Git state for the exact branch | Native operation |
| --- | --- |
| Registered checkout | `worktree.open` with cwd, branch, and focus; Git's registered path wins over any supplied path. |
| Local branch without a checkout | `worktree.create` with cwd, branch, focus, and path if supplied; preserves the branch without fetching or passing a base. |
| Neither | Requires base and path; runs `git fetch origin refs/heads/BASE:refs/remotes/origin/BASE`, then `worktree.create` with base `origin/BASE` and the explicit path. |

The explicit fetch destination refreshes `origin/BASE` even when
`remote.origin.fetch` excludes BASE. A failed fetch stops before native create
and is never retried; local branches are never force-updated or reset.

An existing target file, directory, or dangling symlink is refused before creating
a checkout. An existing registered checkout needs neither base nor path. Focus is
false by default; `--focus` opts in. Plus does not create or remove Git worktrees,
reset branches, retry native refusals, or apply a layout itself. Native Herdr owns
the create/open operation and any resulting event handling.

Success writes exactly one JSON object to stdout, preserving the native result:

```json
{"result":{"workspace":{"workspace_id":"w1"},"root_pane":{"pane_id":"w1:p1"},"worktree":{"path":"/absolute/worktrees/new checkout","branch":"feature/a"},"already_open":true}}
```

The example IDs are illustrative. Actual workspace and root-pane IDs must come
from Herdr; the command validates them and the returned worktree path/branch
before emitting success. Additional native fields, including `already_open` when
native open supplies it, pass through. Failures write stderr and exit nonzero
without a success object. Git queries have 10-second limits, fetch has a 2-minute
limit, and native IPC has a 30-second limit. A timed-out native mutation has an
unknown outcome and is never automatically retried.

Run `go test -run 'TestEnsureWorktree' ./...` for the API verification recipe,
then `go test ./...` and `git diff --check`. The focused suite builds and executes
the real CLI, uses disposable Git repositories with isolated Git configuration,
and checks calls through a synthetic Unix socket. A controlled executable
intercepts fetch except for an explicitly allowed temporary local-file origin
that verifies actual remote-tracking ref freshness with a narrowed fetch mapping.
That fixture allows only the file protocol; tests never fetch over the network
or mutate live Herdr. Additional real Git queries check index/object isolation.
Fixtures, sockets, and owned processes are cleaned up. The Unix socket suite is
skipped on Windows; named-pipe and installed/native acceptance remain separate gates.

Naming policy, issue-ID lookup, contextual P, W/G/L caller migration, and
installed/native acceptance are separate dependent slices. This command does not
change those callers or their existing behavior.

### Grouping

A project may set an optional `group` to cluster related projects under a heading
in the browser (handy when one client has several). Projects sharing a `group` are
shown together; group-less ones fall under an **Ungrouped** heading. Grouping only
engages when at least one project sets a `group` — otherwise the list is plain.
Filtering ignores headings: start typing and it collapses to one ranked list.

### Split panes within a tab

A tab can hold up to **4 panes**. Instead of a single `command`, give it
`[[tabs.panes]]` entries. Each pane after the first sets `split` to `"down"`
(stacked) or `"right"` (side by side) — how it splits off the previous pane. An
omitted `split` defaults to `"down"`.
A pane may also set `ratio` — how much of that split it takes, between `0` and
`1`, leaving the rest to the pane it splits off. Omitted, the split is even.
Each pane may also set an optional `label` — the name herdr shows on the pane
border (when `show_agent_labels_on_pane_borders` is on). A blank or omitted
`label` leaves the pane's default name untouched.

```toml
[[tabs]]
name = "server"

[[tabs.panes]]
label = "Server"
command = "php artisan serve"

[[tabs.panes]]
label = "Assets"
command = "npm run dev"
split = "down"
ratio = 0.3
```

A tab uses *either* `command` *or* `[[tabs.panes]]`, not both.

#### Choosing which pane to split

By default each pane splits the one created just before it, which is a chain: A,
then B off A, then C off B. Some arrangements cannot be written that way. A tab
with a full-height pane down one edge and a stack of panes beside it needs the
stacked panes to split *each other*, not the edge — otherwise the edge pane is
cut in half by the second split.

`split_from` says which pane to split, as a 1-based index of an **earlier pane in
the same tab**. Omitted (or `0`) keeps the previous-pane default, so existing
configs are unchanged.

```toml
[[tabs]]
name = "Work"

[[tabs.panes]]
label = "Notes"          # pane 1, the tab's root

[[tabs.panes]]
label = "Editor"         # pane 2 splits pane 1, keeping Notes full height
split = "right"
split_from = 1
ratio = 0.75

[[tabs.panes]]
label = "Tests"          # pane 3 splits pane 2, stacking under the editor
split = "down"
split_from = 2
```

The first pane is the tab's root and splits nothing, so it must leave `split_from`
unset. A value that is not an earlier pane — its own index, a later pane, one that
does not exist — is refused when the file is read, before any workspace is
created. Creation order can differ from the visual arrangement; `label`, `ratio`,
`working_dir` and startup commands all behave exactly as they did.

## Returning to work you already have open

Both settings below are opt-in and off by default. They exist for the same
situation: you already have the thing you are about to open, and creating a
second copy of it is not what you meant.

Neither one ever re-runs a layout, re-creates a tab or pane, or types into a
pane. Returning to work leaves that work exactly as you left it.

### `crew_tab` — the Projects key returns to your task tab

Set `crew_tab` to the label of the tab you work in:

```toml
[projects]
crew_tab = "Crew"
```

With it set, the Projects action checks the pane it was invoked from. If that
pane is in a workspace herdr opened for a **linked git worktree**, and that
workspace has exactly one tab with this label, the action focuses that tab
instead of opening the browser. Pressing the key again just focuses it again.

Anywhere else — a plain folder, a repository's main checkout, no context at all —
the browser opens exactly as before.

If the tab has been renamed, closed, or exists more than once, you get a message
saying so and **nothing is created**: herdr-plus will not invent a second task
tab or guess which of two is yours. A failed lookup is likewise an error, never
silently treated as "not a task workspace".

The tab label is entirely yours — no name is built into the plugin. Leaving
`crew_tab` empty keeps the plain picker-only behavior.

To open a *different* project from inside one you are working in, use the
separate `cloudmanic.herdr-plus.projects-pick` action (or run the binary with
`projects --pick`). It always opens the browser.

### `reuse_checkout` — opening a project focuses it if it is already open

```toml
[projects]
reuse_checkout = true
```

With it on, choosing a project first asks herdr whether any open workspace
already has that exact directory checked out. If one does, it is focused, and no
workspace, tab, pane or startup command is created.

"Exact" means the canonical filesystem path, so a symlinked alias of an open
checkout counts as the same checkout. Two worktrees of the same repository do
**not**: they share a repository, not a checkout, so opening the second one
builds it. Matching never uses a workspace's title.

If two workspaces somehow hold the same checkout, you are asked which one, by
herdr's own workspace id — nothing is picked for you, and cancelling does
nothing at all. If herdr cannot be asked, or the directory cannot be resolved,
that is an error rather than an assumption that nothing matched.

Headless `herdr-plus open <name>` honors the same setting, except that it has
nobody to ask: several matching workspaces is an error naming them.

Because herdr records checkout provenance only for workspaces it opened as a
worktree — not for the ones this plugin creates — a project workspace is bound to
its checkout right after it is built, by asking herdr to open the checkout it
already has open. herdr recognizes the workspace, records the checkout, and
leaves its tabs, panes and running commands untouched; the next open then finds
it. If that binding fails you are told so at the time, and the workspace itself
is left alone and perfectly usable.

Whether a project is a Git checkout at all is herdr's answer, not a guess: if
herdr says the directory is not inside a Git work tree, the project opens as it
always has. If Git itself cannot be read — no `git` on `PATH`, a corrupt or
unreadable registry, a listing that contradicts herdr — you get an error *before*
anything is created, because at that point nobody can tell whether the project is
already open, and a wrong guess is the duplicate workspace this is meant to
prevent.

Binding has these limits:

- Binding a **linked worktree** asks herdr to act from the repository's primary
  checkout, which is what herdr requires. Open the primary-checkout project
  first. Otherwise this plugin refuses before creating anything: native grouping
  would create an empty parent that could later shadow the primary project's
  configured layout. Binding names the verified parent workspace explicitly, so
  closing it during the operation produces an error, not another parent.
- If herdr reports an open workspace in your project's checkout but it carries no
  provenance (including a scratch workspace whose shell entered that checkout), the open is
  **refused** with a message naming it. Nothing is created and nothing is
  changed: that workspace cannot be verified as your project, and guessing is
  exactly what this feature refuses to do. Inspect it and close it when safe, or
  open the checkout through herdr's own worktree open, and try again.
- Reuse requires a repository herdr can list and a non-bare primary checkout.
  Bare-main repositories are unsupported with this opt-in setting; turn it off
  to retain the original project-opening behavior. Repository trust errors are
  reported without granting trust automatically.

**Limitations:** identity comes from the checkout herdr records for a workspace,
so this applies to projects whose `working_dir` is a git checkout root. A project
pointing at a plain (non-git) directory, or at a *subdirectory* of a checkout,
has no such provenance and keeps the old behavior — a new workspace every time.

## Shared worktree policy

The optional `worktree` action and Projects' Ctrl+G share a planner with external
callers such as an issue picker. Configure one policy per primary repository:

```toml
[worktree]
branch_prefix = "your-name/"

[[worktree.projects]]
name = "project"
repository = "~/code/project"
root = "~/code/worktrees/project"
max_tail = 29
# base = "develop" # optional remote branch override
```

The picker shows the resulting branch and checkout path before applying it.
Existing branches and registered paths are preserved. Issue matches use complete
identifiers, so `IC-177` cannot select `IC-1770`; multiple matching branches remain
explicit choices. New descriptions become lowercase kebab case within `max_tail`.
New checkout directories replace branch slashes with `--` under the configured root.

New branches use the remote's current default branch, or the configured override.
Missing or unreadable remote defaults produce an error. Existing branches need
neither a base query nor a fetch. The primary project must already be open with
native checkout provenance; applying a selection never creates an implicit parent.

For external callers, `plan-worktree --cwd /absolute/checkout --name "description"
--issue IC-177` returns versioned JSON with `candidates` and a `fingerprint`.
Planning reads Git and remote metadata without creating directories, fetching, or
changing branches. Pass the same inputs plus `--candidate <id> --fingerprint <hash>`
to `apply-worktree`. It rebuilds the plan, refuses stale or absent selections, and
delegates native creation/opening to `ensure-worktree`. Its `result` preserves the
native workspace, pane and checkout fields. Cancelling means never invoking apply.

The W action requires Herdr's explicit invoking-pane context and opens a temporary
overlay picker. It never chooses a project from another client's current focus.
`ensure-worktree` also accepts an optional `--workspace` for an explicitly verified
primary workspace; the older explicit-cwd interface remains available.

## Quick Actions

A fuzzy launcher for one-off commands. Trigger it (action
`cloudmanic.herdr-plus.quick-actions`), fuzzy-pick an action, and it runs in the
directory you launched from. Actions are TOML files in the `quick-actions/` subdir
of [herdr-plus's config dir](#configuration) (seeded with editable examples on
first run). A repo can also ship its own in `<repo>/.herdr-plus/quick-actions/`, shown
under a **Project** heading above your **Global** ones — this repo ships
`make build` / `make test` as a live example.

There are three action types:

```toml
# command (default) — runs immediately
name = "GitHub"
command = "open https://github.com"
```

```toml
# select — pick from a second fuzzy list; the choice becomes {{.Value}}
name = "Open Repo"
type = "select"
command = "open https://github.com/cloudmanic/{{.Value}}"

[[options]]
label = "Herdr Plus"
value = "herdr-plus"
```

A select action can build its list at open time instead of hard-coding it. Set
`options_command` in place of `[[options]]` and it runs fresh every time the
action is picked, one option per line of stdout:

```toml
# select — options come from a command, so the list follows what is on disk
name = "New Project"
type = "select"
options_command = "ls -1 ~/projects"
command = "cd ~/projects/{{.Value}} && $EDITOR ."
```

`options_command` is templated exactly like `command`, so it can reference
`{{.WorkDir}}` and friends. A line becomes the option's label *and* value; add a
tab to give it a description — `herdr-plus\tinstalled` shows "installed" beside
the row without it ending up in `{{.Value}}`. If the command fails, the picker
shows the error as an unselectable row rather than an empty list.

```toml
# form — type a value that becomes {{.Value}}
name = "Search Google"
type = "form"
command = "open 'https://www.google.com/search?q={{.Value | urlquery}}'"

[form]
prompt = "Search Google for"
```

The `command` is a [Go template](https://pkg.go.dev/text/template) rendered against
the launch context: `{{.WorkDir}}` (where you launched from), `{{.SessionTitle}}`
(the workspace label), `{{.Value}}` (select/form input), and more — also exported
as `HERDR_PLUS_*` environment variables. If a command doesn't reference
`{{.Value}}`, the value is appended as a final shell-quoted argument.

## Worktree auto-layout

herdr-plus can lay a project-style tab layout into a git **worktree** the moment
herdr creates *or opens* it. When you run `herdr worktree create`/`open` (or use
herdr's right-click worktree dialog), herdr makes a fresh workspace for the
worktree and fires a `worktree.created` event (new worktree) or `worktree.opened`
event (existing one); herdr-plus catches either, finds a layout matching the
worktree's repo, and opens that layout's tabs and panes in the new workspace —
every command running — with no keypress. This is the plugin system's `[[events]]`
hook (declared in [`herdr-plugin.toml`](herdr-plugin.toml)) put to work.

Layouts live in `~/.config/herdr-plus/worktrees/`, one TOML file per layout (the
file name doesn't matter). A layout is a `repo` matcher plus the same `[[tabs]]`
format projects use:

```toml
repo = "options-cafe"          # matches the worktree's repo name (case-insensitive)

[[tabs]]
name = "claude"
command = "claude --dangerously-skip-permissions --chrome"

[[tabs]]
name = "lazygit"
command = "lazygit"

[[tabs]]
name = "terminal"              # no command — just an empty shell
```

- **`repo`** (required) matches the new worktree's repository name — the repo's
  basename, e.g. `options-cafe` — case-insensitively. Set `repo = "*"` to create a
  **wildcard layout** that matches any repo (see below).
- **`branch`** (optional) narrows a layout to worktrees created on exactly that
  branch. When more than one layout matches, a branch-specific one wins over a
  repo-only one.
- **`[[tabs]]`** is identical to a project's tabs, including multi-pane
  `[[tabs.panes]]` splits (see [Split panes within a tab](#split-panes-within-a-tab)).

### Turning a layout on and off

The switch is simply **whether the file exists**. A layout in `worktrees/` is on;
to turn one off, delete the file (or move it out of the directory). With no files
in `worktrees/` at all, the feature is inert — every worktree fires the event, and
herdr-plus does nothing when nothing matches.

The handler's output shows up in `herdr plugin log list --plugin
cloudmanic.herdr-plus`, so you can confirm whether a layout fired.

### Wildcard (generic) layouts

Set `repo = "*"` to define a layout that applies to **every** repo that doesn't
have its own specific layout — a generic project template:

```toml
repo = "*"

[[tabs]]
name = "claude"
command = "claude --dangerously-skip-permissions --chrome"

[[tabs]]
name = "code-review"
command = "lazygit"

[[tabs]]
name = "terminal"
```

This is useful when you have many repos that all want the same workspace shape.
Instead of one file per repo, write one wildcard layout and you're done.

**Specificity:** when multiple layouts match a worktree, the most specific wins:

1. Repo + branch (e.g. `repo = "my-app"`, `branch = "main"`)
2. Repo only (e.g. `repo = "my-app"`)
3. Wildcard + branch (e.g. `repo = "*"`, `branch = "main"`)
4. Wildcard only (e.g. `repo = "*"`)

A repo-specific layout always beats a wildcard, so you can set a generic default
and still override individual repos when needed.

## Binding a key

Binding keys to the actions is an optional, one-time edit to **your** herdr
`config.toml` (`~/.config/herdr/config.toml`). Add `[[keys.command]]` entries with
`type = "plugin_action"` whose `command` is the action id:

```toml
[[keys.command]]
key = "prefix+up"
type = "plugin_action"
command = "cloudmanic.herdr-plus.projects"
description = "herdr-plus: projects"

[[keys.command]]
key = "prefix+down"
type = "plugin_action"
command = "cloudmanic.herdr-plus.quick-actions"
description = "herdr-plus: quick actions"
```

Then `herdr server reload-config` (or restart herdr) and press your herdr prefix
(default `ctrl+b`) followed by the bound key.

## Building

```bash
make build     # build ./bin/herdr-plus
make test      # go test -race ./...
make vet       # go vet ./...
```

The marketing + docs site lives in `www/` (Hugo + Tailwind). Build it with
`make site`, or run it locally with live reload via `make site-dev`.

Projects and Worktree action failures appear as a Herdr notification and in the plugin log. Notification delivery is best effort when the server is unavailable or notifications are suppressed. This fork requires Herdr 0.9.0 or newer.
