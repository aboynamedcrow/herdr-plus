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

// Older task starts named the Crew tab from the raw ask, and dispatch names
// seat panes and the Workers and Archive tabs without role tokens.
func TestCrewRenameMatchesLabelsByIssue(t *testing.T) {
	f := startFakeHerdr(t)
	task := "ENG-5663 🧌 increase-offer-generation"
	tokens := map[string]string{"crew_task": task, "crew_crew_tab": "w1:t1", "crew_orchestrator_pane": "w1:p1", "crew_issue_pane": "w1:p2"}
	f.handle("workspace.get", map[string]any{"workspace": workspaceInfo{WorkspaceID: "w1", Label: task, Tokens: tokens}})
	tabs := map[string]string{
		"w1:t1": "Crew · ENG-5663 --see-above. can we look into offer generation (proactive",
		"w1:t2": "Workers · ENG-5663 🧌 see-above-can-we-look-into",
		"w1:t3": "Archive · ENG-5663 🧌 see-above-can-we-look-into",
		"w1:t4": "Notes · ENG-5663 my own tab",
		"w1:t5": "Workers · ENG-9 🧌 another-issue",
	}
	panes := map[string]string{
		"w1:p1": "Orchestrator · ENG-5663 --see-above. can we look into offer generation (proactive",
		"w1:p2": "Issue / Utility · ENG-5663",
		"w1:p4": "Implementer 1 · ENG-5663 🧌 see-above-can-we-look-into · codex gpt-6-sol high",
		"w1:p5": "Reviewer 12 · ENG-5663 🧌 see-above-can-we-look-into · claude claude-fable-5-1 high",
		"w1:p6": "Reviewer 1 · ENG-9 🧌 another-issue · claude claude-fable-5-1 high",
		"w1:p7": "Scratch · ENG-5663 notes",
	}
	tabOrder := []string{"w1:t1", "w1:t2", "w1:t3", "w1:t4", "w1:t5"}
	paneOrder := []string{"w1:p1", "w1:p2", "w1:p4", "w1:p5", "w1:p6", "w1:p7"}
	f.handleFunc("tab.list", func(r request) (any, *herdrError) {
		list := []tabInfo{}
		for _, id := range tabOrder {
			list = append(list, tabInfo{TabID: id, WorkspaceID: "w1", Label: tabs[id]})
		}
		return map[string]any{"tabs": list}, nil
	})
	f.handleFunc("tab.get", func(r request) (any, *herdrError) {
		id := r.Params["tab_id"].(string)
		return map[string]any{"tab": tabInfo{TabID: id, WorkspaceID: "w1", Label: tabs[id]}}, nil
	})
	f.handleFunc("pane.list", func(r request) (any, *herdrError) {
		list := []paneInfo{}
		for _, id := range paneOrder {
			list = append(list, paneInfo{PaneID: id, TabID: "w1:t1", WorkspaceID: "w1", Label: panes[id]})
		}
		return map[string]any{"panes": list}, nil
	})
	f.handleFunc("pane.get", func(r request) (any, *herdrError) {
		id := r.Params["pane_id"].(string)
		return map[string]any{"pane": paneInfo{PaneID: id, TabID: "w1:t1", WorkspaceID: "w1", Label: panes[id]}}, nil
	})
	f.handleFunc("tab.rename", func(r request) (any, *herdrError) {
		tabs[r.Params["tab_id"].(string)] = fmt.Sprint(r.Params["label"])
		return map[string]any{}, nil
	})
	f.handleFunc("pane.rename", func(r request) (any, *herdrError) {
		panes[r.Params["pane_id"].(string)] = fmt.Sprint(r.Params["label"])
		return map[string]any{}, nil
	})
	f.handle("workspace.report_metadata", map[string]any{})
	c, err := newHerdrClient()
	if err != nil {
		t.Fatal(err)
	}
	// The workspace label already equals crew_task. A rename event to the same name still repairs the labels.
	if err := syncCrewName(c, "w1"); err != nil {
		t.Fatal(err)
	}
	wantTabs := map[string]string{
		"w1:t1": "Crew · " + task,
		"w1:t2": "Workers · " + task,
		"w1:t3": "Archive · " + task,
		"w1:t4": "Notes · ENG-5663 my own tab",
		"w1:t5": "Workers · ENG-9 🧌 another-issue",
	}
	wantPanes := map[string]string{
		"w1:p1": "Orchestrator · " + task,
		"w1:p2": "Issue / Utility · ENG-5663",
		"w1:p4": "Implementer 1 · " + task + " · codex gpt-6-sol high",
		"w1:p5": "Reviewer 12 · " + task + " · claude claude-fable-5-1 high",
		"w1:p6": "Reviewer 1 · ENG-9 🧌 another-issue · claude claude-fable-5-1 high",
		"w1:p7": "Scratch · ENG-5663 notes",
	}
	for id, want := range wantTabs {
		if tabs[id] != want {
			t.Errorf("tab %s = %q, want %q", id, tabs[id], want)
		}
	}
	for id, want := range wantPanes {
		if panes[id] != want {
			t.Errorf("pane %s = %q, want %q", id, panes[id], want)
		}
	}
}

// Herdr stores crew_task cut at 80 characters, so a label made from a longer
// name never equals it.
func TestCrewRenameMatchesLabelsPastTheTokenCap(t *testing.T) {
	f := startFakeHerdr(t)
	long := "ENG-5608 🧌 review-the-ta-services-session-on-stamping-stop-arrivals-from-a-driver-s-call-in"
	recorded := string([]rune(long)[:80])
	task := "ENG-5608 🧌 ta-services-session-on-stamping-stop-arrivals-review"
	tokens := map[string]string{"crew_task": recorded, "crew_crew_tab": "w1:t1", "crew_memory_pane": "w1:p1"}
	f.handle("workspace.get", map[string]any{"workspace": workspaceInfo{WorkspaceID: "w1", Label: task, Tokens: tokens}})
	tab, pane := "Crew · "+long, "Memory · "+long
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
	f.handleFunc("pane.rename", func(r request) (any, *herdrError) { pane = fmt.Sprint(r.Params["label"]); return map[string]any{}, nil })
	f.handle("workspace.report_metadata", map[string]any{})
	c, err := newHerdrClient()
	if err != nil {
		t.Fatal(err)
	}
	if err := syncCrewName(c, "w1"); err != nil {
		t.Fatal(err)
	}
	if tab != "Crew · "+task || pane != "Memory · "+task {
		t.Fatalf("tab=%q pane=%q", tab, pane)
	}
}
