//
// Date: 2026-08-27
// Author: Spicer Matthews (spicer@cloudmanic.com)
// Copyright: 2026 Cloudmanic Labs, LLC. All rights reserved.
//

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// quickActionsInvoker wires a verifiable invoking pane: the action context herdr
// injects, and a fake herdr that answers pane.get with the matching pane. It
// returns the fake and the invoking directory.
func quickActionsInvoker(t *testing.T) (*fakeHerdr, string) {
	t.Helper()
	dir := t.TempDir()
	f := startFakeHerdr(t)
	f.handle("pane.get", map[string]any{"pane": map[string]any{
		"pane_id": "w1:p2", "workspace_id": "w1", "tab_id": "w1:t1", "cwd": dir,
	}})
	setPluginContext(t, map[string]any{
		"workspace_id":       "w1",
		"workspace_label":    "Repo",
		"workspace_cwd":      "/somewhere/else",
		"tab_id":             "w1:t1",
		"tab_label":          "Task",
		"focused_pane_id":    "w1:p2",
		"focused_pane_cwd":   dir,
		"focused_pane_agent": "claude",
	})
	return f, dir
}

// argValue returns the value that follows flag in the recorded argv.
func argValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// TestLaunchQuickActionsPlacement mirrors TestLaunchProjectsPlacement for the
// quick-actions picker, whose built-in default is overlay rather than zoomed.
func TestLaunchQuickActionsPlacement(t *testing.T) {
	cases := []struct {
		name       string
		configToml string
		want       string
	}{
		{"no config defaults to overlay", "", "overlay"},
		{"valid override is passed through", "[quick_actions]\nplacement = \"popup\"\n", "popup"},
		{"invalid value falls back to overlay", "[quick_actions]\nplacement = \"bogus\"\n", "overlay"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			quickActionsInvoker(t)
			configDir := t.TempDir()
			t.Setenv("HERDR_PLUGIN_CONFIG_DIR", configDir)
			if c.configToml != "" {
				if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(c.configToml), 0o644); err != nil {
					t.Fatalf("write config: %v", err)
				}
			}

			record := filepath.Join(t.TempDir(), "args.txt")
			t.Setenv("HERDR_BIN_PATH", writeArgRecordingHerdr(t, record))

			launchQuickActions()

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

// The Quick Actions picker is placed over the pane the action fired from, and
// the chosen command runs in that pane's directory. Both have to come from the
// explicit action context, verified against herdr — not from whatever pane holds
// global focus by the time herdr runs this server-side action.
func TestLaunchQuickActionsTargetsTheVerifiedInvokingPane(t *testing.T) {
	f, dir := quickActionsInvoker(t)
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	record := filepath.Join(t.TempDir(), "args.txt")
	t.Setenv("HERDR_BIN_PATH", writeArgRecordingHerdr(t, record))

	launchQuickActions()

	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read recorded args: %v", err)
	}
	args := strings.Split(string(raw), "\n")
	// The default placement is overlay, which herdr refuses to combine with an
	// explicit target pane, so the launch must not ask for one.
	if got, ok := argValue(args, "--target-pane"); ok {
		t.Fatalf("overlay launch named a target pane %q; herdr rejects that: %v", got, args)
	}
	if _, ok := argValue(args, "--cwd"); ok {
		t.Fatalf("--cwd would break the plugin executable's relative command: %v", args)
	}

	// The launch directory still reaches the picker, and the commands it runs,
	// through the encoded context — not through the pane's own working directory.
	encoded, ok := argValue(args, "--env")
	if !ok || !strings.HasPrefix(encoded, "HERDR_PLUS_CTX=") {
		t.Fatalf("no encoded context was handed to the picker: %v", args)
	}
	ctx, err := decodeRunContext(strings.TrimPrefix(encoded, "HERDR_PLUS_CTX="))
	if err != nil {
		t.Fatal(err)
	}
	if ctx.WorkDir != dir {
		t.Fatalf("carried working directory %q, want the invoking pane's %q", ctx.WorkDir, dir)
	}
	// Everything herdr supplied is still carried; the guard adds proof, not loss.
	if ctx.PaneId != "w1:p2" || ctx.TabId != "w1:t1" || ctx.TabLabel != "Task" || ctx.WorkspaceId != "w1" || ctx.WorkspaceLabel != "Repo" || ctx.Agent != "claude" {
		t.Fatalf("guard dropped context herdr supplied: %+v", ctx)
	}
	f.assertNotCalled("pane.list")
	f.assertNoMutations()
}

// Every way the invoking pane cannot be established must stop before a pane is
// opened, so the picker is never placed over a pane the user is not in.
func TestQuickActionsInvocationRefusesUnprovenPanes(t *testing.T) {
	dir := t.TempDir()
	valid := pluginContext{WorkspaceID: "w1", TabID: "w1:t1", FocusedPaneID: "w1:p2", FocusedPaneCwd: dir}
	for _, tc := range []struct {
		name    string
		context func(pluginContext) pluginContext
		pane    map[string]any
	}{
		{"no invoking workspace", func(pc pluginContext) pluginContext { pc.WorkspaceID = ""; return pc }, nil},
		{"no invoking pane", func(pc pluginContext) pluginContext { pc.FocusedPaneID = ""; return pc }, nil},
		{"no invoking directory", func(pc pluginContext) pluginContext { pc.FocusedPaneCwd = ""; return pc }, nil},
		{"relative invoking directory", func(pc pluginContext) pluginContext { pc.FocusedPaneCwd = "relative"; return pc }, nil},
		{"workspace cwd is not a pane directory", func(pc pluginContext) pluginContext {
			pc.FocusedPaneCwd, pc.WorkspaceCwd = "", dir
			return pc
		}, nil},
		{"pane moved to another workspace", nil, map[string]any{"pane_id": "w1:p2", "workspace_id": "w-other", "tab_id": "w1:t1", "cwd": dir}},
		{"pane answered for another id", nil, map[string]any{"pane_id": "w1:p9", "workspace_id": "w1", "tab_id": "w1:t1", "cwd": dir}},
		{"pane moved to another directory", nil, map[string]any{"pane_id": "w1:p2", "workspace_id": "w1", "tab_id": "w1:t1", "cwd": t.TempDir()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := startFakeHerdr(t)
			if tc.pane != nil {
				f.handle("pane.get", map[string]any{"pane": tc.pane})
			} else {
				f.handle("pane.get", map[string]any{"pane": map[string]any{
					"pane_id": "w1:p2", "workspace_id": "w1", "tab_id": "w1:t1", "cwd": dir,
				}})
			}
			pc := valid
			if tc.context != nil {
				pc = tc.context(valid)
			}
			if ctx, err := quickActionsInvocation(f.client(), pc); err == nil {
				t.Fatalf("accepted an unproven invoking pane: %+v", ctx)
			}
			// Nothing native was changed. That the refusal also stops before the
			// herdr CLI is opened is proved end to end by the quick-actions cases
			// in TestActionFailureIsVisibleAndNotificationIsBounded.
			f.assertNoMutations()
		})
	}
}

// herdr 0.9 refuses `--target-pane` for an overlay or popup plugin pane:
// `invalid_params: overlay and popup plugin panes target the active pane`, with
// no pane created. Naming one broke every launch at the default placement, so
// the flag must appear only for the placements that honor it.
func TestLaunchQuickActionsOnlyTargetsPanesWherePlacementAllowsIt(t *testing.T) {
	for _, tc := range []struct {
		placement string
		wantFlag  bool
	}{
		{"overlay", false},
		{"popup", false},
		{"split", true},
		{"tab", true},
		{"zoomed", true},
	} {
		t.Run(tc.placement, func(t *testing.T) {
			quickActionsInvoker(t)
			configDir := t.TempDir()
			t.Setenv("HERDR_PLUGIN_CONFIG_DIR", configDir)
			config := "[quick_actions]\nplacement = \"" + tc.placement + "\"\n"
			if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0o644); err != nil {
				t.Fatal(err)
			}
			record := filepath.Join(t.TempDir(), "args.txt")
			t.Setenv("HERDR_BIN_PATH", writeArgRecordingHerdr(t, record))

			launchQuickActions()

			raw, err := os.ReadFile(record)
			if err != nil {
				t.Fatalf("read recorded args: %v", err)
			}
			args := strings.Split(string(raw), "\n")
			if !containsPlacement(args, tc.placement) {
				t.Fatalf("placement %q was not passed through: %v", tc.placement, args)
			}
			got, ok := argValue(args, "--target-pane")
			if ok != tc.wantFlag {
				t.Fatalf("placement %q: --target-pane present=%v (%q), want present=%v: %v",
					tc.placement, ok, got, tc.wantFlag, args)
			}
			if tc.wantFlag && got != "w1:p2" {
				t.Fatalf("placement %q targeted %q, want the invoking pane w1:p2", tc.placement, got)
			}
			// Whatever the placement, the launch context still carries the
			// verified invoking pane and its directory: losing the placement
			// flag must not lose the identity behind it.
			encoded, ok := argValue(args, "--env")
			if !ok || !strings.HasPrefix(encoded, "HERDR_PLUS_CTX=") {
				t.Fatalf("no encoded context: %v", args)
			}
			ctx, err := decodeRunContext(strings.TrimPrefix(encoded, "HERDR_PLUS_CTX="))
			if err != nil {
				t.Fatal(err)
			}
			if ctx.PaneId != "w1:p2" || ctx.WorkspaceId != "w1" {
				t.Fatalf("placement %q lost the verified invoking context: %+v", tc.placement, ctx)
			}
		})
	}
}

// The worktree picker is always an overlay, so it must never name a target pane.
func TestPlacementTargetPaneRuleMatchesInstalledHerdr(t *testing.T) {
	for placement, want := range map[string]bool{
		"overlay": false, "popup": false, "split": true, "tab": true, "zoomed": true,
	} {
		if got := placementAcceptsTargetPane(placement); got != want {
			t.Errorf("placementAcceptsTargetPane(%q) = %v, want %v", placement, got, want)
		}
	}
	// Every placement the config accepts has a decided answer here.
	for placement := range validPanePlacements {
		_ = placementAcceptsTargetPane(placement)
	}
}

// The guard compares the pane's foreground working directory, falling back to
// the pane's own. That preference is what makes it the directory a command
// would actually run in, so both halves are pinned here: narrowing the guard to
// pane.cwd would silently compare the wrong directory.
func TestInvokingPaneGuardFollowsTheForegroundDirectory(t *testing.T) {
	foreground := t.TempDir()
	shell := t.TempDir()
	for _, tc := range []struct {
		name       string
		pane       map[string]any
		invokedCwd string
		accept     bool
	}{
		{"foreground directory is the one compared", map[string]any{
			"pane_id": "w1:p2", "workspace_id": "w1", "tab_id": "w1:t1",
			"foreground_cwd": foreground, "cwd": shell,
		}, foreground, true},
		{"the pane's own directory does not stand in for it", map[string]any{
			"pane_id": "w1:p2", "workspace_id": "w1", "tab_id": "w1:t1",
			"foreground_cwd": foreground, "cwd": shell,
		}, shell, false},
		{"absent foreground falls back to the pane directory", map[string]any{
			"pane_id": "w1:p2", "workspace_id": "w1", "tab_id": "w1:t1", "cwd": shell,
		}, shell, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := startFakeHerdr(t)
			f.handle("pane.get", map[string]any{"pane": tc.pane})
			pc := pluginContext{WorkspaceID: "w1", TabID: "w1:t1", FocusedPaneID: "w1:p2", FocusedPaneCwd: tc.invokedCwd}
			ctx, err := quickActionsInvocation(f.client(), pc)
			if tc.accept {
				if err != nil {
					t.Fatalf("refused the verified invoking pane: %v", err)
				}
				if ctx.WorkDir != tc.invokedCwd {
					t.Fatalf("carried %q, want %q", ctx.WorkDir, tc.invokedCwd)
				}
			} else if err == nil {
				t.Fatalf("accepted a directory the foreground process is not in: %+v", ctx)
			}
			f.assertNoMutations()
		})
	}
}
