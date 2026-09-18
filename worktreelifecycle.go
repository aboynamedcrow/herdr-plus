package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type branchPull struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	Head   string `json:"headRefOid"`
	Base   string `json:"baseRefName"`
}

// A failed lookup is not proof that work has finished.
var policyPullRequest = func(repo, branch string) branchPull {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "list", "--state", "all", "--head", branch,
		"--limit", "50", "--json", "number,state,headRefOid,baseRefName")
	cmd.Dir = repo
	cmd.Env = ensureGitEnv()
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	var rows []branchPull
	if err != nil || json.Unmarshal(out, &rows) != nil || len(rows) >= 50 {
		return branchPull{State: "UNKNOWN"}
	}
	newest := branchPull{State: "NONE"}
	for _, row := range rows {
		if row.Number <= 0 || (row.State != "OPEN" && row.State != "MERGED" && row.State != "CLOSED") {
			return branchPull{State: "UNKNOWN"}
		}
		if row.State == "OPEN" {
			return row
		}
		if row.Number > newest.Number {
			newest = row
		}
	}
	if newest.State != "NONE" && newest.State != "MERGED" && newest.State != "CLOSED" {
		return branchPull{State: "UNKNOWN"}
	}
	return newest
}

func policyRemoteHeads(repo string) (map[string]string, error) {
	out, err := ensureGit(repo, 30*time.Second, "ls-remote", "--heads", "origin")
	if err != nil {
		return nil, err
	}
	heads := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		oid, ref, ok := strings.Cut(line, "\t")
		if !ok || !isObjectID(oid) || !strings.HasPrefix(ref, "refs/heads/") {
			return nil, fmt.Errorf("invalid remote branch inventory")
		}
		heads[strings.TrimPrefix(ref, "refs/heads/")] = oid
	}
	return heads, nil
}

func describeChoice(repo string, c *worktreeChoice) bool {
	pr := policyPullRequest(repo, c.Branch)
	c.PRState = pr.State
	c.PRHead = pr.Head
	c.PRBase = pr.Base
	c.Action = "check out branch"
	if c.Checkout {
		c.Action = "open checkout"
	}
	if c.Source == "remote" {
		c.Action = "restore remote branch"
	}
	c.Recommended = true
	if c.Checkout {
		status, err := ensureGit(c.Path, 10*time.Second, "status", "--porcelain", "--untracked-files=all")
		if err != nil {
			c.WorkState = "UNKNOWN"
		} else if len(status) != 0 {
			c.WorkState = "dirty"
		} else {
			c.WorkState = "clean"
		}
	} else {
		c.WorkState = "no checkout"
	}
	if pr.State != "MERGED" {
		return false
	}
	if pr.Head != c.StartCommit || c.WorkState == "dirty" || c.WorkState == "UNKNOWN" {
		c.Action = "continue later work"
		return false
	}
	c.Action = "inspect merged work"
	c.Recommended = false
	return true
}

func unusedPolicyBranch(prefix, name, issue string, limit int, root string, occupied map[string]string) (string, error) {
	for n := 1; n < 10000; n++ {
		suffix := ""
		if n > 1 {
			suffix = fmt.Sprintf("-%d", n)
		}
		branch, err := policyBranch(prefix, name, issue, limit-len(suffix))
		if err != nil {
			return "", err
		}
		branch += suffix
		if _, exists := occupied[branch]; exists {
			continue
		}
		path := filepath.Join(root, strings.ReplaceAll(branch, "/", "--"))
		if _, err := os.Lstat(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return "", err
		}
		return branch, nil
	}
	return "", fmt.Errorf("no unused branch name; choose another title")
}
