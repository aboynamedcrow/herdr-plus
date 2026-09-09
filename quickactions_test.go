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
	if got, ok := argValue(args, "--target-pane"); !ok || got != "w1:p2" {
		t.Fatalf("picker was not placed over the invoking pane: %v", args)
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
