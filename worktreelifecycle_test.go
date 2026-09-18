package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLifecycleMergedOffersFreshWithoutReset(t *testing.T) {
	repo, _ := policyFixture(t)
	old := addWorktree(t, repo, "ingwon/ic-177-fix")
	oid := runGit(t, old, "rev-parse", "HEAD")
	policyPullRequest = func(string, string) branchPull { return branchPull{State: "MERGED", Head: oid, Base: "trunk"} }
	req := worktreeRequest{Cwd: repo, Name: "fix", Issue: "IC-177"}
	plan, err := planWorktree(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 2 {
		t.Fatalf("missing fresh choice: %+v", plan)
	}
	fresh, kept := plan.Candidates[0], plan.Candidates[1]
	if fresh.Source != "new" || !fresh.Recommended || fresh.Branch != "ingwon/ic-177-fix-2" || fresh.StartCommit != plan.BaseOID {
		t.Fatalf("wrong fresh choice: %+v", fresh)
	}
	if kept.Path != old || kept.Recommended || kept.Action != "inspect merged work" {
		t.Fatalf("old choice changed: %+v", kept)
	}
	if got := runGit(t, old, "rev-parse", "HEAD"); got != oid {
		t.Fatal("planning reset old branch")
	}
	// An edit after planning changes the fingerprint and recommends continuation.
	if err := os.WriteFile(filepath.Join(old, "note"), []byte("new work"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := planWorktree(req)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Fingerprint == plan.Fingerprint || len(changed.Candidates) != 1 || changed.Candidates[0].Action != "continue later work" {
		t.Fatalf("edits did not change recommendation: %+v", changed)
	}
}

func TestLifecycleLaterCommitClosedAndUnknownStayReusable(t *testing.T) {
	for _, state := range []string{"MERGED", "CLOSED", "UNKNOWN", "OPEN"} {
		t.Run(state, func(t *testing.T) {
			repo, _ := policyFixture(t)
			old := addWorktree(t, repo, "legacy/ic-177-work")
			before := runGit(t, old, "rev-parse", "HEAD")
			runGit(t, old, "commit", "--allow-empty", "-m", "later work")
			policyPullRequest = func(string, string) branchPull { return branchPull{State: state, Head: before, Base: "trunk"} }
			plan, err := planWorktree(worktreeRequest{Cwd: repo, Name: "fix", Issue: "IC-177"})
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Candidates) != 1 || !plan.Candidates[0].Recommended || plan.Candidates[0].Path != old || plan.Candidates[0].PRState != state {
				t.Fatalf("lost unfinished work: %+v", plan)
			}
		})
	}
}

func TestLifecycleRemoteRestoreUsesVerifiedCommit(t *testing.T) {
	repo, origin := policyFixture(t)
	branch := "legacy/ic-177-remote"
	runGit(t, origin, "branch", branch)
	plan, err := planWorktree(worktreeRequest{Cwd: repo, Name: "fix", Issue: "IC-177"})
	if err != nil {
		t.Fatal(err)
	}
	choice := plan.Candidates[0]
	if len(plan.Candidates) != 1 || choice.Source != "remote" || choice.Existing || choice.Checkout || choice.StartCommit != runGit(t, origin, "rev-parse", branch) {
		t.Fatalf("wrong remote restore: %+v", plan)
	}
	f := startFakeHerdr(t)
	f.handle("worktree.list", map[string]any{"worktrees": []any{listedCheckout(repo, "wparent")}})
	f.handle("workspace.get", map[string]any{"workspace": wsInfo("wparent", "Primary", repo, false)})
	f.handleFunc("worktree.create", func(req request) (any, *herdrError) {
		if req.Params["base"] != choice.StartCommit || req.Params["branch"] != branch {
			t.Errorf("wrong restore: %v", req.Params)
		}
		return selectionResult(branch, choice.Path), nil
	})
	_, err = applyWorktreePlan(worktreeRequest{Cwd: repo, Name: "fix", Issue: "IC-177", Candidate: choice.ID, Fingerprint: plan.Fingerprint}, plan)
	if err != nil {
		t.Fatal(err)
	}
	f.assertNotCalled("worktree.open")
}

func TestLifecycleRemoteAdvanceRefusesRestore(t *testing.T) {
	repo, origin := policyFixture(t)
	branch := "legacy/ic-177-remote"
	runGit(t, origin, "branch", branch)
	plan, err := planWorktree(worktreeRequest{Cwd: repo, Name: "fix", Issue: "IC-177"})
	if err != nil {
		t.Fatal(err)
	}
	choice := plan.Candidates[0]
	f := startFakeHerdr(t)
	f.handleFunc("worktree.list", func(request) (any, *herdrError) {
		runGit(t, origin, "switch", branch)
		runGit(t, origin, "commit", "--allow-empty", "-m", "new remote tip")
		return map[string]any{"worktrees": []any{listedCheckout(repo, "wparent")}}, nil
	})
	_, err = applyWorktreePlan(worktreeRequest{Cwd: repo, Name: "fix", Issue: "IC-177", Candidate: choice.ID, Fingerprint: plan.Fingerprint}, plan)
	if err == nil {
		t.Fatal("restored a remote tip the user did not choose")
	}
	f.assertNotCalled("worktree.create")
}

func TestLifecycleLocalHeadAdvanceRefusesOpen(t *testing.T) {
	repo, _ := policyFixture(t)
	old := addWorktree(t, repo, "legacy/ic-177-work")
	plan, err := planWorktree(worktreeRequest{Cwd: repo, Name: "fix", Issue: "IC-177"})
	if err != nil {
		t.Fatal(err)
	}
	choice := plan.Candidates[0]
	f := startFakeHerdr(t)
	f.handle("worktree.list", map[string]any{"worktrees": []any{listedCheckout(repo, "wparent")}})
	f.handleFunc("workspace.get", func(request) (any, *herdrError) {
		runGit(t, old, "commit", "--allow-empty", "-m", "new local tip")
		return map[string]any{"workspace": wsInfo("wparent", "Primary", repo, false)}, nil
	})
	_, err = applyWorktreePlan(worktreeRequest{Cwd: repo, Name: "fix", Issue: "IC-177", Candidate: choice.ID, Fingerprint: plan.Fingerprint}, plan)
	if err == nil {
		t.Fatal("opened a changed local selection")
	}
	f.assertNotCalled("worktree.open")
}

func TestLifecycleSuffixKeepsIssueAndTailBudget(t *testing.T) {
	root := t.TempDir()
	name := "a very long title that reaches the branch limit"
	first, err := unusedPolicyBranch("ingwon/", name, "HSYS-1234", 29, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	occupied := map[string]string{first: "oid"}
	second, err := unusedPolicyBranch("ingwon/", name, "HSYS-1234", 29, root, occupied)
	if err != nil {
		t.Fatal(err)
	}
	occupied[second] = "oid"
	third, err := unusedPolicyBranch("ingwon/", name, "HSYS-1234", 29, root, occupied)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(second, "-2") || !strings.HasSuffix(third, "-3") || len(strings.TrimPrefix(third, "ingwon/")) > 29 || !matchesIssue(third, "HSYS-1234") {
		t.Fatalf("invalid suffixes: %s %s", second, third)
	}
	if _, err := unusedPolicyBranch("ingwon/", "", "HSYS-1234", 9, root, map[string]string{"ingwon/hsys-1234": "oid"}); err == nil {
		t.Fatal("suffix cut the issue identifier")
	}
}
