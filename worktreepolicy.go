package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Personal names and paths belong in configuration, shared by every UI caller.
type WorktreePolicy struct {
	Name       string `toml:"name" json:"name"`
	Repository string `toml:"repository" json:"repository"`
	Root       string `toml:"root" json:"root"`
	MaxTail    int    `toml:"max_tail" json:"max_tail"`
	Base       string `toml:"base" json:"base,omitempty"`
}

type worktreeRequest struct {
	Cwd, Name, Issue, Candidate, Fingerprint string
}

type worktreeChoice struct {
	ID       string `json:"id"`
	Branch   string `json:"branch"`
	Path     string `json:"path"`
	Existing bool   `json:"existing"`
	Checkout bool   `json:"checkout"`
}

type worktreePlan struct {
	Version     int              `json:"version"`
	Project     string           `json:"project"`
	Repository  string           `json:"repository"`
	Issue       string           `json:"issue,omitempty"`
	Base        string           `json:"base,omitempty"`
	BaseOID     string           `json:"base_oid,omitempty"`
	Candidates  []worktreeChoice `json:"candidates"`
	Fingerprint string           `json:"fingerprint"`
}

func parseWorktreeRequest(args []string, apply bool) (worktreeRequest, error) {
	var req worktreeRequest
	f := flag.NewFlagSet("worktree policy", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	seen := map[string]bool{}
	for _, spec := range []struct {
		name  string
		value *string
	}{
		{"cwd", &req.Cwd}, {"name", &req.Name}, {"issue", &req.Issue},
		{"candidate", &req.Candidate}, {"fingerprint", &req.Fingerprint},
	} {
		f.Func(spec.name, spec.name, func(value string) error {
			if seen[spec.name] {
				return fmt.Errorf("duplicate --%s", spec.name)
			}
			seen[spec.name] = true
			*spec.value = value
			return nil
		})
	}
	if err := f.Parse(args); err != nil {
		return req, err
	}
	if f.NArg() != 0 || !filepath.IsAbs(req.Cwd) || strings.TrimSpace(req.Name) == "" {
		return req, errors.New("explicit absolute --cwd and nonempty --name are required")
	}
	if apply && (req.Candidate == "" || req.Fingerprint == "") {
		return req, errors.New("apply-worktree requires a candidate and fingerprint from plan-worktree")
	}
	if !apply && (seen["candidate"] || seen["fingerprint"]) {
		return req, errors.New("candidate and fingerprint are apply-worktree flags")
	}
	return req, nil
}

func runWorktreePolicy(command string, args []string) {
	req, err := parseWorktreeRequest(args, command == "apply-worktree")
	if err != nil {
		errExit(err)
	}
	plan, err := planWorktree(req)
	if err != nil {
		errExit(err)
	}
	if command == "plan-worktree" {
		if err := json.NewEncoder(os.Stdout).Encode(plan); err != nil {
			errExit(err)
		}
		return
	}
	result, err := applyWorktreePlan(req, plan)
	if err != nil {
		errExit(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(struct {
		Result json.RawMessage `json:"result"`
	}{result}); err != nil {
		errExit(err)
	}
}

var issuePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-[1-9][0-9]*$`)
var leadingIssuePattern = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9]*-[1-9][0-9]*)(?:[^A-Za-z0-9]|$)`)
var slugBreak = regexp.MustCompile(`[^a-z0-9]+`)

func policyBranch(prefix, name, issue string, limit int) (string, error) {
	if prefix == "" || !strings.HasSuffix(prefix, "/") {
		return "", errors.New("worktree.branch_prefix must end in /")
	}
	name = strings.TrimPrefix(strings.TrimSpace(name), prefix)
	tail := strings.Trim(slugBreak.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if issue != "" {
		if !issuePattern.MatchString(issue) {
			return "", errors.New("invalid issue identifier")
		}
		id := strings.ToLower(issue)
		if tail != id && !strings.HasPrefix(tail, id+"-") {
			tail = id + "-" + tail
		}
	}
	if limit < 1 || limit > 200 {
		return "", errors.New("project max_tail must be between 1 and 200")
	}
	if len(tail) > limit {
		tail = strings.TrimRight(tail[:limit], "-")
	}
	if tail == "" || (issue != "" && len(tail) < len(issue)) {
		return "", errors.New("project max_tail leaves no complete issue/name")
	}
	return prefix + tail, nil
}

func matchesIssue(branch, issue string) bool {
	if issue == "" {
		return false
	}
	pattern := `(?i)(^|[/_-])` + regexp.QuoteMeta(issue) + `($|[/_-])`
	return regexp.MustCompile(pattern).MatchString(branch)
}

// canonicalDestination gives an absolute path that does not exist yet the same
// single spelling canonicalPath gives an existing one. It resolves the deepest
// ancestor that does exist and reattaches the components that do not, so a root
// reached through a symlink is identified by the directory it physically lands
// in. It creates nothing: planning stays read-only, and a destination whose
// ancestor is a file — or unreadable — is an error rather than a guess.
func canonicalDestination(path string) (string, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("%q is not an absolute path", path)
	}
	current := filepath.Clean(path)
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			info, err := os.Stat(resolved)
			if err != nil {
				return "", err
			}
			if !info.IsDir() {
				return "", fmt.Errorf("%s is not a directory", current)
			}
			return filepath.Join(append([]string{resolved}, missing...)...), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("%q has no existing ancestor directory", path)
		}
		missing = append([]string{filepath.Base(current)}, missing...)
		current = parent
	}
}

var errNoWorktreePolicy = errors.New("no configured worktree policy")

func policyForCheckout(cfg PluginConfig, primary string) (WorktreePolicy, error) {
	var matches []WorktreePolicy
	for _, policy := range cfg.Worktree.Projects {
		if policy.Repository == "" {
			return WorktreePolicy{}, errors.New("worktree project repository is required")
		}
		expanded, err := expandPath(policy.Repository)
		if err != nil || !filepath.IsAbs(expanded) {
			return WorktreePolicy{}, errors.New("worktree project repository must expand to an absolute path")
		}
		canonical, err := canonicalPath(expanded)
		if err != nil {
			return WorktreePolicy{}, fmt.Errorf("resolve configured repository %s: %w", policy.Name, err)
		}
		if canonical != primary {
			continue
		}
		if policy.Name == "" || policy.Root == "" {
			return WorktreePolicy{}, errors.New("project name and root are required")
		}
		policy.Repository = canonical
		policy.Root, err = expandPath(policy.Root)
		if err != nil || !filepath.IsAbs(policy.Root) {
			return WorktreePolicy{}, errors.New("project root must expand to an absolute path")
		}
		// The root is a destination, not an existing directory, so it cannot be
		// canonicalized outright. Resolving the ancestors it does have gives the
		// plan a physical identity: a retargeted symlink above the root then
		// changes the fingerprint instead of silently redirecting the checkout.
		policy.Root, err = canonicalDestination(policy.Root)
		if err != nil {
			return WorktreePolicy{}, fmt.Errorf("resolve configured root %s: %w", policy.Name, err)
		}
		matches = append(matches, policy)
	}
	if len(matches) == 0 {
		return WorktreePolicy{}, fmt.Errorf("expected one worktree policy for %s, found 0: %w", primary, errNoWorktreePolicy)
	}
	if len(matches) != 1 {
		return WorktreePolicy{}, fmt.Errorf("expected one worktree policy for %s, found %d", primary, len(matches))
	}
	return matches[0], nil
}

// Projects retains its legacy path only when this repository has no policy.
// Invalid or ambiguous configuration must not silently select another route.
func projectUsesSharedWorktreePolicy(cfg PluginConfig, cwd string) (bool, error) {
	if len(cfg.Worktree.Projects) == 0 {
		return false, nil
	}
	canonical, err := canonicalPath(cwd)
	if err != nil {
		return false, err
	}
	listing, err := ensureGit(canonical, 10*time.Second, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return false, err
	}
	records, err := parseGitWorktreeList(canonical, listing)
	if err != nil {
		return false, err
	}
	if len(records) == 0 {
		return false, errors.New("project has no primary Git checkout")
	}
	primary, err := canonicalPath(records[0].Path)
	if err != nil {
		return false, err
	}
	_, err = policyForCheckout(cfg, primary)
	if errors.Is(err, errNoWorktreePolicy) {
		return false, nil
	}
	return err == nil, err
}

// Resolve against the remote itself. A stale local origin/HEAD is not evidence of
// today's remote default, and a failed query never means "main".
func policyRemoteBase(repo, override string) (string, string, error) {
	args := []string{"ls-remote", "--symref", "origin", "HEAD"}
	if override != "" {
		if err := ensureBranchName(repo, "base", override); err != nil {
			return "", "", err
		}
		args = []string{"ls-remote", "origin", "refs/heads/" + override}
	}
	out, err := ensureGit(repo, 30*time.Second, args...)
	if err != nil {
		return "", "", err
	}
	branch, oid := override, ""
	refs, objects := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		left, right, ok := strings.Cut(line, "\t")
		if !ok {
			return "", "", errors.New("malformed remote base response")
		}
		if strings.HasPrefix(left, "ref: refs/heads/") && right == "HEAD" && override == "" {
			refs++
			branch = strings.TrimPrefix(left, "ref: refs/heads/")
			continue
		}
		expectedRef := "HEAD"
		if override != "" {
			expectedRef = "refs/heads/" + override
		}
		if right != expectedRef {
			return "", "", errors.New("unexpected remote base ref")
		}
		if len(left) != 40 && len(left) != 64 {
			return "", "", errors.New("invalid remote object id")
		}
		if _, err := hex.DecodeString(left); err != nil {
			return "", "", errors.New("invalid remote object id")
		}
		objects++
		oid = left
	}
	if objects != 1 || (override == "" && refs != 1) {
		return "", "", errors.New("remote default branch is missing or ambiguous; configure a project base explicitly")
	}
	if err := ensureBranchName(repo, "base", branch); err != nil {
		return "", "", err
	}
	return branch, oid, nil
}

func planWorktree(req worktreeRequest) (worktreePlan, error) {
	var plan worktreePlan
	if !filepath.IsAbs(req.Cwd) || strings.TrimSpace(req.Name) == "" {
		return plan, errors.New("explicit cwd/name required")
	}
	if req.Issue != "" && !issuePattern.MatchString(req.Issue) {
		return plan, errors.New("invalid issue identifier")
	}
	cwd, err := canonicalPath(req.Cwd)
	if err != nil {
		return plan, err
	}
	listing, err := ensureGit(cwd, 10*time.Second, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return plan, err
	}
	records, err := parseGitWorktreeList(cwd, listing)
	if err != nil {
		return plan, err
	}
	if len(records) == 0 || records[0].Bare {
		return plan, errors.New("shared worktree policy requires a non-bare primary checkout")
	}
	primary, err := canonicalPath(records[0].Path)
	if err != nil {
		return plan, err
	}
	cfg, err := loadPluginConfig()
	if err != nil {
		return plan, err
	}
	policy, err := policyForCheckout(cfg, primary)
	if err != nil {
		return plan, err
	}
	if req.Issue == "" {
		name := strings.TrimPrefix(strings.TrimSpace(req.Name), cfg.Worktree.BranchPrefix)
		if match := leadingIssuePattern.FindStringSubmatch(name); len(match) > 1 {
			req.Issue = match[1]
		}
	}
	// Validate configuration independently from a hypothetical new name.
	// Existing branch names and issue matches need no normalization.
	probe, err := policyBranch(cfg.Worktree.BranchPrefix, "valid", "", policy.MaxTail)
	if err != nil {
		return plan, err
	}
	if err := ensureBranchName(primary, "branch prefix", probe); err != nil {
		return plan, err
	}
	branch, nameErr := policyBranch(cfg.Worktree.BranchPrefix, req.Name, req.Issue, policy.MaxTail)
	refs, err := ensureGit(primary, 10*time.Second, "for-each-ref", "--format=%(refname)%00%(objectname)", "refs/heads/")
	if err != nil {
		return plan, err
	}
	heads := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(refs), "\n"), "\n") {
		if line == "" {
			continue
		}
		ref, oid, ok := strings.Cut(line, "\x00")
		if !ok || !strings.HasPrefix(ref, "refs/heads/") || (len(oid) != 40 && len(oid) != 64) {
			return plan, errors.New("malformed local branch inventory")
		}
		if _, err := hex.DecodeString(oid); err != nil {
			return plan, errors.New("malformed local branch object id")
		}
		name := strings.TrimPrefix(ref, "refs/heads/")
		if _, exists := heads[name]; exists {
			return plan, errors.New("duplicate local branch inventory")
		}
		heads[name] = oid
	}
	paths := map[string]string{}
	for _, record := range records {
		if record.Ref == "" {
			continue
		}
		name := strings.TrimPrefix(record.Ref, "refs/heads/")
		if _, exists := paths[name]; exists {
			return plan, errors.New("ambiguous registered branch checkouts")
		}
		if _, exists := heads[name]; !exists {
			return plan, errors.New("worktree registry disagrees with local branches")
		}
		paths[name] = record.Path
	}
	plan = worktreePlan{Version: 1, Project: policy.Name, Repository: primary, Issue: strings.ToUpper(req.Issue), Candidates: []worktreeChoice{}}
	for name := range heads {
		if req.Issue != "" && !matchesIssue(name, req.Issue) {
			continue
		}
		if req.Issue == "" && name != branch && name != req.Name {
			continue
		}
		path, checkedOut := paths[name]
		if !checkedOut {
			path = filepath.Join(policy.Root, strings.ReplaceAll(name, "/", "--"))
		}
		plan.Candidates = append(plan.Candidates, worktreeChoice{Branch: name, Path: path, Existing: true, Checkout: checkedOut})
	}
	if len(plan.Candidates) == 0 {
		if nameErr != nil {
			return plan, nameErr
		}
		if err := ensureBranchName(primary, "branch", branch); err != nil {
			return plan, err
		}
		plan.Base, plan.BaseOID, err = policyRemoteBase(primary, policy.Base)
		if err != nil {
			return plan, err
		}
		plan.Candidates = append(plan.Candidates, worktreeChoice{Branch: branch, Path: filepath.Join(policy.Root, strings.ReplaceAll(branch, "/", "--"))})
	}
	sort.Slice(plan.Candidates, func(i, j int) bool { return plan.Candidates[i].Branch < plan.Candidates[j].Branch })
	for i := range plan.Candidates {
		c := &plan.Candidates[i]
		hash := sha256.Sum256([]byte(c.Branch + "\x00" + c.Path))
		c.ID = hex.EncodeToString(hash[:])
	}
	data, err := json.Marshal(struct {
		Plan                   worktreePlan
		Policy                 WorktreePolicy
		Prefix, Registry, Refs string
	}{plan, policy, cfg.Worktree.BranchPrefix, string(listing), string(refs)})
	if err != nil {
		return plan, err
	}
	hash := sha256.Sum256(data)
	plan.Fingerprint = hex.EncodeToString(hash[:])
	return plan, nil
}

func applyWorktreePlan(req worktreeRequest, plan worktreePlan) (json.RawMessage, error) {
	if req.Fingerprint == "" || req.Fingerprint != plan.Fingerprint {
		return nil, errors.New("worktree plan changed; refresh and choose again")
	}
	var selected *worktreeChoice
	for i := range plan.Candidates {
		if plan.Candidates[i].ID == req.Candidate {
			selected = &plan.Candidates[i]
		}
	}
	if selected == nil {
		return nil, errors.New("selected worktree candidate is no longer available")
	}
	client, err := newHerdrClient()
	if err != nil {
		return nil, err
	}
	client.timeout = 30 * time.Second
	entries, err := client.worktreeList(plan.Repository)
	if err != nil {
		return nil, err
	}
	var parentIDs []string
	for _, entry := range entries {
		if samePath(entry.Path, plan.Repository) && entry.OpenWorkspaceID != nil {
			parentIDs = append(parentIDs, *entry.OpenWorkspaceID)
		}
	}
	if len(parentIDs) != 1 || !workspaceIDPattern.MatchString(parentIDs[0]) {
		return nil, errors.New("open the primary-checkout project first; no worktree was created")
	}
	args := []string{"--cwd", plan.Repository, "--branch", selected.Branch, "--workspace", parentIDs[0], "--focus"}
	if !selected.Checkout {
		args = append(args, "--path", selected.Path)
	}
	if !selected.Existing {
		args = append(args, "--base", plan.Base)
	}
	// The flags above name the selection; the snapshot below is what the user
	// actually accepted. ensure-worktree reads Git again, and without the
	// snapshot its fresh answer would silently become the decision — a moved
	// checkout opened instead of the chosen one, or a base that advanced between
	// the plan and the fetch.
	return ensureWorktreeSelected(args, &worktreeSelection{
		Repository: plan.Repository,
		Branch:     selected.Branch,
		Path:       selected.Path,
		Checkout:   selected.Checkout,
		Existing:   selected.Existing,
		BaseOID:    plan.BaseOID,
	})
}
