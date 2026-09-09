package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ensure-worktree resolves Git state only. Native Herdr owns the worktree and
// workspace mutation; this command never creates a checkout or resets a branch.
func runEnsureWorktree(args []string) {
	result, err := ensureWorktree(args)
	if err != nil {
		errExit(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(struct {
		Result json.RawMessage `json:"result"`
	}{result}); err != nil {
		errExit("write result:", err)
	}
}

func ensureWorktree(args []string) (json.RawMessage, error) {
	flags := flag.NewFlagSet("ensure-worktree", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var cwd, branch, base, path string
	var focus bool
	// Reject duplicates rather than letting a later flag hide an invalid value.
	seen := make(map[string]bool)
	for _, entry := range []struct {
		name  string
		value *string
	}{
		{"cwd", &cwd}, {"branch", &branch}, {"base", &base}, {"path", &path},
	} {
		flags.Func(entry.name, "explicit "+entry.name, func(value string) error {
			if seen[entry.name] {
				return fmt.Errorf("duplicate --%s", entry.name)
			}
			seen[entry.name] = true
			*entry.value = value
			return nil
		})
	}
	flags.BoolVar(&focus, "focus", false, "focus the native workspace")
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	if flags.NArg() != 0 {
		return nil, errors.New("ensure-worktree accepts flags only")
	}
	if !filepath.IsAbs(cwd) || strings.ContainsRune(cwd, 0) {
		return nil, errors.New("--cwd must be an explicit absolute directory")
	}
	if branch == "" {
		return nil, errors.New("--branch is required")
	}
	if seen["path"] && (!filepath.IsAbs(path) || strings.ContainsRune(path, 0)) {
		return nil, errors.New("--path must be an absolute path")
	}
	canonical, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve --cwd: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return nil, fmt.Errorf("stat --cwd: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("--cwd must be a directory")
	}
	if err := ensureBranchName(canonical, "branch", branch); err != nil {
		return nil, err
	}
	if seen["base"] {
		if err := ensureBranchName(canonical, "base", base); err != nil {
			return nil, err
		}
	}
	root, err := ensureGit(canonical, 10*time.Second, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	canonical, err = filepath.EvalSymlinks(strings.TrimSuffix(string(root), "\n"))
	if err != nil || !filepath.IsAbs(canonical) {
		return nil, fmt.Errorf("invalid git repository root %q", root)
	}
	listing, err := ensureGit(canonical, 10*time.Second, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	registered, err := ensureRegisteredPath(canonical, listing, branch)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"cwd": canonical, "branch": branch, "focus": focus}
	method, expectedPath := "worktree.open", registered
	if registered == "" {
		method = "worktree.create"
		_, err := ensureGit(canonical, 10*time.Second, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
		var exitErr *exec.ExitError
		absent := errors.As(err, &exitErr) && exitErr.ExitCode() == 1
		if err != nil && !absent {
			return nil, err
		}
		if absent && (base == "" || path == "") {
			return nil, errors.New("a new branch requires explicit --base and --path")
		}
		if path != "" {
			// Check both spellings: trailing separators or '/.' can otherwise
			// make Lstat follow a dangling symlink and report it as absent.
			for _, candidate := range []string{path, filepath.Clean(path)} {
				if _, err := os.Lstat(candidate); err == nil {
					return nil, fmt.Errorf("target path already exists: %s", path)
				} else if !os.IsNotExist(err) {
					return nil, fmt.Errorf("inspect target path: %w", err)
				}
			}
			params["path"], expectedPath = path, path
		}
		if absent {
			// Map the destination explicitly: remote.origin.fetch may exclude BASE
			// and leave a previously fetched origin/BASE stale.
			if _, err := ensureGit(canonical, 2*time.Minute, "fetch", "origin", "refs/heads/"+base+":refs/remotes/origin/"+base); err != nil {
				return nil, err
			}
			params["base"] = "origin/" + base
		}
	}
	client, err := newHerdrClient()
	if err != nil {
		return nil, err
	}
	client.timeout = 30 * time.Second
	var result json.RawMessage
	if err := client.call(method, params, &result); err != nil {
		return nil, err
	}
	if err := validateEnsureResult(result, branch, expectedPath); err != nil {
		return nil, err
	}
	return result, nil
}

func ensureBranchName(cwd, flagName, value string) error {
	// Use a full ref so Git cannot expand @{-1}; branch shorthand must never
	// silently select another branch. Leading '-' is unsafe for fetch/native CLI;
	// a base beginning with '+' is force-refspec syntax, not an accepted base.
	if value == "" || strings.HasPrefix(value, "-") || value == "HEAD" || (flagName == "base" && strings.HasPrefix(value, "+")) {
		return fmt.Errorf("invalid --%s branch name %q", flagName, value)
	}
	if _, err := ensureGit(cwd, 10*time.Second, "check-ref-format", "refs/heads/"+value); err != nil {
		return fmt.Errorf("invalid --%s: %w", flagName, err)
	}
	return nil
}

func ensureGit(cwd string, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	// Explicit cwd owns repository discovery and its per-repository state.
	// Preserve transport, authentication and config variables (including
	// GIT_CONFIG_*); only inherited repository/object selection is excluded.
	cmd.Env = make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
			"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
			"GIT_GRAFT_FILE", "GIT_SHALLOW_FILE", "GIT_REPLACE_REF_BASE",
			"GIT_NO_REPLACE_OBJECTS", "GIT_IMPLICIT_WORK_TREE", "GIT_PREFIX",
			"GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	// Bound pipe draining too, e.g. when a Git transport leaves a child holding it.
	cmd.WaitDelay = time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("git %s: %w", args[0], ctx.Err())
	}
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Porcelain -z uses NUL fields and an empty field between records, preserving
// spaces, newlines and quotes in paths. A broken query is never branch absence.
func ensureRegisteredPath(cwd string, data []byte, branch string) (string, error) {
	bad := errors.New("malformed git worktree list output")
	if len(data) == 0 || !bytes.HasSuffix(data, []byte{0, 0}) {
		return "", bad
	}
	var selected string
	for _, record := range strings.Split(string(data[:len(data)-2]), "\x00\x00") {
		fields := strings.Split(record, "\x00")
		if !strings.HasPrefix(fields[0], "worktree ") {
			return "", bad
		}
		path := strings.TrimPrefix(fields[0], "worktree ")
		if !filepath.IsAbs(path) {
			return "", bad
		}
		var head, ref string
		var detached, bare bool
		seen := make(map[string]bool)
		for _, field := range fields[1:] {
			key, value, hasValue := strings.Cut(field, " ")
			if seen[key] {
				return "", bad
			}
			seen[key] = true
			switch key {
			case "HEAD":
				if !hasValue || (len(value) != 40 && len(value) != 64) {
					return "", bad
				}
				if _, err := hex.DecodeString(value); err != nil {
					return "", bad
				}
				head = value
			case "branch":
				if !strings.HasPrefix(value, "refs/heads/") || len(value) == len("refs/heads/") {
					return "", bad
				}
				if _, err := ensureGit(cwd, 10*time.Second, "check-ref-format", value); err != nil {
					return "", fmt.Errorf("malformed git worktree branch: %w", err)
				}
				ref = value
			case "detached":
				if hasValue {
					return "", bad
				}
				detached = true
			case "bare":
				if hasValue {
					return "", bad
				}
				bare = true
			case "locked", "prunable": // Optional reason is an opaque NUL-delimited field.
			default:
				return "", bad
			}
		}
		if bare {
			if head != "" || ref != "" || detached {
				return "", bad
			}
		} else if head == "" || (ref == "" && !detached) || (ref != "" && detached) {
			return "", bad
		}
		if ref == "refs/heads/"+branch {
			if selected != "" {
				return "", errors.New("ambiguous git worktree registration for branch")
			}
			selected = path
		}
	}
	return selected, nil
}

func validateEnsureResult(raw json.RawMessage, branch, expectedPath string) error {
	var result struct {
		Workspace struct {
			ID string `json:"workspace_id"`
		} `json:"workspace"`
		RootPane struct {
			ID string `json:"pane_id"`
		} `json:"root_pane"`
		Worktree struct {
			Path   string `json:"path"`
			Branch string `json:"branch"`
		} `json:"worktree"`
		AlreadyOpen *bool `json:"already_open"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("malformed native worktree result: %w", err)
	}
	if strings.TrimSpace(result.Workspace.ID) == "" || strings.TrimSpace(result.RootPane.ID) == "" || !filepath.IsAbs(result.Worktree.Path) || strings.ContainsRune(result.Worktree.Path, 0) || result.Worktree.Branch != branch {
		return errors.New("native worktree result lacks valid workspace/pane IDs or matching worktree path/branch")
	}
	if expectedPath != "" && filepath.Clean(result.Worktree.Path) != filepath.Clean(expectedPath) {
		// Native may canonicalize a supplied path through a symlinked parent.
		a, errA := filepath.EvalSymlinks(result.Worktree.Path)
		b, errB := filepath.EvalSymlinks(expectedPath)
		if errA != nil || errB != nil || a != b {
			return errors.New("native worktree result path does not match requested checkout")
		}
	}
	return nil
}
