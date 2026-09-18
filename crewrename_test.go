package main

import "testing"

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
