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
	"regexp"
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

// worktreeSelection is the plan snapshot a caller already showed the user and
// the user accepted. ensure-worktree reads Git for itself, so without the
// snapshot its fresh answer would quietly replace the accepted one; carrying it
// through makes a changed selection a visible refusal instead.
//
// The legacy explicit-cwd command passes nil. It has no accepted plan to
// preserve — its caller supplies the branch, path and base directly — and its
// semantics are unchanged by these additional constraints.
type worktreeSelection struct {
	// Repository is the canonical primary checkout the plan was built against.
	Repository string
	// Branch and Path are the accepted candidate.
	Branch, Path string
	// Checkout is true when the candidate was a checkout Git had already
	// registered for Branch, so Path must still be that registration.
	Checkout bool
	// Existing is true when Branch already existed locally at plan time.
	Existing bool
	// BaseOID is the commit the plan resolved the base ref to. It is empty for
	// a candidate that needs no base.
	BaseOID string
}

// isObjectID reports whether value is a full Git object id — SHA-1 or SHA-256.
// A base that reaches native becomes the start-point argument of
// `git worktree add`, so it must be an object id and nothing that Git could
// interpret as an option or a revision expression.
func isObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// errWorktreeSelectionChanged closes every selection refusal with the only safe
// remedy. Applying the new state instead would create or open something the
// user never chose.
var errWorktreeSelectionChanged = errors.New("refresh and choose again; no worktree was created")

// verifyGitState compares the fresh Git read against the accepted candidate.
func (s *worktreeSelection) verifyGitState(root, branch, path, registered string) error {
	if !samePath(root, s.Repository) {
		return fmt.Errorf("the repository resolved to %s, not the planned %s; %w", root, s.Repository, errWorktreeSelectionChanged)
	}
	if branch != s.Branch {
		return fmt.Errorf("the branch resolved to %q, not the selected %q; %w", branch, s.Branch, errWorktreeSelectionChanged)
	}
	if s.Checkout {
		if registered == "" || !samePath(registered, s.Path) {
			return fmt.Errorf("branch %s is no longer checked out at the selected %s (git now registers %q); %w", branch, s.Path, registered, errWorktreeSelectionChanged)
		}
		return nil
	}
	if registered != "" {
		return fmt.Errorf("branch %s acquired a checkout at %s after the plan was made; %w", branch, registered, errWorktreeSelectionChanged)
	}
	if filepath.Clean(path) != filepath.Clean(s.Path) {
		return fmt.Errorf("the destination resolved to %s, not the selected %s; %w", path, s.Path, errWorktreeSelectionChanged)
	}
	return nil
}

// verifyBranchPresence refuses when the local branch appeared or disappeared
// after the plan: either way the selection now means something else.
func (s *worktreeSelection) verifyBranchPresence(branch string, absent bool) error {
	if absent != s.Existing {
		return nil
	}
	state := "now exists locally"
	if absent {
		state = "no longer exists locally"
	}
	return fmt.Errorf("branch %s %s, contradicting the accepted plan; %w", branch, state, errWorktreeSelectionChanged)
}

// verifyBase confirms the fetch brought back exactly the commit the plan showed,
// and returns that commit for native creation.
//
// origin/BASE is a mutable ref: the remote can advance between planning and
// applying, and another local fetch can move it again. So the ref is checked
// once, here, against the accepted commit — and it is the commit, never the
// ref, that is handed to native afterwards. Herdr 0.9 passes base through
// `run_worktree_add_command` into the start-point argument of
// `git worktree add -b <branch> <path> <base>` with no branch-only validation
// (pinned `src/app/api/worktrees/deferred.rs` and `src/worktree.rs`), so an
// object id is accepted there. Native uses this start point only if the branch
// is still absent when its asynchronous Git operation runs.
func (s *worktreeSelection) verifyBase(root, ref string) (string, error) {
	if !isObjectID(s.BaseOID) {
		return "", fmt.Errorf("the accepted plan carries no verified base commit for %s; %w", s.Branch, errWorktreeSelectionChanged)
	}
	out, err := ensureGit(root, 10*time.Second, "rev-parse", "--verify", ref)
	if err != nil {
		return "", err
	}
	if got := strings.TrimSpace(string(out)); got != s.BaseOID {
		return "", fmt.Errorf("base %s resolves to %s, not the accepted %s; %w", ref, got, s.BaseOID, errWorktreeSelectionChanged)
	}
	return s.BaseOID, nil
}

// verifyNativeParent refuses a native parent record that does not agree with the
// Git repository just planned.
//
// Herdr 0.9 routes a workspace-addressed operation through that workspace's
// stored membership.repo_root (`worktree_source_from_workspace`), so a record
// whose repo_root is missing or names another repository would run the mutation
// somewhere other than the checkout this plan was built from — even when its
// checkout_path matches. repo_key is the identity herdr groups and finds parent
// workspaces by (`find_parent_workspace_by_key`), and pinned
// `src/workspace/git_discovery.rs` derives it as the canonical Git common
// directory, so Git can be asked for it directly rather than trusted.
//
// commonDir is this repository's canonical Git common directory.
func verifyNativeParent(parent workspaceInfo, workspace, primary, commonDir string) error {
	// No provenance at all is a different situation from provenance that
	// disagrees, and it has a different remedy: herdr records checkout
	// membership when it opens a workspace as a worktree, not when one is made
	// with workspace.create, so a workspace opened outside Plus can hold this
	// very checkout and still carry nothing. Saying it "no longer holds the
	// primary checkout" would describe a state that is not the case and hide
	// the fix.
	if parent.WorkspaceID == workspace && parent.Worktree == nil {
		return fmt.Errorf("source workspace %s has no recorded checkout provenance; open the project through Herdr Plus so herdr records it, then retry. No worktree was created", workspace)
	}
	if parent.WorkspaceID != workspace || parent.Worktree == nil || parent.Worktree.IsLinkedWorktree || !samePath(parent.Worktree.CheckoutPath, primary) {
		return errors.New("source workspace no longer holds the primary checkout; no worktree was created")
	}
	if !samePath(parent.Worktree.RepoRoot, primary) {
		return fmt.Errorf("source workspace %s reports repository root %q, not the primary checkout %s it is being used for; no worktree was created", workspace, parent.Worktree.RepoRoot, primary)
	}
	// samePath refuses an empty value, so a record with no key is refused here
	// rather than needing a separate presence check.
	if !samePath(parent.Worktree.RepoKey, commonDir) {
		return fmt.Errorf("source workspace %s reports repository key %q, not this repository's Git directory %s; no worktree was created", workspace, parent.Worktree.RepoKey, commonDir)
	}
	// repo_name is herdr's display label for the repository, derived from the
	// common directory's own name; it selects nothing, so it is required to be
	// present and not re-derived here.
	if strings.TrimSpace(parent.Worktree.RepoName) == "" {
		return fmt.Errorf("source workspace %s reports no repository name; no worktree was created", workspace)
	}
	return nil
}

// ensureWorktree is the legacy explicit-cwd entry point: every constraint comes
// from its flags, with no accepted plan behind them.
func ensureWorktree(args []string) (json.RawMessage, error) {
	return ensureWorktreeSelected(args, nil)
}

// ensureWorktreeSelected is the sole ensure implementation. want is the plan
// snapshot to preserve, or nil for the legacy API.
func ensureWorktreeSelected(args []string, want *worktreeSelection) (json.RawMessage, error) {
	flags := flag.NewFlagSet("ensure-worktree", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var cwd, branch, base, path, workspace string
	var focus bool
	// Reject duplicates rather than letting a later flag hide an invalid value.
	seen := make(map[string]bool)
	for _, entry := range []struct {
		name  string
		value *string
	}{
		{"cwd", &cwd}, {"branch", &branch}, {"base", &base}, {"path", &path}, {"workspace", &workspace},
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
	if seen["workspace"] && !workspaceIDPattern.MatchString(workspace) {
		return nil, errors.New("--workspace must be an explicit native workspace id")
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
	if want != nil {
		if err := want.verifyGitState(canonical, branch, path, registered); err != nil {
			return nil, err
		}
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
		if want != nil {
			if err := want.verifyBranchPresence(branch, absent); err != nil {
				return nil, err
			}
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
			// Legacy callers name a branch and get the remote-tracking ref they
			// asked for. An accepted plan gets the commit it showed the user.
			params["base"] = "origin/" + base
			if want != nil {
				verified, err := want.verifyBase(canonical, "refs/remotes/origin/"+base)
				if err != nil {
					return nil, err
				}
				params["base"] = verified
			}
		}
	}
	client, err := newHerdrClient()
	if err != nil {
		return nil, err
	}
	client.timeout = 30 * time.Second
	if workspace != "" {
		parent, err := client.workspaceGet(workspace)
		if err != nil {
			return nil, err
		}
		// Ask Git for the same value herdr derives repo_key from, rather than
		// accepting the key the record carries.
		commonDir, err := ensureGit(canonical, 10*time.Second, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return nil, err
		}
		if err := verifyNativeParent(parent, workspace, canonical, strings.TrimSpace(string(commonDir))); err != nil {
			return nil, err
		}
		delete(params, "cwd")
		params["workspace_id"] = workspace
	}
	// Fetch and native parent lookup can be slow. Refuse a selection whose
	// Git meaning changed during those preparations, just before submission.
	// Native execution is asynchronous; this is not an atomic Git lock.
	if want != nil {
		listing, err := ensureGit(canonical, 10*time.Second, "worktree", "list", "--porcelain", "-z")
		if err != nil {
			return nil, err
		}
		registered, err := ensureRegisteredPath(canonical, listing, branch)
		if err != nil {
			return nil, err
		}
		if err := want.verifyGitState(canonical, branch, path, registered); err != nil {
			return nil, err
		}
		_, err = ensureGit(canonical, 10*time.Second, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
		var exitErr *exec.ExitError
		absent := errors.As(err, &exitErr) && exitErr.ExitCode() == 1
		if err != nil && !absent {
			return nil, err
		}
		if err := want.verifyBranchPresence(branch, absent); err != nil {
			return nil, err
		}
	}
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

// gitWorktreeRecord is one entry of `git worktree list --porcelain -z`: the
// checkout's path plus the state Git registers for it. It is shared by the
// ensure-worktree command and the Projects checkout binding so both read Git's
// registry through one strict parser rather than two.
type gitWorktreeRecord struct {
	Path string
	// Ref is the full "refs/heads/<name>" a checkout has on a branch, empty when
	// it is detached or bare.
	Ref      string
	Detached bool
	Bare     bool
}

// Porcelain -z uses NUL fields and an empty field between records, preserving
// spaces, newlines and quotes in paths. A broken query is never branch absence.
func parseGitWorktreeList(cwd string, data []byte) ([]gitWorktreeRecord, error) {
	bad := errors.New("malformed git worktree list output")
	if len(data) == 0 || !bytes.HasSuffix(data, []byte{0, 0}) {
		return nil, bad
	}
	var records []gitWorktreeRecord
	for _, record := range strings.Split(string(data[:len(data)-2]), "\x00\x00") {
		fields := strings.Split(record, "\x00")
		if !strings.HasPrefix(fields[0], "worktree ") {
			return nil, bad
		}
		path := strings.TrimPrefix(fields[0], "worktree ")
		if !filepath.IsAbs(path) {
			return nil, bad
		}
		var head, ref string
		var detached, bare bool
		seen := make(map[string]bool)
		for _, field := range fields[1:] {
			key, value, hasValue := strings.Cut(field, " ")
			if seen[key] {
				return nil, bad
			}
			seen[key] = true
			switch key {
			case "HEAD":
				if !hasValue || (len(value) != 40 && len(value) != 64) {
					return nil, bad
				}
				if _, err := hex.DecodeString(value); err != nil {
					return nil, bad
				}
				head = value
			case "branch":
				if !strings.HasPrefix(value, "refs/heads/") || len(value) == len("refs/heads/") {
					return nil, bad
				}
				if _, err := ensureGit(cwd, 10*time.Second, "check-ref-format", value); err != nil {
					return nil, fmt.Errorf("malformed git worktree branch: %w", err)
				}
				ref = value
			case "detached":
				if hasValue {
					return nil, bad
				}
				detached = true
			case "bare":
				if hasValue {
					return nil, bad
				}
				bare = true
			case "locked", "prunable": // Optional reason is an opaque NUL-delimited field.
			default:
				return nil, bad
			}
		}
		if bare {
			if head != "" || ref != "" || detached {
				return nil, bad
			}
		} else if head == "" || (ref == "" && !detached) || (ref != "" && detached) {
			return nil, bad
		}
		records = append(records, gitWorktreeRecord{Path: path, Ref: ref, Detached: detached, Bare: bare})
	}
	return records, nil
}

// ensureRegisteredPath returns the checkout path Git has registered for branch,
// or "" when the branch has no worktree. A broken query is never branch absence.
func ensureRegisteredPath(cwd string, data []byte, branch string) (string, error) {
	records, err := parseGitWorktreeList(cwd, data)
	if err != nil {
		return "", err
	}
	var selected string
	for _, record := range records {
		if record.Ref == "refs/heads/"+branch {
			if selected != "" {
				return "", errors.New("ambiguous git worktree registration for branch")
			}
			selected = record.Path
		}
	}
	return selected, nil
}

// panePartPattern is the suffix Herdr appends to the owning workspace id to
// spell a pane id: ":p" plus the pane's public number
// (public_pane_id_for_number in the pinned 0.9.0 source). Checking it, and the
// workspace id it is built on, is what makes "this pane belongs to this
// workspace" a fact rather than an assumption the adapter would inherit.
var panePartPattern = regexp.MustCompile(`^p[0-9A-Za-z]+$`)

func validateEnsureResult(raw json.RawMessage, branch, expectedPath string) error {
	var result struct {
		Workspace struct {
			ID string `json:"workspace_id"`
		} `json:"workspace"`
		RootPane struct {
			ID          string `json:"pane_id"`
			WorkspaceID string `json:"workspace_id"`
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
	workspaceID := strings.TrimSpace(result.Workspace.ID)
	paneID := strings.TrimSpace(result.RootPane.ID)
	if !workspaceIDPattern.MatchString(workspaceID) || !workspaceIDPattern.MatchString(paneID) || !filepath.IsAbs(result.Worktree.Path) || strings.ContainsRune(result.Worktree.Path, 0) || result.Worktree.Branch != branch {
		return errors.New("native worktree result lacks valid workspace/pane IDs or matching worktree path/branch")
	}
	// A workspace and a root pane that name different workspaces are not one
	// result. Passing the pair on intact would hand the caller two native
	// objects it has no basis for treating as related.
	suffix, ok := strings.CutPrefix(paneID, workspaceID+":")
	if !ok || !panePartPattern.MatchString(suffix) || strings.TrimSpace(result.RootPane.WorkspaceID) != workspaceID {
		return fmt.Errorf("native worktree result reports root pane %q for workspace %q; they are not one workspace", result.RootPane.ID, result.Workspace.ID)
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
