//
// Date: 2026-07-05
// Author: Spicer Matthews (spicer@cloudmanic.com)
// Copyright: 2026 Cloudmanic Labs, LLC. All rights reserved.
//

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsInsideGitWorkTree confirms the pre-flight check openProjectAsWorktree uses
// before creating a worktree: a plain directory is not a work tree, while a
// git-initialized one is.
func TestIsInsideGitWorkTree(t *testing.T) {
	// A fresh temp dir with no git repo is not inside a work tree.
	plain := t.TempDir()
	if isInsideGitWorkTree(plain) {
		t.Fatalf("expected %s to not be inside a git work tree", plain)
	}

	// A git-initialized dir is. Skip (rather than fail) if git can't init here, so
	// the suite still runs in an environment without a usable git.
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init").CombinedOutput(); err != nil {
		t.Skipf("git init unavailable, skipping: %v (%s)", err, out)
	}
	if !isInsideGitWorkTree(repo) {
		t.Fatalf("expected %s to be inside a git work tree", repo)
	}
}

// writeArgRecordingHerdr writes a fake herdr binary that appends its argv to
// recordPath (one arg per line, invocations separated by a blank line) and
// exits 0. Used to confirm launchProjects/launchQuickActions pass the
// expected --placement value through to `herdr plugin pane open`.
func writeArgRecordingHerdr(t *testing.T, recordPath string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "herdr")
	script := "#!/bin/sh\nfor a in \"$@\"; do echo \"$a\" >> \"" + recordPath + "\"; done\necho >> \"" + recordPath + "\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake herdr: %v", err)
	}
	return path
}

// TestLaunchProjectsPlacement confirms launchProjects defaults to zoomed with
// no config, and passes through a valid [projects].placement override.
func TestLaunchProjectsPlacement(t *testing.T) {
	cases := []struct {
		name       string
		configToml string
		want       string
	}{
		{"no config defaults to zoomed", "", "zoomed"},
		{"valid override is passed through", "[projects]\nplacement = \"popup\"\n", "popup"},
		{"invalid value falls back to zoomed", "[projects]\nplacement = \"bogus\"\n", "zoomed"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			configDir := t.TempDir()
			t.Setenv("HERDR_PLUGIN_CONFIG_DIR", configDir)
			if c.configToml != "" {
				if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(c.configToml), 0o644); err != nil {
					t.Fatalf("write config: %v", err)
				}
			}

			record := filepath.Join(t.TempDir(), "args.txt")
			t.Setenv("HERDR_BIN_PATH", writeArgRecordingHerdr(t, record))

			launchProjects(nil)

			got, err := os.ReadFile(record)
			if err != nil {
				t.Fatalf("read recorded args: %v", err)
			}
			args := strings.Split(string(got), "\n")
			if !containsPlacement(args, c.want) {
				t.Fatalf("recorded args %v do not contain --placement %q", args, c.want)
			}
		})
	}
}

// containsPlacement reports whether args contains "--placement" immediately
// followed by want.
func containsPlacement(args []string, want string) bool {
	for i, a := range args {
		if a == "--placement" && i+1 < len(args) && args[i+1] == want {
			return true
		}
	}
	return false
}

// layoutFixture wires the fake native side for a layoutTabs run: tabs and splits
// hand back fresh ids, and every id handed out is recorded so a test can assert
// which pane each split actually targeted.
type layoutFixture struct {
	*fakeHerdr
	next int
}

func newLayoutFixture(t *testing.T) *layoutFixture {
	t.Helper()
	lf := &layoutFixture{fakeHerdr: startFakeHerdr(t)}
	// Ids say where they came from, so an assertion about a split target reads as
	// "the second tab's root", not as an opaque counter that could coincide.
	lf.handleFunc("tab.create", func(request) (any, *herdrError) {
		lf.next++
		return map[string]any{
			"tab":       map[string]any{"tab_id": fmt.Sprintf("t%d", lf.next)},
			"root_pane": map[string]any{"pane_id": fmt.Sprintf("t%d:root", lf.next)},
		}, nil
	})
	lf.handleFunc("pane.split", func(request) (any, *herdrError) {
		lf.next++
		return map[string]any{"pane": map[string]any{"pane_id": fmt.Sprintf("s%d", lf.next)}}, nil
	})
	lf.handle("tab.rename", map[string]any{})
	lf.handle("tab.close", map[string]any{})
	lf.handle("pane.rename", map[string]any{})
	return lf
}

// splitTargets returns the target_pane_id of each pane.split, in call order.
func (lf *layoutFixture) splitTargets() []string {
	lf.mu.Lock()
	defer lf.mu.Unlock()
	var out []string
	for _, c := range lf.calls {
		if c.Method == "pane.split" {
			out = append(out, fmt.Sprint(c.Params["target_pane_id"]))
		}
	}
	return out
}

// splitParams returns the params of each pane.split, in call order.
func (lf *layoutFixture) splitParams() []map[string]any {
	lf.mu.Lock()
	defer lf.mu.Unlock()
	var out []map[string]any
	for _, c := range lf.calls {
		if c.Method == "pane.split" {
			out = append(out, c.Params)
		}
	}
	return out
}

// TestLayoutTabsSplitFromTargets runs the real layoutTabs against synthetic
// native IPC and checks which pane each split was actually taken from. The shape
// under test is the one plain "split the pane before me" cannot express: two
// full-height edge panes with a worker pair stacked between them.
func TestLayoutTabsSplitFromTargets(t *testing.T) {
	lf := newLayoutFixture(t)
	root := t.TempDir()
	tabs := []ProjectTab{{
		Name: "Crew",
		Panes: []ProjectPane{
			{Label: "Orchestrator"},
			{Label: "Issue", Split: SplitRight, SplitFrom: 1, Ratio: 0.25},
			{Label: "Worker 1", Split: SplitRight, SplitFrom: 1},
			{Label: "Worker 2", Split: SplitDown, SplitFrom: 3},
		},
	}}

	if err := layoutTabs(lf.client(), "w1", "w1:t1", "w1:p1", root, tabs); err != nil {
		t.Fatalf("layoutTabs: %v", err)
	}

	// Panes 2 and 3 both split the root, keeping it full height; pane 4 splits
	// pane 3 — the pane the second split created — not the pane made just before
	// it. That is the arrangement a previous-pane chain cannot produce.
	want := []string{"w1:p1", "w1:p1", "s2"}
	got := lf.splitTargets()
	if len(got) != len(want) {
		t.Fatalf("split targets = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("split %d targeted %q, want %q (all targets: %v)", i+1, got[i], want[i], got)
		}
	}

	// Ratios and directions still ride along untouched.
	params := lf.splitParams()
	if params[0]["direction"] != SplitRight || params[0]["ratio"] != 0.75 {
		t.Fatalf("first split params = %v, want a right split keeping 0.75", params[0])
	}
	if _, ok := params[1]["ratio"]; ok {
		t.Fatalf("second split params = %v, want no ratio key for an even split", params[1])
	}
	if params[2]["direction"] != SplitDown {
		t.Fatalf("third split params = %v, want a down split", params[2])
	}
}

// TestLayoutTabsWithoutSplitFromIsUnchanged pins the existing behavior: with no
// split_from anywhere, every pane still splits the one created before it.
func TestLayoutTabsWithoutSplitFromIsUnchanged(t *testing.T) {
	lf := newLayoutFixture(t)
	root := t.TempDir()
	tabs := []ProjectTab{{
		Name: "work",
		Panes: []ProjectPane{
			{Label: "one"},
			{Label: "two", Split: SplitDown},
			{Label: "three", Split: SplitDown},
		},
	}}

	if err := layoutTabs(lf.client(), "w1", "w1:t1", "w1:p1", root, tabs); err != nil {
		t.Fatalf("layoutTabs: %v", err)
	}
	want := []string{"w1:p1", "s1"}
	got := lf.splitTargets()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("split targets = %v, want %v (the unchanged previous-pane chain)", got, want)
	}
}

// TestLayoutTabsSplitFromIsPerTab confirms a pane index means "in my own tab":
// the second tab's split_from = 1 targets that tab's own root, never a pane from
// the tab before it.
func TestLayoutTabsSplitFromIsPerTab(t *testing.T) {
	lf := newLayoutFixture(t)
	root := t.TempDir()
	tabs := []ProjectTab{
		{Name: "first", Panes: []ProjectPane{{Label: "a"}, {Label: "b", Split: SplitRight, SplitFrom: 1}}},
		{Name: "second", Panes: []ProjectPane{{Label: "c"}, {Label: "d", Split: SplitRight, SplitFrom: 1}}},
	}

	if err := layoutTabs(lf.client(), "w1", "w1:t1", "w1:p1", root, tabs); err != nil {
		t.Fatalf("layoutTabs: %v", err)
	}
	got := lf.splitTargets()
	if len(got) != 2 {
		t.Fatalf("split targets = %v, want two", got)
	}
	if got[0] != "w1:p1" {
		t.Fatalf("first tab split targeted %q, want the workspace root pane", got[0])
	}
	if got[1] != "t2:root" {
		t.Fatalf("second tab split targeted %q, want that tab's own root pane t2:root", got[1])
	}
}

// TestLayoutTabsSplitFromAfterRootRebuild covers the awkward case: when the
// first tab declares its own working_dir the root tab is rebuilt, so pane 1 is a
// different native pane than the workspace's original root. split_from = 1 must
// follow the rebuilt root, not the discarded one.
func TestLayoutTabsSplitFromAfterRootRebuild(t *testing.T) {
	lf := newLayoutFixture(t)
	root := t.TempDir()
	nested := filepath.Join(root, "web")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tabs := []ProjectTab{{
		Name:       "work",
		WorkingDir: "web",
		Panes: []ProjectPane{
			{Label: "a"},
			{Label: "b", Split: SplitRight, SplitFrom: 1},
			{Label: "c", Split: SplitDown, SplitFrom: 1},
		},
	}}

	if err := layoutTabs(lf.client(), "w1", "w1:t1", "w1:p1", root, tabs); err != nil {
		t.Fatalf("layoutTabs: %v", err)
	}
	got := lf.splitTargets()
	for i, target := range got {
		if target == "w1:p1" {
			t.Fatalf("split %d targeted the replaced root pane %q; targets: %v", i+1, target, got)
		}
		if target != "t1:root" {
			t.Fatalf("split %d targeted %q, want the rebuilt tab's root pane t1:root; targets: %v", i+1, target, got)
		}
	}
}

// TestLayoutTabsRejectsBadSplitFromBeforeMutating is the safety property: an
// impossible split target stops the layout before a single native call, so a
// mistyped config never leaves half a workspace behind.
func TestLayoutTabsRejectsBadSplitFromBeforeMutating(t *testing.T) {
	lf := newLayoutFixture(t)
	root := t.TempDir()
	tabs := []ProjectTab{{
		Name:  "work",
		Panes: []ProjectPane{{Label: "a"}, {Label: "b", Split: SplitDown, SplitFrom: 7}},
	}}

	err := layoutTabs(lf.client(), "w1", "w1:t1", "w1:p1", root, tabs)
	if err == nil || !strings.Contains(err.Error(), "split_from") {
		t.Fatalf("want a split_from error, got %v", err)
	}
	if len(lf.methods()) != 0 {
		t.Fatalf("native calls %v; an invalid layout must not touch anything", lf.methods())
	}
}

// TestLayoutTabsRunsCommandsAfterEveryPaneExists confirms the ordering the
// layout has always had: panes are all created first, then startup commands are
// typed — so a command never runs in a workspace that is still being built.
func TestLayoutTabsRunsCommandsAfterEveryPaneExists(t *testing.T) {
	lf := newLayoutFixture(t)
	lf.handleFunc("pane.read", func(request) (any, *herdrError) {
		// A settled pane echoing back whatever was typed, so runCommand's
		// readiness and echo checks resolve immediately instead of timing out.
		return map[string]any{"read": map[string]any{"text": "$ htop lazygit"}}, nil
	})
	lf.handle("pane.send_input", map[string]any{})

	root := t.TempDir()
	tabs := []ProjectTab{{
		Name: "work",
		Panes: []ProjectPane{
			{Label: "a", Command: "htop"},
			{Label: "b", Split: SplitRight, SplitFrom: 1, Command: "lazygit"},
		},
	}}

	if err := layoutTabs(lf.client(), "w1", "w1:t1", "w1:p1", root, tabs); err != nil {
		t.Fatalf("layoutTabs: %v", err)
	}

	lastSplit, firstInput := -1, -1
	for i, m := range lf.methods() {
		if m == "pane.split" {
			lastSplit = i
		}
		if m == "pane.send_input" && firstInput < 0 {
			firstInput = i
		}
	}
	if lastSplit < 0 || firstInput < 0 || firstInput < lastSplit {
		t.Fatalf("calls %v; every pane must exist before the first startup command is typed", lf.methods())
	}
}

// TestLayoutTabsRejectsRootSplitFromBeforeMutating covers the root pane rule at
// the executor: a tab whose first pane declares a split_from is refused before
// any native call. The check has to read the authored config — pane
// normalization clears the root pane's split fields, so validating the
// normalized panes would silently accept it.
func TestLayoutTabsRejectsRootSplitFromBeforeMutating(t *testing.T) {
	lf := newLayoutFixture(t)
	tabs := []ProjectTab{{
		Name:  "work",
		Panes: []ProjectPane{{Label: "a", SplitFrom: 2}, {Label: "b", Split: SplitDown}},
	}}

	err := layoutTabs(lf.client(), "w1", "w1:t1", "w1:p1", t.TempDir(), tabs)
	if err == nil || !strings.Contains(err.Error(), "splits nothing") {
		t.Fatalf("want a root-pane split_from error, got %v", err)
	}
	if len(lf.methods()) != 0 {
		t.Fatalf("native calls %v; an invalid layout must not touch anything", lf.methods())
	}
}
