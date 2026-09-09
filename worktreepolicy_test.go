package main

import (
	"encoding/json"
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

func TestPolicyExplicitIssueCannotReuseAnUnrelatedExactName(t *testing.T) {
	for _, name := range []string{"cleanup", "ingwon/ic-1770-other"} {
		t.Run(name, func(t *testing.T) {
			repo, _ := policyFixture(t)
			runGit(t, repo, "branch", name)
			plan, err := planWorktree(worktreeRequest{Cwd: repo, Name: name, Issue: "IC-177"})
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Candidates) != 1 || plan.Candidates[0].Existing || !matchesIssue(plan.Candidates[0].Branch, "IC-177") {
				t.Fatalf("explicit issue selected an unrelated branch: %+v", plan)
			}
			runGit(t, repo, "branch", "team/ic-177-existing")
			plan, err = planWorktree(worktreeRequest{Cwd: repo, Name: name, Issue: "IC-177"})
			if err != nil || len(plan.Candidates) != 1 || plan.Candidates[0].Branch != "team/ic-177-existing" {
				t.Fatalf("whole-issue branch must be the only existing choice: %+v %v", plan, err)
			}
		})
	}
}

func TestPolicyProjectsRouteBySelectedRepository(t *testing.T) {
	repo, _ := policyFixture(t)
	linked := addWorktree(t, repo, "linked")
	other := newGitRepo(t)
	bare := t.TempDir()
	runGit(t, bare, "init", "--bare")
	cfg, err := loadPluginConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		cwd    string
		shared bool
	}{{repo, true}, {linked, true}, {other, false}, {bare, false}} {
		shared, err := projectUsesSharedWorktreePolicy(cfg, item.cwd)
		if err != nil || shared != item.shared {
			t.Fatalf("route for %s = %v, %v; want shared=%v", item.cwd, shared, err, item.shared)
		}
	}
	duplicate := cfg
	duplicate.Worktree.Projects = append(append([]WorktreePolicy{}, cfg.Worktree.Projects...), cfg.Worktree.Projects[0])
	if shared, err := projectUsesSharedWorktreePolicy(duplicate, repo); err == nil || shared {
		t.Fatal("duplicate policy must refuse, not choose a route")
	}
	malformed := cfg
	malformed.Worktree.Projects = append([]WorktreePolicy{}, cfg.Worktree.Projects...)
	malformed.Worktree.Projects[0].Root = ""
	if shared, err := projectUsesSharedWorktreePolicy(malformed, repo); err == nil || shared {
		t.Fatal("malformed matching policy must not fall back to legacy")
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
		return map[string]any{"workspace": map[string]any{"workspace_id": "wchild"}, "root_pane": map[string]any{"pane_id": "wchild:p1", "workspace_id": "wchild"}, "worktree": map[string]any{"branch": "legacy/IC-72-kept", "path": old}, "already_open": true}, nil
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

// selectionResult is the native reply for a delegated worktree action, shaped
// the way herdr 0.9 sends one: a workspace, and a root pane that names it.
func selectionResult(branch, path string) map[string]any {
	return map[string]any{
		"workspace": map[string]any{"workspace_id": "wchild"},
		"root_pane": map[string]any{"pane_id": "wchild:p1", "workspace_id": "wchild"},
		"worktree":  map[string]any{"branch": branch, "path": path},
	}
}

// The accepted candidate names one registered checkout. Between the plan the
// user confirmed and the Git read inside apply, that registration can move —
// and opening whatever Git now points at would silently substitute a checkout
// for the one on screen.
func TestPolicyApplyRefusesMovedRegisteredCheckout(t *testing.T) {
	repo, _ := policyFixture(t)
	old := addWorktree(t, repo, "legacy/IC-72-kept")
	req := worktreeRequest{Cwd: repo, Name: "new title", Issue: "IC-72"}
	plan, err := planWorktree(req)
	if err != nil {
		t.Fatal(err)
	}
	req.Fingerprint, req.Candidate = plan.Fingerprint, plan.Candidates[0].ID
	if !plan.Candidates[0].Checkout || plan.Candidates[0].Path != old {
		t.Fatalf("fixture must select the registered checkout: %+v", plan.Candidates)
	}
	moved := filepath.Join(t.TempDir(), "moved")
	f := startFakeHerdr(t)
	f.handleFunc("worktree.list", func(request) (any, *herdrError) {
		runGit(t, repo, "worktree", "move", old, moved)
		return map[string]any{"worktrees": []any{listedCheckout(repo, "wparent")}}, nil
	})
	f.handle("workspace.get", map[string]any{"workspace": wsInfo("wparent", "Primary", repo, false)})
	f.handle("worktree.open", selectionResult(plan.Candidates[0].Branch, moved))
	out, err := applyWorktreePlan(req, plan)
	if err == nil {
		t.Fatalf("accepted a checkout that moved to %s: %s", moved, out)
	}
	if !strings.Contains(err.Error(), old) {
		t.Fatalf("refusal does not name the selected checkout: %v", err)
	}
	f.assertNotCalled("worktree.open")
	f.assertNotCalled("worktree.create")
}

// A new branch is created from the commit the plan resolved, not from wherever
// origin/BASE has moved to by the time the fetch runs.
func TestPolicyApplyRefusesAdvancedRemoteBase(t *testing.T) {
	repo, origin := policyFixture(t)
	req := worktreeRequest{Cwd: repo, Name: "new task"}
	plan, err := planWorktree(req)
	if err != nil {
		t.Fatal(err)
	}
	req.Fingerprint, req.Candidate = plan.Fingerprint, plan.Candidates[0].ID
	if plan.BaseOID == "" || plan.Candidates[0].Existing {
		t.Fatalf("fixture must plan a new branch from a resolved base: %+v", plan)
	}
	f := startFakeHerdr(t)
	f.handleFunc("worktree.list", func(request) (any, *herdrError) {
		runGit(t, origin, "commit", "--allow-empty", "-m", "advance after the accepted plan")
		return map[string]any{"worktrees": []any{listedCheckout(repo, "wparent")}}, nil
	})
	f.handle("workspace.get", map[string]any{"workspace": wsInfo("wparent", "Primary", repo, false)})
	f.handle("worktree.create", selectionResult(plan.Candidates[0].Branch, plan.Candidates[0].Path))
	out, err := applyWorktreePlan(req, plan)
	if err == nil {
		t.Fatalf("accepted a base that advanced past %s: %s", plan.BaseOID, out)
	}
	if !strings.Contains(err.Error(), plan.BaseOID) {
		t.Fatalf("refusal does not name the accepted base commit: %v", err)
	}
	f.assertNotCalled("worktree.create")
	f.assertNotCalled("worktree.open")
	if got := runGit(t, repo, "for-each-ref", "refs/heads/"+plan.Candidates[0].Branch); got != "" {
		t.Fatalf("refused apply created a local branch: %s", got)
	}
}

// The configured root is a destination, so its identity is the directory it
// physically resolves to. Retargeting a symlink above it must change the
// fingerprint, or a plan accepted for one location applies to another.
func TestPolicyRootSymlinkParticipatesInFingerprint(t *testing.T) {
	repo, _ := policyFixture(t)
	first, second := t.TempDir(), t.TempDir()
	link := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(os.Getenv("HERDR_PLUGIN_CONFIG_DIR"), "config.toml")
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadPluginConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(strings.ReplaceAll(string(data), cfg.Worktree.Projects[0].Root, link)), 0600); err != nil {
		t.Fatal(err)
	}
	req := worktreeRequest{Cwd: repo, Name: "new task"}
	before, err := planWorktree(req)
	if err != nil {
		t.Fatal(err)
	}
	physical, err := filepath.EvalSymlinks(first)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(before.Candidates[0].Path) != physical {
		t.Fatalf("candidate kept the symlinked spelling: %s", before.Candidates[0].Path)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, link); err != nil {
		t.Fatal(err)
	}
	after, err := planWorktree(req)
	if err != nil {
		t.Fatal(err)
	}
	if before.Fingerprint == after.Fingerprint {
		t.Fatalf("root moved from %s to %s but the fingerprint stayed %s", first, second, before.Fingerprint)
	}
	if before.Candidates[0].Path == after.Candidates[0].Path {
		t.Fatalf("retargeted root kept candidate path %s", after.Candidates[0].Path)
	}
	if _, err := os.Stat(filepath.Join(second, filepath.Base(after.Candidates[0].Path))); !os.IsNotExist(err) {
		t.Fatalf("planning created the destination: %v", err)
	}
}

// Projects' ctrl+g and the worktree action are the same planner. Projects must
// hand it the name as typed: prefixing first hides an existing unprefixed
// branch, turning a reuse that needs no remote into a create that does.
func TestPolicyProjectsAndWorktreeAgreeOnUnprefixedBranch(t *testing.T) {
	repo, origin := policyFixture(t)
	runGit(t, repo, "branch", "legacy")
	// An existing branch needs no base query, so an unreachable origin is only
	// fatal to a caller that lost the existing name.
	runGit(t, repo, "remote", "set-url", "origin", filepath.Join(origin, "missing"))

	fromW, err := planWorktree(worktreeRequest{Cwd: repo, Name: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if !fromW.Candidates[0].Existing || fromW.Candidates[0].Branch != "legacy" {
		t.Fatalf("the worktree action lost the existing branch: %+v", fromW.Candidates)
	}

	m := newProjectsModel([]Project{{Name: "fixture", WorkingDir: repo}}, "", "ingwon/")
	view, _ := m.promptWorktreeBranch()
	m = view.(projectsModel)
	m.branchInput.SetValue("legacy")
	view, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = view.(projectsModel)
	if m.rawBranch != "legacy" || m.branch != "ingwon/legacy" {
		t.Fatalf("projects lost the typed name: raw=%q legacy=%q", m.rawBranch, m.branch)
	}
	fromProjects, err := planWorktree(worktreeRequest{Cwd: repo, Name: m.rawBranch})
	if err != nil {
		t.Fatalf("projects could not plan an existing branch with an unreachable origin: %v", err)
	}
	if fromProjects.Candidates[0].ID != fromW.Candidates[0].ID {
		t.Fatalf("same typed name planned differently: worktree=%+v projects=%+v", fromW.Candidates, fromProjects.Candidates)
	}
}

// Herdr routes a workspace-addressed worktree action through that workspace's
// stored repo_root, and groups repositories by repo_key — the canonical Git
// common directory. A record that names another repository, or omits either
// identity, is not evidence that the delegated mutation lands in the repository
// just planned.
func TestPolicyApplyRefusesUnusableParentProvenance(t *testing.T) {
	// key is resolved per-case against the fixture repository; "" omits the
	// field and "KEEP" means the real canonical Git common directory.
	for _, tc := range []struct{ name, repoRoot, key, diagnostic string }{
		{"contradictory repo root", "/repo", "KEEP", "repository root"},
		{"missing repo root", "", "KEEP", "repository root"},
		{"contradictory repo key", "KEEP", "/elsewhere/.git", "repository key"},
		{"missing repo key", "KEEP", "", "repository key"},
		{"repo key names the checkout, not its git dir", "KEEP", "KEEP-ROOT", "repository key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, _ := policyFixture(t)
			runGit(t, repo, "branch", "ingwon/main")
			req := worktreeRequest{Cwd: repo, Name: "main"}
			plan, err := planWorktree(req)
			if err != nil {
				t.Fatal(err)
			}
			req.Fingerprint, req.Candidate = plan.Fingerprint, plan.Candidates[0].ID
			root, key := tc.repoRoot, tc.key
			if root == "KEEP" {
				root = repo
			}
			switch key {
			case "KEEP":
				key = filepath.Join(repo, ".git")
			case "KEEP-ROOT":
				key = repo
			}
			f := startFakeHerdr(t)
			f.handle("worktree.list", map[string]any{"worktrees": []any{listedCheckout(repo, "wparent")}})
			f.handle("workspace.get", map[string]any{"workspace": wsInfoWithProvenance("wparent", "Primary", repo, root, key, false)})
			f.handle("worktree.open", selectionResult(plan.Candidates[0].Branch, repo))
			f.handle("worktree.create", selectionResult(plan.Candidates[0].Branch, plan.Candidates[0].Path))
			if out, err := applyWorktreePlan(req, plan); err == nil {
				t.Fatalf("accepted repo_root %q / repo_key %q against primary %s: %s", root, key, repo, out)
			} else if !strings.Contains(err.Error(), tc.diagnostic) {
				t.Fatalf("refusal does not explain the provenance gap: %v", err)
			}
			f.assertNotCalled("worktree.open")
			f.assertNotCalled("worktree.create")
		})
	}
}

// The success object is passed on whole. A workspace and a root pane that name
// different workspaces are two native objects, not one result, and the caller
// has no basis for treating them as related.
func TestPolicyRefusesUnusableNativeResultIdentities(t *testing.T) {
	for _, tc := range []struct {
		name      string
		workspace map[string]any
		rootPane  map[string]any
	}{
		{"pane owned by another workspace", map[string]any{"workspace_id": "w1"}, map[string]any{"pane_id": "w2:p1", "workspace_id": "w2"}},
		{"pane id disagrees with its own workspace_id", map[string]any{"workspace_id": "w1"}, map[string]any{"pane_id": "w2:p1", "workspace_id": "w1"}},
		{"pane without an owning workspace", map[string]any{"workspace_id": "w1"}, map[string]any{"pane_id": "w1:p1"}},
		{"unusable workspace id", map[string]any{"workspace_id": "w 1\n"}, map[string]any{"pane_id": "w 1\n:p1", "workspace_id": "w 1\n"}},
		{"unusable pane number", map[string]any{"workspace_id": "w1"}, map[string]any{"pane_id": "w1:t1", "workspace_id": "w1"}},
		{"pane id is only the workspace id", map[string]any{"workspace_id": "w1"}, map[string]any{"pane_id": "w1", "workspace_id": "w1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{
				"workspace": tc.workspace,
				"root_pane": tc.rootPane,
				"worktree":  map[string]any{"branch": "ingwon/task", "path": "/tmp/checkout"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := validateEnsureResult(raw, "ingwon/task", "/tmp/checkout"); err == nil {
				t.Fatalf("accepted %s: %s", tc.name, raw)
			}
		})
	}
	valid, err := json.Marshal(map[string]any{
		"workspace":    map[string]any{"workspace_id": "w3V"},
		"root_pane":    map[string]any{"pane_id": "w3V:p1", "workspace_id": "w3V"},
		"worktree":     map[string]any{"branch": "ingwon/task", "path": "/tmp/checkout"},
		"native_extra": "preserved",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateEnsureResult(valid, "ingwon/task", "/tmp/checkout"); err != nil {
		t.Fatalf("refused a consistent native result: %v", err)
	}
}

// A new branch is created from the exact commit the plan showed, handed to
// native as an object id.
//
// Verifying the fetched ref proves what origin held at fetch time; it cannot
// bind what origin/BASE names later. Herdr passes base straight into the
// start-point of `git worktree add -b <branch> <path> <base>`, so sending the
// commit rather than the ref is what makes the accepted base unmovable — here
// the ref is deliberately moved after verification and before the native call.
func TestPolicyApplyCreatesFromTheImmutableAcceptedBase(t *testing.T) {
	repo, origin := policyFixture(t)
	req := worktreeRequest{Cwd: repo, Name: "new task"}
	plan, err := planWorktree(req)
	if err != nil {
		t.Fatal(err)
	}
	req.Fingerprint, req.Candidate = plan.Fingerprint, plan.Candidates[0].ID
	if !isObjectID(plan.BaseOID) || plan.Candidates[0].Existing {
		t.Fatalf("fixture must plan a new branch from a resolved base: %+v", plan)
	}

	f := startFakeHerdr(t)
	f.handle("worktree.list", map[string]any{"worktrees": []any{listedCheckout(repo, "wparent")}})
	// workspace.get runs after the base has been fetched and verified, and
	// before the create is submitted: exactly the window a ref-named base would
	// still be exposed in.
	f.handleFunc("workspace.get", func(request) (any, *herdrError) {
		runGit(t, origin, "commit", "--allow-empty", "-m", "advance after verification")
		runGit(t, repo, "fetch", "origin", "refs/heads/trunk:refs/remotes/origin/trunk")
		return map[string]any{"workspace": wsInfo("wparent", "Primary", repo, false)}, nil
	})
	f.handle("worktree.create", selectionResult(plan.Candidates[0].Branch, plan.Candidates[0].Path))

	if _, err := applyWorktreePlan(req, plan); err != nil {
		t.Fatal(err)
	}
	moved := runGit(t, repo, "rev-parse", "refs/remotes/origin/trunk")
	if moved == plan.BaseOID {
		t.Fatal("fixture must move origin/trunk after verification")
	}
	params := f.paramsFor("worktree.create")
	if params["base"] != plan.BaseOID {
		t.Fatalf("native base was %q, want the accepted commit %s (origin/trunk is now %s)", params["base"], plan.BaseOID, moved)
	}
	if params["branch"] != plan.Candidates[0].Branch || params["path"] != plan.Candidates[0].Path || params["workspace_id"] != "wparent" {
		t.Fatalf("wrong native create target: %v", params)
	}
}
