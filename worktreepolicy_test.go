package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func policyFixture(t *testing.T) (string, string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	repo := newGitRepo(t)
	origin := newGitRepo(t)
	runGit(t, origin, "branch", "-m", "trunk")
	runGit(t, repo, "remote", "add", "origin", origin)
	config := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", config)
	root := filepath.Join(t.TempDir(), "flat worktrees")
	contents := fmt.Sprintf("[worktree]\nbranch_prefix = 'ingwon/'\n[[worktree.projects]]\nname = 'fixture'\nrepository = '%s'\nroot = '%s'\nmax_tail = 29\n", repo, root)
	if err := os.WriteFile(filepath.Join(config, "config.toml"), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return repo, origin
}

func TestPolicyPlansRemoteDefaultWithoutMutating(t *testing.T) {
	repo, _ := policyFixture(t)
	before := runGit(t, repo, "show-ref", "--heads")
	plan, err := planWorktree(worktreeRequest{Cwd: repo, Name: "Fix The Layout's Very Long Description", Issue: "HSYS-4619"})
	if err != nil {
		t.Fatal(err)
	}
	c := plan.Candidates[0]
	if plan.Base != "trunk" || len(plan.BaseOID) != 40 || !strings.HasPrefix(c.Branch, "ingwon/hsys-4619-") || len(strings.TrimPrefix(c.Branch, "ingwon/")) > 29 {
		t.Fatalf("wrong remote/name policy: %+v", plan)
	}
	if filepath.Base(c.Path) != strings.ReplaceAll(c.Branch, "/", "--") {
		t.Fatal(c)
	}
	if _, err := os.Stat(filepath.Dir(c.Path)); !os.IsNotExist(err) {
		t.Fatalf("planning created directories: %v", err)
	}
	if after := runGit(t, repo, "show-ref", "--heads"); before != after {
		t.Fatal("planning changed refs")
	}
	if got := runGit(t, repo, "for-each-ref", "refs/remotes/"); got != "" {
		t.Fatal("planning fetched remote refs")
	}
}

func TestPolicyPreservesWholeIssueCandidatesAndOldPaths(t *testing.T) {
	repo, origin := policyFixture(t)
	old := addWorktree(t, repo, "old/IC-177-original")
	runGit(t, repo, "branch", "another/ic-177-local")
	runGit(t, repo, "branch", "ingwon/ic-1770-other")
	// Existing candidates need neither base discovery nor a reachable remote.
	runGit(t, repo, "remote", "set-url", "origin", filepath.Join(origin, "missing"))
	plan, err := planWorktree(worktreeRequest{Cwd: old, Name: "New title", Issue: "IC-177"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Repository != repo || plan.Base != "" || len(plan.Candidates) != 2 {
		t.Fatalf("wrong candidates: %+v", plan)
	}
	for _, c := range plan.Candidates {
		if !c.Existing || strings.Contains(c.Branch, "1770") {
			t.Fatal(c)
		}
		if c.Checkout && c.Path != old {
			t.Fatalf("renamed existing checkout: %+v", c)
		}
	}
	fromW, err := planWorktree(worktreeRequest{Cwd: old, Name: "ingwon/IC-177 new description"})
	if err != nil || len(fromW.Candidates) != 2 || fromW.Issue != "IC-177" {
		t.Fatalf("W and L issue matching disagree: %+v %v", fromW, err)
	}
}

func TestPolicyDefaultFailuresAndDuplicateConfigRefuse(t *testing.T) {
	repo, _ := policyFixture(t)
	runGit(t, repo, "remote", "remove", "origin")
	if _, err := planWorktree(worktreeRequest{Cwd: repo, Name: "brand new"}); err == nil {
		t.Fatal("missing origin guessed a base")
	}
	path := filepath.Join(os.Getenv("HERDR_PLUGIN_CONFIG_DIR"), "config.toml")
	data, _ := os.ReadFile(path)
	section := string(data[strings.Index(string(data), "[[worktree.projects]]"):])
	if err := os.WriteFile(path, append(data, []byte(section)...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := planWorktree(worktreeRequest{Cwd: repo, Name: "main"}); err == nil || !strings.Contains(err.Error(), "found 2") {
		t.Fatalf("duplicate policy: %v", err)
	}
}

func TestPolicyStalePlanAndCancellationNeverMutate(t *testing.T) {
	repo, _ := policyFixture(t)
	req := worktreeRequest{Cwd: repo, Name: "main"}
	plan, err := planWorktree(req)
	if err != nil {
		t.Fatal(err)
	}
	req.Fingerprint = plan.Fingerprint
	req.Candidate = plan.Candidates[0].ID
	runGit(t, repo, "branch", "another-task")
	fresh, err := planWorktree(req)
	if err != nil {
		t.Fatal(err)
	}
	f := startFakeHerdr(t)
	if _, err := applyWorktreePlan(req, fresh); err == nil {
		t.Fatal("accepted stale candidate inventory")
	}
	req.Fingerprint = fresh.Fingerprint
	req.Candidate = ""
	if _, err := applyWorktreePlan(req, fresh); err == nil {
		t.Fatal("empty choice was treated as consent")
	}
	if len(f.methods()) != 0 {
		t.Fatal("stale/cancelled plan reached native IPC")
	}
}

func TestPolicyApplyUsesSharedEnsureAndExplicitParent(t *testing.T) {
	repo, _ := policyFixture(t)
	old := addWorktree(t, repo, "legacy/IC-72-kept")
	req := worktreeRequest{Cwd: old, Name: "new title", Issue: "IC-72"}
	plan, err := planWorktree(req)
	if err != nil {
		t.Fatal(err)
	}
	req.Fingerprint = plan.Fingerprint
	req.Candidate = plan.Candidates[0].ID
	f := startFakeHerdr(t)
	f.handle("worktree.list", map[string]any{"worktrees": []any{listedCheckout(repo, "wparent"), listedCheckout(old, "wchild")}})
	f.handle("workspace.get", map[string]any{"workspace": wsInfo("wparent", "Primary", repo, false)})
	f.handleFunc("worktree.open", func(req request) (any, *herdrError) {
		if req.Params["workspace_id"] != "wparent" || req.Params["cwd"] != nil || req.Params["branch"] != "legacy/IC-72-kept" {
			t.Errorf("wrong shared native target: %v", req.Params)
		}
		return map[string]any{"workspace": map[string]any{"workspace_id": "wchild"}, "root_pane": map[string]any{"pane_id": "wchild:p1"}, "worktree": map[string]any{"branch": "legacy/IC-72-kept", "path": old}, "already_open": true}, nil
	})
	if _, err := applyWorktreePlan(req, plan); err != nil {
		t.Fatal(err)
	}
	f.assertNotCalled("worktree.create")
	f.assertNotCalled("workspace.create")
}

func TestPolicyNamesAndRequestBoundaries(t *testing.T) {
	for _, issue := range []string{"IC-1770", "IC-17", "XIC-177"} {
		if matchesIssue("ingwon/ic-177-description", issue) {
			t.Fatal("partial issue token matched", issue)
		}
	}
	if _, err := policyBranch("ingwon/", "x", "HSYS-4619", 5); err == nil {
		t.Fatal("truncated issue token")
	}
	for _, args := range [][]string{{"--cwd", "relative", "--name", "x"}, {"--cwd", "/tmp", "--cwd", "/else", "--name", "x"}, {"--cwd", "/tmp", "--name", "x", "--candidate", "unexpected"}} {
		if _, err := parseWorktreeRequest(args, false); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestPolicyInvocationNeverUsesAnotherClientsFocus(t *testing.T) {
	dir := t.TempDir()
	f := startFakeHerdr(t)
	f.handle("pane.get", map[string]any{"pane": map[string]any{"pane_id": "w1:p2", "workspace_id": "w1", "tab_id": "w1:t1", "cwd": dir}})
	pc := pluginContext{WorkspaceID: "w1", FocusedPaneID: "w1:p2", FocusedPaneCwd: dir}
	ctx, err := policyInvocation(f.client(), pc)
	if err != nil || ctx.WorkDir != dir || ctx.PaneId != "w1:p2" {
		t.Fatalf("explicit context: %+v %v", ctx, err)
	}
	pc.WorkspaceID = "w-other"
	if _, err := policyInvocation(f.client(), pc); err == nil {
		t.Fatal("accepted moved pane")
	}
	if _, err := policyInvocation(f.client(), pluginContext{}); err == nil {
		t.Fatal("empty context fell back to focus")
	}
	f.assertNotCalled("pane.list")
	f.assertNoMutations()
}

func TestPolicyPickerKeepsFilterAndDoesNotCancelSubmittedWork(t *testing.T) {
	m := newWorktreeUI(worktreeRequest{Cwd: "/tmp"})
	updated, _ := m.Update(worktreePlanned{plan: worktreePlan{Candidates: []worktreeChoice{
		{Branch: "ingwon/alpha", Path: "/tmp/alpha"}, {Branch: "ingwon/beta", Path: "/tmp/beta"},
	}}})
	m = updated.(worktreeUI)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("beta")})
	m = updated.(worktreeUI)
	if m.list.selectedIndex() != 1 {
		t.Fatalf("filter selected original index %d", m.list.selectedIndex())
	}
	m.busy = true
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Fatal("picker exited while native operation was outstanding")
	}
	m.busy = false
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("idle picker could not cancel")
	}
}
