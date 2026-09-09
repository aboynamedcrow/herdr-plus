//
// Date: 2026-09-09
// Author: Spicer Matthews (spicer@cloudmanic.com)
// Copyright: 2026 Cloudmanic Labs, LLC. All rights reserved.
//

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// This file holds the two "go back to what is already open" behaviors the
// Projects action grew, kept together because they answer the same question with
// the same evidence: which native workspace, if any, is already the thing the
// user is asking for?
//
//   - returnToCrew answers it for the action's own invocation context — the pane
//     the keybinding fired from — so pressing the Projects key inside a task
//     workspace returns to that task's tab instead of offering a picker.
//   - reuseOpenCheckout answers it for a project the user picked, so choosing an
//     already-open project focuses its workspace instead of building a second one.
//
// Three rules govern both, and every function here exists to keep them:
//
//  1. Identity comes from herdr's own metadata, read live — the workspace's
//     recorded checkout path, and the tab labels as they are right now. Never a
//     display title, never a remembered association, never another client's
//     focus, and never a repository key (every worktree of a repo shares one).
//  2. Reuse only ever moves focus. Nothing is created, closed, split, renamed,
//     relaid, or typed into. Returning to busy work must not disturb it.
//  3. When identity cannot be established, say so. A failed lookup is never
//     downgraded to "nothing matched", because that reads as "make a new one".

// returnToCrew focuses the task tab of the workspace the Projects action was
// invoked from, reporting whether it did. A false return with no error means
// this simply is not a task workspace — the caller should open the project
// picker, exactly as it always has.
//
// crewTab is the label the machine's config gives its task tab; the caller only
// gets here when one is configured, so no tab name is baked into the plugin.
//
// The injected context is a claim about a moment that has already passed, so
// every part of it is checked against live native metadata before anything is
// acted on: the workspace still exists and is the one named, the pane the action
// fired from still belongs to it, and the checkout has not changed underneath.
func returnToCrew(client *herdrClient, pc pluginContext, crewTab string) (bool, error) {
	label := strings.TrimSpace(crewTab)
	if label == "" || strings.TrimSpace(pc.WorkspaceID) == "" {
		// No policy, or no explicit context to work from. Fall back to the picker
		// rather than guessing at a workspace from anyone's focus.
		return false, nil
	}

	ws, err := client.workspaceGet(pc.WorkspaceID)
	if err != nil {
		return false, fmt.Errorf("look up workspace %s: %w", pc.WorkspaceID, err)
	}
	if ws.WorkspaceID != pc.WorkspaceID {
		return false, fmt.Errorf("cannot establish workspace identity: herdr answered for %q when asked about %q", ws.WorkspaceID, pc.WorkspaceID)
	}

	// The context names the pane the action fired from. Where herdr supplied one,
	// confirm it really lives in this workspace — that is the check that makes the
	// context explicit rather than ambient.
	if paneID := strings.TrimSpace(pc.FocusedPaneID); paneID != "" {
		pane, err := client.paneGet(paneID)
		if err != nil {
			return false, fmt.Errorf("look up pane %s from the action context: %w", paneID, err)
		}
		if pane.PaneID != paneID {
			return false, fmt.Errorf("cannot establish pane identity: herdr answered for pane %q when asked about %q", pane.PaneID, paneID)
		}
		if pane.WorkspaceID != pc.WorkspaceID {
			return false, fmt.Errorf("action context pane %s now belongs to workspace %q, not %q", paneID, pane.WorkspaceID, pc.WorkspaceID)
		}
	}

	if ws.Worktree == nil || !ws.Worktree.IsLinkedWorktree {
		// A plain folder or the repository's main checkout: not a task workspace,
		// so the Projects action does what it has always done.
		return false, nil
	}

	// A context describing a different checkout than the workspace now holds means
	// the world moved between the keypress and this lookup. Refuse rather than act
	// on the stale half.
	if pc.Worktree != nil && pc.Worktree.CheckoutPath != "" && ws.Worktree.CheckoutPath != "" &&
		!samePath(pc.Worktree.CheckoutPath, ws.Worktree.CheckoutPath) {
		return false, fmt.Errorf("workspace %s now holds checkout %s, not the %s the action was invoked from", ws.WorkspaceID, ws.Worktree.CheckoutPath, pc.Worktree.CheckoutPath)
	}

	tabs, err := client.tabList(ws.WorkspaceID)
	if err != nil {
		return false, fmt.Errorf("list tabs of workspace %s: %w", ws.WorkspaceID, err)
	}

	var matches []tabInfo
	for _, t := range tabs {
		if t.WorkspaceID != "" && t.WorkspaceID != ws.WorkspaceID {
			continue
		}
		if strings.TrimSpace(t.Label) == label {
			matches = append(matches, t)
		}
	}

	switch len(matches) {
	case 1:
		if err := client.tabFocus(matches[0].TabID); err != nil {
			return false, fmt.Errorf("focus tab %s: %w", matches[0].TabID, err)
		}
		return true, nil
	case 0:
		return false, fmt.Errorf("workspace %s (%s) has no tab labeled %q — it was renamed or closed; rename it back or open the project you want, since Projects will not create a second one", ws.WorkspaceID, ws.Label, label)
	default:
		ids := make([]string, len(matches))
		for i, t := range matches {
			ids[i] = t.TabID
		}
		return false, fmt.Errorf("workspace %s (%s) has %d tabs labeled %q (%s); rename all but one, since Projects will not guess which is the task tab", ws.WorkspaceID, ws.Label, len(matches), label, strings.Join(ids, ", "))
	}
}

// workspaceCandidate is one open workspace whose checkout is exactly the
// directory a project was about to be opened in. Everything shown to the user
// comes from herdr: the native workspace id — which is unique and unambiguous
// even when two workspaces carry the same label — the label, and the checkout.
type workspaceCandidate struct {
	WorkspaceID  string
	Label        string
	CheckoutPath string
}

// chooseWorkspaceFunc asks the user to pick between candidates. It reports the
// choice and whether one was made; false means the user cancelled, which is a
// no-op — not a licence to create a workspace they did not ask for. It is nil in
// headless callers (`herdr-plus open <name>`), where ambiguity is an error
// instead: there is no one at the keyboard to ask.
type chooseWorkspaceFunc func([]workspaceCandidate) (workspaceCandidate, bool, error)

// reuseOptions carries the reuse policy into openProject: whether reuse is
// enabled at all ([projects].reuse_checkout), and how to resolve an ambiguous
// match when it is.
type reuseOptions struct {
	enabled bool
	choose  chooseWorkspaceFunc
}

// reuseOpenCheckout focuses the workspace that already has dir checked out,
// reporting whether it did. A false return with no error means no open workspace
// holds this exact checkout and the caller should create one as usual.
//
// dir is the project's resolved working directory. Matching is by canonical
// filesystem path, so a symlinked alias of an open checkout is the same
// checkout, while a sibling worktree of the same repository — same repo_key,
// often the same label — is a different one and gets its own workspace.
func reuseOpenCheckout(client *herdrClient, dir string, choose chooseWorkspaceFunc) (bool, error) {
	want, err := canonicalPath(dir)
	if err != nil {
		// Canonicalization failing is not "nothing matched": we do not know what
		// this directory is, so we must not conclude a duplicate is safe to make.
		return false, fmt.Errorf("resolve the project directory %s: %w", dir, err)
	}

	workspaces, err := client.workspaceList()
	if err != nil {
		return false, fmt.Errorf("list workspaces to look for an open checkout: %w", err)
	}

	var matches []workspaceCandidate
	for _, ws := range workspaces {
		// A workspace herdr records no checkout for is never adopted: without
		// provenance there is nothing to prove it is this project.
		if ws.Worktree == nil || strings.TrimSpace(ws.Worktree.CheckoutPath) == "" || strings.TrimSpace(ws.WorkspaceID) == "" {
			continue
		}
		if !samePath(ws.Worktree.CheckoutPath, want) {
			continue
		}
		matches = append(matches, workspaceCandidate{
			WorkspaceID:  ws.WorkspaceID,
			Label:        ws.Label,
			CheckoutPath: ws.Worktree.CheckoutPath,
		})
	}

	switch len(matches) {
	case 0:
		return false, nil
	case 1:
		return true, focusExistingWorkspace(client, matches[0], want)
	}

	if choose == nil {
		ids := make([]string, len(matches))
		for i, m := range matches {
			ids[i] = m.WorkspaceID
		}
		return false, fmt.Errorf("%d open workspaces already have %s checked out (%s); focus the one you want, or close the others", len(matches), want, strings.Join(ids, ", "))
	}

	choice, ok, err := choose(matches)
	if err != nil {
		return false, err
	}
	if !ok {
		// Cancelled. Doing nothing is the whole point — falling through to create a
		// workspace would be the opposite of what the user just declined.
		return true, nil
	}
	return true, focusExistingWorkspace(client, choice, want)
}

// focusExistingWorkspace revalidates a candidate against live native metadata
// and then focuses it. The inventory it came from is a snapshot: between listing
// and focusing, a workspace can be closed or have its checkout change. Checking
// again means a stale choice fails visibly instead of focusing the wrong work.
func focusExistingWorkspace(client *herdrClient, candidate workspaceCandidate, want string) error {
	ws, err := client.workspaceGet(candidate.WorkspaceID)
	if err != nil {
		return fmt.Errorf("revalidate workspace %s before focusing it: %w", candidate.WorkspaceID, err)
	}
	if ws.WorkspaceID != candidate.WorkspaceID || ws.Worktree == nil || !samePath(ws.Worktree.CheckoutPath, want) {
		return fmt.Errorf("workspace %s no longer has %s checked out; nothing was opened or created", candidate.WorkspaceID, want)
	}
	if err := client.workspaceFocus(ws.WorkspaceID); err != nil {
		return fmt.Errorf("focus workspace %s: %w", ws.WorkspaceID, err)
	}
	return nil
}

// canonicalPath resolves a directory to the single spelling two paths can be
// compared by: absolute, with every symlink along the way resolved. It is an
// error when the directory cannot be resolved, because a guess here would decide
// whether a workspace gets reused or duplicated.
func canonicalPath(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

// samePath reports whether two paths name the same directory. It compares the
// cleaned paths first, then their canonical forms, so a symlinked alias matches
// the checkout it points at. A path that cannot be resolved (herdr knows of a
// checkout that has since been deleted, say) falls back to the plain comparison
// rather than matching loosely — this is never a fuzzy or prefix match.
func samePath(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ca, errA := canonicalPath(a)
	cb, errB := canonicalPath(b)
	return errA == nil && errB == nil && ca == cb
}

// Native checkout provenance and the workspaces herdr-plus creates itself
// ---------------------------------------------------------------------------
//
// herdr records checkout provenance (workspace.worktree) for a workspace it
// opened as a worktree, but NOT for one made with workspace.create — which is
// how openProject builds a project's workspace. Verified against Herdr 0.9.0:
// `handle_worktree_open` is what calls `mark_worktree_membership`; nothing on
// the workspace.create path does. So a project workspace this plugin created is
// invisible to the provenance matching above, and the second P would build a
// duplicate of work that is already on screen.
//
// The two functions below close that gap without loosening the identity rule —
// no shell-directory matching, no title matching, no local registry:
//
//   - bindCheckoutProvenance asks herdr to open the checkout it just created a
//     workspace for. herdr finds that workspace already open on that checkout
//     (`already_open`), attaches provenance to it, and leaves its panes alone —
//     it does not create or lay out anything. From then on the workspace is
//     matchable by the same strict provenance rule as any other.
//   - resolveCheckoutForReuse decides whether that is even possible, and covers
//     workspaces that never got provenance: ones from an older build, or created
//     some other way. herdr's own worktree.list reports which registered checkout
//     is open in which workspace, so a checkout that is open but unprovenanced is
//     detected — and refused visibly instead of duplicated. Adopting it would mean
//     trusting an identity herdr-plus cannot verify, which is the decision
//     deferred to the user. Anything it cannot establish — a Git that will not
//     answer, a listing that contradicts Git — is an error raised before a
//     workspace exists, never a silent fall-through to creating one.

// gitCheckout is a project directory that Git has registered as a checkout, and
// the primary checkout of its repository — the only source herdr accepts for a
// worktree action.
type gitCheckout struct {
	// Path is the canonical registered checkout path, as Git spells it.
	Path string
	// Primary is the canonical path of the repository's main worktree.
	Primary string
}

// resolveGitCheckout reports how a project's directory sits in Git's own
// registry (`git worktree list --porcelain`, through the same strict parser
// ensure-worktree uses), for a directory herdr has already confirmed is inside a
// Git work tree.
//
// It returns nil with no error for the one benign case: the directory is inside
// a work tree but is not itself a checkout root — a monorepo subdirectory, say —
// so there is no checkout for herdr to record.
//
// Every other problem is an error, and the caller surfaces it before creating
// anything. A Git that cannot be run, times out, or answers with a listing that
// cannot be parsed leaves this plugin unable to tell whether the project is
// already open; creating a workspace anyway is precisely the duplicate this is
// meant to prevent. Guessing a primary path from a directory name is likewise
// out: it is the kind of improvisation that puts panes in the wrong place.
func resolveGitCheckout(canonical string) (*gitCheckout, error) {
	listing, err := ensureGit(canonical, 10*time.Second, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, fmt.Errorf("read the Git worktree registry for %s: %w\n  herdr reports this directory is inside a Git work tree, so this has to be readable before a second workspace could safely be created", canonical, err)
	}
	records, err := parseGitWorktreeList(canonical, listing)
	if err != nil {
		return nil, fmt.Errorf("read the Git worktree registry for %s: %w", canonical, err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("git registered no worktrees for %s", canonical)
	}
	// Git lists the main worktree first, then each linked worktree.
	primary := records[0]
	if primary.Bare {
		return nil, fmt.Errorf("repository for %s has a bare main worktree, so herdr has no primary checkout to open it from", canonical)
	}
	primaryPath, err := canonicalPath(primary.Path)
	if err != nil {
		return nil, fmt.Errorf("resolve the primary checkout %s: %w", primary.Path, err)
	}
	for _, record := range records {
		if record.Bare {
			continue
		}
		if samePath(record.Path, canonical) {
			return &gitCheckout{Path: canonical, Primary: primaryPath}, nil
		}
	}
	// Inside a work tree but not a checkout root. herdr's provenance is per
	// checkout, so there is nothing to bind and nothing to look up.
	return nil, nil
}

// workspaceIDPattern is the shape a herdr workspace id has ("w3A", "w1D:2").
// An id from herdr is only ever shown back to the user inside a message, so it
// is checked before it is displayed rather than pasted through unexamined.
var workspaceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

// resolveCheckoutForReuse decides what to do about a project directory after
// provenance matching found no match. It returns the checkout to bind once the
// workspace has been created, or nil when there is nothing to bind — and an
// error whenever the answer cannot be established, so that every uncertainty is
// resolved BEFORE a workspace is created rather than after.
//
// herdr, not this plugin, decides whether the directory is a Git checkout at
// all: a native "not_git_worktree" refusal is the one positive identification of
// a non-Git project. A local Git failure is never read that way, because "git
// could not answer" and "this is not a repository" are completely different
// facts with opposite consequences.
//
// When the directory is a registered checkout root, herdr's listing must contain
// exactly one entry for it. A valid but empty listing, a listing missing this
// checkout, or one naming it twice are all inconsistent or ambiguous answers —
// never absence — and none of them may lead to a second workspace.
func resolveCheckoutForReuse(client *herdrClient, dir string) (*gitCheckout, error) {
	canonical, err := canonicalPath(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve the project directory %s: %w", dir, err)
	}

	entries, err := client.worktreeList(canonical)
	if err != nil {
		if herdrErrorCode(err) == "not_git_worktree" {
			// The one benign refusal: herdr says this is not a Git work tree, so
			// there is no checkout identity to establish and the project opens
			// exactly as it always has.
			return nil, nil
		}
		return nil, fmt.Errorf("ask herdr which checkouts are open: %w", err)
	}

	checkout, err := resolveGitCheckout(canonical)
	if err != nil {
		return nil, err
	}
	if checkout == nil {
		return nil, nil
	}

	var matches []worktreeEntry
	for _, entry := range entries {
		if samePath(entry.Path, checkout.Path) {
			matches = append(matches, entry)
		}
	}
	switch len(matches) {
	case 1:
		// The consistent case, handled below.
	case 0:
		return nil, fmt.Errorf("git has %s registered as a checkout, but herdr's listing of this repository does not include it (%d other checkouts listed); nothing was created, because herdr and Git disagree about what this directory is", checkout.Path, len(entries))
	default:
		return nil, fmt.Errorf("herdr's listing names the checkout %s %d times; nothing was created, because there is no unambiguous entry to act on", checkout.Path, len(matches))
	}

	raw := matches[0].OpenWorkspaceID
	if raw == nil {
		// Registered, consistent, and not open anywhere: create it, then bind it.
		return checkout, nil
	}
	open := strings.TrimSpace(*raw)
	if !workspaceIDPattern.MatchString(open) {
		return nil, fmt.Errorf("herdr reports %s is already open, but gave its workspace id in an unusable form (%q); nothing was created or changed", checkout.Path, *raw)
	}
	return nil, fmt.Errorf("workspace %s already has %s checked out, but herdr holds no checkout provenance for it — so it cannot be identified as this project. Nothing was created or changed. Close that workspace and open the project again, or open the checkout through herdr's own worktree open so it carries provenance", open, checkout.Path)
}

// bindCheckoutProvenance attaches herdr's native checkout provenance to a
// workspace herdr-plus just created, so opening the same project again finds it
// instead of building a second one.
//
// It asks herdr to open the checkout the workspace was created for. herdr sees
// that checkout is already open, binds it to that very workspace, and reports
// already_open — no workspace, tab or pane is created, and the layout that was
// just built is left exactly as it is. The worktree.opened event this emits
// carries already_open, which the worktree layout handler skips on, so the
// layout cannot be applied a second time.
//
// Every part of the answer is checked: it must be the workspace we created, on
// the checkout we asked for, and already open. Anything else means herdr acted
// on something other than what was intended, which is reported rather than
// retried — one attempt, no competing lifecycle management.
func bindCheckoutProvenance(client *herdrClient, workspaceID string, checkout gitCheckout) error {
	result, err := client.worktreeOpenPath(checkout.Primary, checkout.Path, false)
	if err != nil {
		return fmt.Errorf("bind checkout %s to workspace %s: %w", checkout.Path, workspaceID, err)
	}
	if result.WorkspaceID != workspaceID {
		return fmt.Errorf("herdr bound checkout %s to workspace %s, not the workspace %s just created for it", checkout.Path, result.WorkspaceID, workspaceID)
	}
	if !samePath(result.Path, checkout.Path) {
		return fmt.Errorf("herdr bound workspace %s to checkout %s, not %s", workspaceID, result.Path, checkout.Path)
	}
	if !result.AlreadyOpen {
		return fmt.Errorf("herdr did not recognize workspace %s as already holding checkout %s; provenance was not established", workspaceID, checkout.Path)
	}
	return nil
}

// chooseWorkspaceInteractively is the chooseWorkspaceFunc the projects browser
// installs: a small fuzzyList of the matching workspaces, in the same idiom as
// every other picker here. It runs after the browser's own program has exited,
// in the same herdr-owned pane.
func chooseWorkspaceInteractively(candidates []workspaceCandidate, dir string) (workspaceCandidate, bool, error) {
	p := tea.NewProgram(newWorkspacePickerModel(candidates, dir), tea.WithAltScreen())
	result, err := p.Run()
	if err != nil {
		return workspaceCandidate{}, false, err
	}
	m, ok := result.(workspacePickerModel)
	if !ok || m.chosen == nil {
		return workspaceCandidate{}, false, nil
	}
	return *m.chosen, true, nil
}

// workspacePickerTitle heads the ambiguity picker.
const workspacePickerTitle = "Herdr Plus · Already open"

// workspacePickerModel is the explicit choice between several open workspaces on
// one checkout. There is deliberately no default: the first match is not more
// likely to be the right one, and picking it silently is exactly the guess this
// whole path exists to avoid. Esc cancels and nothing happens.
type workspacePickerModel struct {
	candidates []workspaceCandidate
	list       fuzzyList
	dir        string

	// chosen is the workspace to focus, read back after the program exits; nil
	// when the user cancelled.
	chosen *workspaceCandidate

	width  int
	height int
}

// newWorkspacePickerModel builds the picker over the matching workspaces. Each
// row leads with herdr's own workspace id, so two identically labeled
// workspaces are still told apart by something unambiguous.
func newWorkspacePickerModel(candidates []workspaceCandidate, dir string) workspacePickerModel {
	items := make([]listItem, len(candidates))
	for i, c := range candidates {
		items[i] = listItem{
			name:       fmt.Sprintf("%s · %s", c.WorkspaceID, c.Label),
			desc:       c.CheckoutPath,
			selectable: true,
			ref:        i,
		}
	}
	return workspacePickerModel{
		candidates: candidates,
		list:       newFuzzyList("Type to filter workspaces…", items),
		dir:        dir,
	}
}

// Init satisfies tea.Model; the picker has no startup command.
func (m workspacePickerModel) Init() tea.Cmd { return nil }

// Update handles navigation, an explicit enter, and cancellation.
func (m workspacePickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "up", "ctrl+p":
			m.list.moveUp()
			return m, nil
		case "down", "ctrl+n":
			m.list.moveDown()
			return m, nil
		case "enter":
			idx := m.list.selectedIndex()
			if idx < 0 {
				return m, nil
			}
			c := m.candidates[idx]
			m.chosen = &c
			return m, tea.Quit
		}
		cmd := m.list.editQuery(msg)
		return m, cmd
	}

	cmd := m.list.editQuery(msg)
	return m, cmd
}

// View renders the title, the directory in question, the list, and the keys.
func (m workspacePickerModel) View() string {
	var b strings.Builder
	b.WriteString(headerBarStyle.Render(workspacePickerTitle))
	b.WriteString("\n\n")
	b.WriteString(descStyle.Render(fmt.Sprintf("%d workspaces already have %s checked out. Choose one to return to:", len(m.candidates), m.dir)))
	b.WriteString("\n\n")
	b.WriteString(m.list.view("no workspaces match"))
	b.WriteString("\n")
	b.WriteString(footerStyle.Render("enter focus · esc cancel (nothing is created either way)"))
	return lipgloss.NewStyle().Render(b.String())
}

// projectsPickFlag is the explicit escape hatch: `projects --pick` always opens
// the project picker, whatever context it was invoked from.
const projectsPickFlag = "--pick"

// parseProjectsArgs reads the Projects action's arguments, reporting whether the
// picker was explicitly asked for. Anything else is an error rather than a
// silently ignored argument — a typo in a keybinding or manifest entry should
// say so, not quietly do something else.
func parseProjectsArgs(args []string) (bool, error) {
	pick := false
	for _, a := range args {
		if a == projectsPickFlag && !pick {
			pick = true
			continue
		}
		return false, fmt.Errorf("usage: herdr-plus projects [%s] (got %q)", projectsPickFlag, strings.Join(args, " "))
	}
	return pick, nil
}

// warnf reports a non-fatal problem on stderr, where herdr captures it in the
// plugin log.
func warnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "herdr-plus: "+format+"\n", args...)
}
