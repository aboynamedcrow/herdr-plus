package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPolicyExistingNamesDoNotRequireNewSlug(t *testing.T) {
	for _, mode := range []string{"unicode exact name", "existing issue exceeds new tail budget"} {
		t.Run(mode, func(t *testing.T) {
			repo, _ := policyFixture(t)
			req := worktreeRequest{Cwd: repo, Name: "ingwon/한글"}
			branch := req.Name
			if mode != "unicode exact name" {
				req.Name = "new description"
				req.Issue = "IC-177"
				branch = "old/ic-177-kept"
				path := filepath.Join(os.Getenv("HERDR_PLUGIN_CONFIG_DIR"), "config.toml")
				data, _ := os.ReadFile(path)
				if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(data), "max_tail = 29", "max_tail = 5")), 0600); err != nil {
					t.Fatal(err)
				}
			}
			old := addWorktree(t, repo, branch)
			runGit(t, repo, "remote", "remove", "origin")
			plan, err := planWorktree(req)
			if err != nil {
				t.Fatalf("valid registered branch %q at %s cannot be reused: %v", branch, old, err)
			}
			if len(plan.Candidates) != 1 || plan.Candidates[0].Branch != branch || plan.Candidates[0].Path != old {
				t.Fatalf("lost existing checkout: %+v", plan)
			}
		})
	}
}
func TestPolicyBranchAppearsDuringParentLookup(t *testing.T) {
	repo, _ := policyFixture(t)
	req := worktreeRequest{Cwd: repo, Name: "new task"}
	plan, err := planWorktree(req)
	if err != nil {
		t.Fatal(err)
	}
	req.Fingerprint, req.Candidate = plan.Fingerprint, plan.Candidates[0].ID
	c := plan.Candidates[0]
	f := startFakeHerdr(t)
	f.handle("worktree.list", map[string]any{"worktrees": []any{listedCheckout(repo, "wparent")}})
	f.handleFunc("workspace.get", func(request) (any, *herdrError) {
		runGit(t, repo, "commit", "--allow-empty", "-m", "concurrent branch origin")
		runGit(t, repo, "branch", c.Branch, "HEAD")
		return map[string]any{"workspace": wsInfo("wparent", "Primary", repo, false)}, nil
	})
	// Model the pinned native run_worktree_add_command existing-branch path with
	// real disposable Git. No actual Herdr process is used.
	f.handleFunc("worktree.create", func(request) (any, *herdrError) {
		runGit(t, repo, "worktree", "add", c.Path, c.Branch)
		return selectionResult(c.Branch, c.Path), nil
	})
	_, err = applyWorktreePlan(req, plan)
	if err == nil {
		actual := runGit(t, c.Path, "rev-parse", "HEAD")
		t.Fatalf("apply succeeded after new selection became existing; accepted base=%s actual checkout=%s", plan.BaseOID, actual)
	}
	f.assertNotCalled("worktree.create")
}

func TestPolicyBranchDisappearsDuringParentLookup(t *testing.T) {
	repo, _ := policyFixture(t)
	branch := "ingwon/existing-task"
	runGit(t, repo, "branch", branch)
	req := worktreeRequest{Cwd: repo, Name: branch}
	plan, err := planWorktree(req)
	if err != nil {
		t.Fatal(err)
	}
	req.Fingerprint, req.Candidate = plan.Fingerprint, plan.Candidates[0].ID
	f := startFakeHerdr(t)
	f.handle("worktree.list", map[string]any{"worktrees": []any{listedCheckout(repo, "wparent")}})
	f.handleFunc("workspace.get", func(request) (any, *herdrError) {
		runGit(t, repo, "branch", "-D", branch)
		return map[string]any{"workspace": wsInfo("wparent", "Primary", repo, false)}, nil
	})
	if _, err := applyWorktreePlan(req, plan); err == nil {
		t.Fatal("accepted disappearance during parent lookup")
	}
	f.assertNotCalled("worktree.create")
}
