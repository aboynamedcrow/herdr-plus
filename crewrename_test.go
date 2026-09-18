package main

import (
	"fmt"
	"os"
	"testing"
)

// Captured through a plugin hook on Herdr 0.9.0 on 2026-09-18.
// The fixture uses a disposable shell workspace with no provider session.
func TestCrewRenameCapturedNativeEvent(t *testing.T) {
	f := startFakeHerdr(t)
	f.handle("workspace.get", map[string]any{"workspace": workspaceInfo{WorkspaceID: "w68", Label: "Rename fixture after"}})
	data, err := os.ReadFile("testdata/workspace-renamed.json")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_PLUGIN_EVENT_JSON", string(data))
	t.Setenv("HERDR_WORKSPACE_ID", "w99")
	runOnWorkspaceRename()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 1 || f.calls[0].Method != "workspace.get" || f.calls[0].Params["workspace_id"] != "w68" {
		t.Fatalf("rename used caller context instead of event: %+v", f.calls)
	}
}

func TestCrewRenameRecoversPartialUpdates(t *testing.T) {
	for _, failure := range []string{"pane.rename", "workspace.report_metadata"} {
		t.Run(failure, func(t *testing.T) {
			f := startFakeHerdr(t)
			task, recorded, tab, pane := "B", "A", "Crew · A", "Orchestrator · A"
			fail := true
			f.handleFunc("workspace.get", func(r request) (any, *herdrError) {
				return map[string]any{"workspace": workspaceInfo{WorkspaceID: "w1", Label: task, Tokens: map[string]string{
					"crew_task": recorded, "crew_crew_tab": "w1:t1", "crew_orchestrator_pane": "w1:p1"}}}, nil
			})
			f.handleFunc("tab.list", func(r request) (any, *herdrError) {
				return map[string]any{"tabs": []tabInfo{{TabID: "w1:t1", WorkspaceID: "w1", Label: tab}}}, nil
			})
			f.handleFunc("tab.get", func(r request) (any, *herdrError) {
				return map[string]any{"tab": tabInfo{TabID: "w1:t1", WorkspaceID: "w1", Label: tab}}, nil
			})
			f.handleFunc("pane.list", func(r request) (any, *herdrError) {
				return map[string]any{"panes": []paneInfo{{PaneID: "w1:p1", TabID: "w1:t1", WorkspaceID: "w1", Label: pane}}}, nil
			})
			f.handleFunc("pane.get", func(r request) (any, *herdrError) {
				return map[string]any{"pane": paneInfo{PaneID: "w1:p1", TabID: "w1:t1", WorkspaceID: "w1", Label: pane}}, nil
			})
			f.handleFunc("tab.rename", func(r request) (any, *herdrError) { tab = fmt.Sprint(r.Params["label"]); return map[string]any{}, nil })
			f.handleFunc("pane.rename", func(r request) (any, *herdrError) {
				if fail && failure == r.Method {
					return nil, &herdrError{Code: "injected", Message: "test failure"}
				}
				pane = fmt.Sprint(r.Params["label"])
				return map[string]any{}, nil
			})
			f.handleFunc("workspace.report_metadata", func(r request) (any, *herdrError) {
				if fail && failure == r.Method {
					return nil, &herdrError{Code: "injected", Message: "test failure"}
				}
				recorded = r.Params["tokens"].(map[string]any)["crew_task"].(string)
				return map[string]any{}, nil
			})
			c, err := newHerdrClient()
			if err != nil {
				t.Fatal(err)
			}
			if err = syncCrewName(c, "w1"); err == nil {
				t.Fatal("expected injected failure")
			}
			fail = false
			for _, next := range []string{"C", "D"} {
				task = next
				if err = syncCrewName(c, "w1"); err != nil {
					t.Fatal(err)
				}
				if tab != "Crew · "+next || pane != "Orchestrator · "+next || recorded != next {
					t.Fatalf("partial rename stayed behind: tab=%q pane=%q recorded=%q", tab, pane, recorded)
				}
			}
			// A later manual label must not become owned by the recovery journal.
			tab = "My tab"
			task = "E"
			if err = syncCrewName(c, "w1"); err != nil {
				t.Fatal(err)
			}
			if tab != "My tab" {
				t.Fatal("custom label replaced")
			}
		})
	}
}

func TestCrewRenamePreservesCustomAndOtherTaskLabels(t *testing.T) {
	f := startFakeHerdr(t)
	tokens := map[string]string{"crew_task": "old", "crew_crew_tab": "w1:t1", "crew_orchestrator_pane": "w1:p1"}
	f.handle("workspace.get", map[string]any{"workspace": workspaceInfo{WorkspaceID: "w1", Label: "new", Tokens: tokens}})
	f.handle("tab.list", map[string]any{"tabs": []tabInfo{{TabID: "w1:t1", WorkspaceID: "w1", Label: "Crew · old"}, {TabID: "w1:t2", WorkspaceID: "w1", Label: "Custom"}}})
	f.handle("pane.list", map[string]any{"panes": []paneInfo{
		{PaneID: "w1:p1", TabID: "w1:t1", WorkspaceID: "w1", Label: "Orchestrator · old"},
		{PaneID: "w1:p2", TabID: "w1:t2", WorkspaceID: "w1", Label: "Reviewer 1 · other"},
		{PaneID: "w1:p3", TabID: "w1:t2", WorkspaceID: "w1", Label: "Reviewer 2 · old", Tokens: map[string]string{"crew_worker_role": "Reviewer", "crew_role_number": "2"}},
	}})
	f.handle("tab.get", map[string]any{"tab": tabInfo{TabID: "w1:t1", WorkspaceID: "w1", Label: "Crew · old"}})
	f.handlers["pane.get"] = func(r request) (any, *herdrError) {
		id := r.Params["pane_id"].(string)
		label := "Orchestrator · old"
		if id == "w1:p3" {
			label = "Reviewer 2 · old"
		}
		return map[string]any{"pane": paneInfo{PaneID: id, WorkspaceID: "w1", Label: label}}, nil
	}
	f.handle("tab.rename", map[string]any{})
	f.handle("pane.rename", map[string]any{})
	f.handle("workspace.report_metadata", map[string]any{})
	c, err := newHerdrClient()
	if err != nil {
		t.Fatal(err)
	}
	if err := syncCrewName(c, "w1"); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.calls {
		counts[r.Method]++
		if r.Method == "pane.rename" && r.Params["pane_id"] == "w1:p2" {
			t.Fatal("renamed another task")
		}
	}
	if counts["tab.rename"] != 1 || counts["pane.rename"] != 2 || counts["workspace.report_metadata"] != 1 {
		t.Fatalf("calls: %+v", counts)
	}
}

func TestCrewRenameSkipsPlainWorkspace(t *testing.T) {
	f := startFakeHerdr(t)
	f.handle("workspace.get", map[string]any{"workspace": workspaceInfo{WorkspaceID: "w1", Label: "new"}})
	c, err := newHerdrClient()
	if err != nil {
		t.Fatal(err)
	}
	if err := syncCrewName(c, "w1"); err != nil {
		t.Fatal(err)
	}
	f.assertNotCalled("tab.list")
	f.assertNotCalled("pane.list")
	f.assertNotCalled("workspace.report_metadata")
}

func TestCrewRenameUsesCurrentNameAfterQueuedEvent(t *testing.T) {
	f := startFakeHerdr(t)
	f.handle("workspace.get", map[string]any{"workspace": workspaceInfo{WorkspaceID: "w1", Label: "latest", Tokens: map[string]string{"crew_task": "old"}}})
	f.handle("tab.list", map[string]any{"tabs": []tabInfo{}})
	f.handle("pane.list", map[string]any{"panes": []paneInfo{}})
	f.handle("workspace.report_metadata", map[string]any{})
	c, err := newHerdrClient()
	if err != nil {
		t.Fatal(err)
	}
	if err := syncCrewName(c, "w1"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	last := f.calls[len(f.calls)-1]
	if last.Params["tokens"].(map[string]any)["crew_task"] != "latest" {
		t.Fatalf("stale task: %+v", last)
	}
}
