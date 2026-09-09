//
// Date: 2026-06-15
// Author: Spicer Matthews (spicer@cloudmanic.com)
// Copyright: 2026 Cloudmanic Labs, LLC. All rights reserved.
//

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// launchProjects is the Projects action's entry point. herdr runs it server-side
// (from the plugin action / keybinding), so it has no terminal of its own. It
// asks herdr to open the projects browser as a pane (the `picker` entrypoint in
// herdr-plugin.toml) — zoomed by default, or the placement set by
// [projects].placement in config.toml. herdr creates and tears down that pane for
// us, so — unlike the old design — there is no throwaway workspace to manage.
//
// With [projects].crew_tab configured the action gains one step in front of that:
// fired from inside a linked worktree workspace, it returns to that workspace's
// task tab instead of offering a picker, so the same key both starts work and
// comes back to it. Nothing is created or relaid on that path — see returnToCrew.
// `projects --pick` skips the step entirely when you do want another project.
func launchProjects(args []string) {
	pick, err := parseProjectsArgs(args)
	if err != nil {
		errExit(err)
	}

	cfg, err := loadPluginConfig()
	if err != nil {
		errExit(err)
	}
	placement := resolvePlacement(cfg.Projects.Placement, "zoomed")

	// The context herdr injected for this invocation: the pane the action fired
	// from. It decides whether there is a Crew to return to, and is forwarded to
	// the picker pane so the browser runs with the caller's directory in hand.
	ctx := contextFromPluginEnv()

	if !pick && strings.TrimSpace(cfg.Projects.CrewTab) != "" {
		pc, err := pluginContextFromEnv()
		if err != nil {
			errExit(err)
		}
		if strings.TrimSpace(pc.WorkspaceID) != "" {
			client, err := newHerdrClient()
			if err != nil {
				errExit(err)
			}
			focused, err := returnToCrew(client, pc, cfg.Projects.CrewTab)
			if err != nil {
				errExit(err)
			}
			if focused {
				return
			}
		}
	}

	enc, err := ctx.encode()
	if err != nil {
		errExit("could not encode run context:", err)
	}

	// HERDR_BIN_PATH points at the running herdr binary; it is the portable way to
	// call back into the CLI from a plugin command.
	herdr := os.Getenv("HERDR_BIN_PATH")
	if herdr == "" {
		herdr = "herdr"
	}

	// IMPORTANT: no --cwd. The manifest registers this pane with a relative
	// command (./bin/herdr-plus), which herdr resolves against the pane's working
	// directory, so the pane must run in the plugin's own install dir. The user's
	// directory is context data, not an executable lookup path, and reaches the
	// browser through HERDR_PLUS_CTX — the same way Quick Actions forwards it.
	cmd := exec.Command(herdr, "plugin", "pane", "open",
		"--plugin", pluginID,
		"--entrypoint", paneEntrypoint("picker"),
		"--placement", placement,
		"--env", "HERDR_PLUS_CTX="+enc,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		errExit("could not open the projects browser:", err)
	}
}

// runProjectsUI renders the full-screen projects browser. It runs inside the
// zoomed pane herdr opens for the `picker` entrypoint (which has a real
// terminal), loads the projects, and — when one is chosen — spins up its
// workspace. On cancel it simply exits and herdr tears the pane down.
func runProjectsUI() {
	projects, err := loadProjects()
	if err != nil {
		// Leave the pane open so the user can read the config error.
		errExit(err)
	}

	cfg, err := loadPluginConfig()
	if err != nil {
		errExit(err)
	}

	dir, _ := projectsConfigDir()

	// WithMouseCellMotion enables click/release/wheel events so a project can be
	// opened with the mouse. herdr forwards these to us once we ask for them;
	// until then it keeps the mouse for its own pane focus/selection.
	p := tea.NewProgram(newProjectsModel(projects, dir, cfg.Worktree.BranchPrefix), tea.WithAltScreen(), tea.WithMouseCellMotion())
	result, err := p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "herdr-plus:", err)
	}

	m, ok := result.(projectsModel)
	if !ok || m.chosen == nil {
		// Cancelled (or the program never produced a model) — nothing to do; herdr
		// closes the pane when this process exits.
		return
	}

	client, err := newHerdrClient()
	if err != nil {
		errExit(err)
	}
	if m.worktree {
		if err := openProjectAsWorktree(client, *m.chosen, m.branch); err != nil {
			errExit("could not open project as worktree:", err)
		}
		return
	}
	// With [projects].reuse_checkout on, a project whose checkout is already open
	// focuses that workspace instead of building a second one; several matches ask
	// which, in this same pane, before anything happens. See reuseOpenCheckout.
	reuse := reuseOptions{enabled: cfg.Projects.ReuseCheckout}
	if reuse.enabled {
		reuse.choose = func(candidates []workspaceCandidate) (workspaceCandidate, bool, error) {
			return chooseWorkspaceInteractively(candidates, m.chosen.displayWorkingDir())
		}
	}
	if err := openProject(client, *m.chosen, reuse); err != nil {
		errExit("could not open project:", err)
	}
}

// openProject turns a project into a live herdr workspace: it creates a focused
// workspace rooted at the project's working directory and lays out its tabs and
// panes, running each startup command — unless reuse is enabled and that exact
// checkout is already open, in which case the existing workspace is focused and
// nothing is created. Creating the focused workspace switches
// the user to it; the picker pane this was launched from is then torn down by
// herdr when runProjectsUI exits.
func openProject(client *herdrClient, p Project, reuse reuseOptions) error {
	dir, err := p.expandedWorkingDir()
	if err != nil {
		return err
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("working directory does not exist: %s", dir)
	}

	// Check every tab and pane directory now, while backing out still costs
	// nothing. layoutTabs would catch the same problem, but only after the
	// workspace exists — leaving the user an empty workspace to close by hand.
	if err := checkTabDirs(dir, p.Tabs); err != nil {
		return err
	}

	// Before creating anything, ask herdr whether this exact checkout is already
	// open. A match is focused as it stands — no layout, no startup commands, no
	// repair — and a failure to find out is an error, never a licence to make a
	// duplicate. Off by default; see reuseOpenCheckout for the identity rules.
	var checkout *gitCheckout
	if reuse.enabled {
		reused, err := reuseOpenCheckout(client, dir, reuse.choose)
		if err != nil {
			return err
		}
		if reused {
			return nil
		}

		// No workspace carries provenance for this checkout — but herdr may still
		// have it open in a workspace it holds none for (one from an older build of
		// this plugin, say), and the question of whether this is even a Git checkout
		// belongs to herdr rather than to a local Git command that might not answer.
		// Everything uncertain is settled here, before anything is created. See the
		// commentary in projectreuse.go.
		checkout, err = resolveCheckoutForReuse(client, dir)
		if err != nil {
			return err
		}
	}

	ws, rootTab, rootPane, err := client.workspaceCreate(dir, p.Name, true)
	if err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}

	// Lay the project's tabs into the new workspace. dir anchors any per-tab or
	// per-pane working_dir written relative to the project.
	if err := layoutTabs(client, ws, rootTab, rootPane, dir, p.Tabs); err != nil {
		return err
	}

	// The workspace and its layout are finished; now let herdr record which
	// checkout it holds, so opening this project again returns to it instead of
	// building a second one. Only for a Git checkout, and only with reuse on —
	// nothing about the default behavior changes. A failure here leaves the
	// finished workspace alone and says so: the work is usable, but this
	// workspace will not be recognized on the next open.
	if checkout != nil {
		if err := bindCheckoutProvenance(client, ws, *checkout); err != nil {
			return fmt.Errorf("%w\n  The workspace is open and laid out; only its checkout binding failed, so opening this project again will not return to it", err)
		}
	}
	return nil
}

// openProjectAsWorktree creates a git worktree from the project's working
// directory instead of opening the project as a normal workspace. It resolves and
// validates the directory, confirms it is inside a git work tree (worktrees
// require one — a clearer error than herdr's if not), and asks herdr to create the
// worktree, focused. The new workspace's tabs are not laid out here: herdr's
// worktree.created event drives the worktree auto-layout, so a matching file in
// worktrees/ fills the workspace. Without one the worktree opens bare — the
// project's own [[tabs]] are not used for this path.
func openProjectAsWorktree(client *herdrClient, p Project, branch string) error {
	// filepath.Abs already returns an absolute path (or an error), so no separate
	// IsAbs check is needed after it.
	expanded, err := p.expandedWorkingDir()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	dir, err := filepath.Abs(expanded)
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("working directory does not exist: %s", dir)
	}
	if !isInsideGitWorkTree(dir) {
		return fmt.Errorf("not a git repository: %s (opening as a worktree requires one)", dir)
	}
	return client.worktreeCreate(dir, branch, true)
}

// isInsideGitWorkTree reports whether dir is inside a git work tree, so
// openProjectAsWorktree can give a clear error before asking herdr to create a
// worktree there. It shells out to git: when git runs and reports the directory is
// not a work tree it returns false, but when git cannot be run at all (not
// installed / not on PATH) it returns true, deferring the real check to herdr
// rather than blocking on git's absence.
func isInsideGitWorkTree(dir string) bool {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false // git ran and said this is not a work tree
		}
		return true // git could not run — let herdr make the call
	}
	return strings.TrimSpace(string(out)) == "true"
}

// resolvePaneDirs resolves the directory every pane of every tab should start in,
// returning a table indexed the same way layoutTabs walks them: dirs[i][j] is tab
// i pane j. A pane that declares no working_dir of its own gets root, the
// workspace's own directory.
//
// Every pane gets an explicit directory rather than being left to inherit one:
// herdr starts a new tab in the directory of the tab that was active when it was
// created, not the workspace's, so a tab declaring no working_dir would otherwise
// drift into whichever directory the tab before it happened to use. Naming the
// directory every time makes each tab depend only on its own config.
//
// It runs as one pass up front so a mistyped path fails before any tab is
// created, rather than stranding the user with half a workspace. Each resolved
// directory must already exist — herdr would either error or silently start
// somewhere unexpected, and a clear message naming the tab is far more useful.
func resolvePaneDirs(root string, tabs []ProjectTab) ([][]string, error) {
	dirs := make([][]string, len(tabs))
	for i, t := range tabs {
		panes := t.effectivePanes()
		dirs[i] = make([]string, len(panes))
		for j, pane := range panes {
			dir, err := resolveNestedDir(pane.WorkingDir, root)
			if err != nil {
				return nil, fmt.Errorf("tab %q pane %d: %w", t.Name, j+1, err)
			}
			if dir == "" {
				// Nothing declared, so the workspace's own directory it is. A caller
				// with no root to offer passes "", which omits the key and leaves
				// herdr's inheritance in charge exactly as it was before.
				dirs[i][j] = root
				continue
			}
			if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
				return nil, fmt.Errorf("tab %q pane %d: working directory does not exist: %s", t.Name, j+1, dir)
			}
			dirs[i][j] = dir
		}
	}
	return dirs, nil
}

// checkTabDirs reports whether every tab and pane directory in tabs resolves and
// exists, without creating anything. It lets a caller fail before committing to a
// workspace it would then have to tear down.
func checkTabDirs(root string, tabs []ProjectTab) error {
	_, err := resolvePaneDirs(root, tabs)
	return err
}

// resolveSplitTargets resolves, for every pane of every tab, which pane it is
// split off — the previous one by default, or the pane its split_from names.
// targets[i][j] is the 0-based index within tab i of the pane that tab i's pane j
// splits, and -1 for each tab's root pane, which splits nothing.
//
// It runs as one pass before any native call so an impossible target is refused
// while backing out is free. Config loading validates the same rule (see
// splitTargetIndex), so this is a second, closer guard rather than the only one:
// layoutTabs is reachable from both projects and worktree layouts.
func resolveSplitTargets(tabs []ProjectTab) ([][]int, error) {
	targets := make([][]int, len(tabs))
	for i, t := range tabs {
		panes := t.effectivePanes()
		targets[i] = make([]int, len(panes))
		for j := range panes {
			// Check the pane as it was written, not as effectivePanes normalized it:
			// normalization clears the root pane's split fields, which would hide a
			// root pane that declared a split_from it cannot have. Only SplitFrom is
			// read here, so the raw and normalized panes are otherwise equivalent.
			pane := panes[j]
			if j < len(t.Panes) {
				pane = t.Panes[j]
			}
			idx, err := splitTargetIndex(j, pane)
			if err != nil {
				return nil, fmt.Errorf("tab %q %w", t.Name, err)
			}
			targets[i][j] = idx
		}
	}
	return targets, nil
}

// layoutTabs lays an ordered list of tabs — each with its panes and optional
// startup commands — into an existing workspace whose root tab and root pane are
// rootTab and rootPane, and whose own directory is root. tab[0] reuses the root
// tab (renamed) and root pane; each later tab is created without focus so the
// first stays in front while the rest spin up. Within a tab the first pane is the
// tab's root and each later pane is split off the previous one — or off the
// earlier pane its split_from names, which is how a full-height edge pane is
// kept whole. Every startup command is run last, once all panes exist, paced to
// its freshly spawned shell.
//
// root anchors the optional per-tab and per-pane working_dir: a relative one is
// resolved against it, and a tab or pane that declares none simply inherits it.
func layoutTabs(client *herdrClient, ws, rootTab, rootPane, root string, tabs []ProjectTab) error {
	// pendingRun pairs a pane with the command it should run once all panes exist.
	type pendingRun struct {
		pane    string
		command string
	}
	var runs []pendingRun
	var err error

	// Resolve every pane's directory before touching the workspace. A bad path
	// found halfway through would otherwise leave a half-built workspace behind,
	// which is worse than not starting. dirs[i][j] is tab i pane j's directory.
	dirs, err := resolvePaneDirs(root, tabs)
	if err != nil {
		return err
	}

	// Same reasoning for split targets: resolve every one up front so a layout
	// that cannot be built is refused before the first native call, not halfway
	// through. targets[i][j] is the index of the pane tab i pane j splits off.
	targets, err := resolveSplitTargets(tabs)
	if err != nil {
		return err
	}

	for i, t := range tabs {
		tabRoot := rootPane
		if i == 0 {
			// The root tab already exists at the workspace's directory. When this tab
			// wants a different one there is nothing to change it with — herdr has no
			// "set a tab's cwd" call — so the tab is rebuilt where it belongs and the
			// original closed, leaving the new one first in the workspace.
			if dir := dirs[i][0]; dir != "" && dir != root {
				_, tabRoot, err = client.tabCreate(ws, t.Name, dir, true)
				if err != nil {
					return fmt.Errorf("create tab %q: %w", t.Name, err)
				}
				if err = client.tabClose(rootTab); err != nil {
					return fmt.Errorf("close the replaced root tab: %w", err)
				}
			} else if err = client.tabRename(rootTab, t.Name); err != nil {
				return fmt.Errorf("rename root tab: %w", err)
			}
		} else {
			_, tabRoot, err = client.tabCreate(ws, t.Name, dirs[i][0], false)
			if err != nil {
				return fmt.Errorf("create tab %q: %w", t.Name, err)
			}
		}

		// panes[j] is the native id of this tab's pane j, so a later pane can be
		// split off any earlier one rather than only the pane before it. It is
		// per-tab: a pane index never reaches into another tab's panes.
		panes := make([]string, len(t.effectivePanes()))
		for j, pane := range t.effectivePanes() {
			paneID := tabRoot
			if j > 0 {
				// tabRoot may not be the workspace's original root pane — a first tab
				// with its own working_dir is rebuilt above — so targets are read from
				// the ids this invocation actually created.
				target := panes[targets[i][j]]
				if target == "" {
					return fmt.Errorf("tab %q pane %d: split target pane %d was not created", t.Name, j+1, targets[i][j]+1)
				}
				paneID, err = client.paneSplit(target, pane.Split, pane.splitRatio(), dirs[i][j], false)
				if err != nil {
					return fmt.Errorf("split pane %d in tab %q: %w", j+1, t.Name, err)
				}
			}
			panes[j] = paneID
			if lbl := strings.TrimSpace(pane.Label); lbl != "" {
				if err := client.paneRename(paneID, lbl); err != nil {
					// Labeling is cosmetic — warn but keep building the workspace.
					fmt.Fprintf(os.Stderr, "herdr-plus: failed to label pane %d in tab %q: %v\n", j+1, t.Name, err)
				}
			}
			if strings.TrimSpace(pane.Command) != "" {
				runs = append(runs, pendingRun{pane: paneID, command: pane.Command})
			}
		}
	}

	// Run each tab's startup command. runCommand paces itself to each freshly
	// spawned shell — waiting for its prompt, typing the command, then submitting
	// with a real Enter key — so the apps (claude, lazygit, …) actually start
	// instead of sitting unsubmitted at the prompt for the user to press Enter.
	for _, r := range runs {
		if err := client.runCommand(r.pane, r.command); err != nil {
			return fmt.Errorf("run command in pane %s: %w", r.pane, err)
		}
	}
	return nil
}
