//
// Date: 2026-06-15
// Author: Spicer Matthews (spicer@cloudmanic.com)
// Copyright: 2026 Cloudmanic Labs, LLC. All rights reserved.
//

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// herdrClient talks to the running herdr instance over its local IPC endpoint:
// a unix domain socket on macOS/Linux and a named pipe on Windows (dialHerdr
// hides the difference). The protocol is newline-delimited JSON: one request
// object per line, one response object per line. Each call opens a short-lived
// connection, writes a single request, and reads a single response. herdr
// injects HERDR_SOCKET_PATH into every plugin command, so this works whenever
// herdr runs us.
type herdrClient struct {
	socketPath string
	// Opt-in bound for headless ensure-worktree; existing callers retain their
	// transport behavior. Includes dial, write and response read time.
	timeout time.Duration
}

// newHerdrClient builds a client from the HERDR_SOCKET_PATH environment
// variable. It returns an error when the process is not running inside herdr.
func newHerdrClient() (*herdrClient, error) {
	path := os.Getenv("HERDR_SOCKET_PATH")
	if path == "" {
		return nil, errors.New("HERDR_SOCKET_PATH is not set; are you running inside herdr?")
	}
	return &herdrClient{socketPath: path}, nil
}

// request is one JSON-RPC-style message sent to herdr.
type request struct {
	ID     string         `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}

// herdrError carries the code and human message herdr returns on failure. It is
// returned as the error itself (wrapped) so a caller can tell one refusal from
// another with errors.As — "this directory is not a Git work tree" is a normal
// answer to some questions, while every other code is a real failure. The text
// is unchanged from when this was a formatted string.
type herdrError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error renders the failure the way herdr-plus has always reported it.
func (e *herdrError) Error() string {
	return fmt.Sprintf("herdr error %s: %s", e.Code, e.Message)
}

// herdrErrorCode returns the native error code inside err, or "" when err did
// not come from herdr.
func herdrErrorCode(err error) string {
	var he *herdrError
	if errors.As(err, &he) {
		return he.Code
	}
	return ""
}

// response is one JSON line returned by herdr. Exactly one of Result or Error
// is populated.
type response struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *herdrError     `json:"error"`
}

// call sends a single request over a fresh connection and decodes the result
// into out (which may be nil when the caller does not care about the payload).
func (c *herdrClient) call(method string, params map[string]any, out any) error {
	if c.timeout > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
		defer cancel()
		type reply struct {
			result json.RawMessage
			err    error
		}
		done := make(chan reply, 1)
		go func() {
			// Keep decoding private: a timeout must not leave a goroutine writing
			// into the caller's output after call returns.
			var result json.RawMessage
			err := c.callContext(ctx, method, params, &result)
			done <- reply{result, err}
		}()
		select {
		case <-ctx.Done():
			return fmt.Errorf("herdr IPC %s: %w (native outcome unknown; no retry)", method, ctx.Err())
		case r := <-done:
			if ctx.Err() != nil {
				return fmt.Errorf("herdr IPC %s: %w (native outcome unknown; no retry)", method, ctx.Err())
			}
			if r.err != nil {
				return r.err
			}
			if out != nil {
				if err := json.Unmarshal(r.result, out); err != nil {
					return fmt.Errorf("decode result: %w", err)
				}
			}
			return nil
		}
	}
	return c.callContext(context.Background(), method, params, out)
}

func (c *herdrClient) callContext(ctx context.Context, method string, params map[string]any, out any) error {
	conn, err := dialHerdr(c.socketPath)
	if err != nil {
		return fmt.Errorf("connect herdr IPC endpoint: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err := ctx.Err(); err != nil {
		return err
	}

	// json.Encoder.Encode appends a trailing newline, which is exactly the
	// framing herdr expects for each request.
	if err := json.NewEncoder(conn).Encode(request{ID: "herdr-plus", Method: method, Params: params}); err != nil {
		return fmt.Errorf("write request: %w", err)
	}

	var resp response
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&resp); err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if c.timeout > 0 && resp.ID != "herdr-plus" {
		return errors.New("malformed herdr response: request ID mismatch")
	}
	if out != nil {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("decode result: %w", err)
		}
	}
	return nil
}

// worktreeCreateParams builds the worktree.create payload. A blank branch is
// omitted so herdr can generate its native worktree/<name> branch.
func worktreeCreateParams(cwd, branch string, focus bool) map[string]any {
	params := map[string]any{
		"cwd":   cwd,
		"focus": focus,
	}
	if b := strings.TrimSpace(branch); b != "" {
		params["branch"] = b
	}
	return params
}

// worktreeCreate asks herdr to create a git worktree from the repo at cwd, on the
// given branch (blank lets herdr generate a worktree/<name> branch), and — when
// focus is true — switch to its new workspace. herdr fires a worktree.created
// event, which the worktree auto-layout handler reacts to; we do not read the
// response, so the layout is applied by that handler rather than here.
func (c *herdrClient) worktreeCreate(cwd, branch string, focus bool) error {
	return c.call("worktree.create", worktreeCreateParams(cwd, branch, focus), nil)
}

// paneSplitParams builds the pane.split payload. A zero ratio is omitted so herdr
// applies its own even split; any other value is the share of the space the target
// pane keeps. An empty cwd is omitted too, which leaves the new pane inheriting
// the directory of the pane it was split from.
func paneSplitParams(targetPaneID, direction string, ratio float64, cwd string, focus bool) map[string]any {
	params := map[string]any{
		"target_pane_id": targetPaneID,
		"direction":      direction,
		"focus":          focus,
	}
	if ratio > 0 {
		params["ratio"] = ratio
	}
	if strings.TrimSpace(cwd) != "" {
		params["cwd"] = cwd
	}
	return params
}

// paneSplit splits the target pane in the given direction ("down" for a new pane
// beneath it, "right" for one beside it), creating a new pane, and returns the
// new pane's id. ratio is the share of the space the target pane keeps, zero for
// an even split. cwd is the directory the new pane starts in; empty inherits the
// split pane's. When focus is true the new pane becomes the focused pane (the
// socket API does not focus new panes by default).
func (c *herdrClient) paneSplit(targetPaneID, direction string, ratio float64, cwd string, focus bool) (string, error) {
	var out struct {
		Pane struct {
			PaneID string `json:"pane_id"`
		} `json:"pane"`
	}
	err := c.call("pane.split", paneSplitParams(targetPaneID, direction, ratio, cwd, focus), &out)
	if err != nil {
		return "", err
	}
	return out.Pane.PaneID, nil
}

// sendInput types text into a pane and then presses the given keys, as if at the
// keyboard. The keys are herdr key names (e.g. "Enter") delivered as real key
// events. To RUN a shell command, pass the command as text and "Enter" as the
// sole key — do not embed a trailing newline in text. herdr's send_input treats
// text as a paste: once the shell's line editor (zsh ZLE) is active it inserts an
// embedded "\n" literally instead of executing the line, so the command would
// just sit at the prompt until the user pressed Enter by hand. A real Enter key
// always submits, which is also how herdr's own `pane run` works. Pass no keys
// for plain typing with no submission.
func (c *herdrClient) sendInput(paneID, text string, keys ...string) error {
	params := map[string]any{
		"pane_id": paneID,
		"text":    text,
	}
	if len(keys) > 0 {
		params["keys"] = keys
	}
	return c.call("pane.send_input", params, nil)
}

// paneRead returns the text currently shown in a pane. source selects which slice
// of the terminal to read ("visible" for the on-screen rows, "recent" for the
// recent scrollback); lines caps how many trailing lines come back. It exists so
// callers can confirm a command actually ran — its output appeared — rather than
// merely being typed at the prompt.
func (c *herdrClient) paneRead(paneID, source string, lines int) (string, error) {
	var out struct {
		Read struct {
			Text string `json:"text"`
		} `json:"read"`
	}
	err := c.call("pane.read", map[string]any{
		"pane_id": paneID,
		"source":  source,
		"lines":   lines,
	}, &out)
	if err != nil {
		return "", err
	}
	return out.Read.Text, nil
}

// keyCtrlU clears the shell's pending input line (backward-kill-line, bound in
// both zsh's emacs and vi-insert keymaps).
const keyCtrlU = "ctrl+u"

// runCommand types command into a freshly created pane and submits it, pacing
// itself to the shell's startup so the command actually runs instead of sitting
// unsubmitted at the prompt. There are two startup races it has to dodge:
//
//   - Typing too early: a pane created moments ago may not have an interactive
//     shell yet; keystrokes sent into that gap are dropped — often just the
//     first character ("claude" lands as "laude"). Startup output also paints
//     in bursts, so readiness means a quiescent screen, not the first paint.
//   - Submitting too early: even once typing lands, pressing Enter before the
//     shell's line editor has the text races startup and the line is lost.
//
// So Enter is only pressed once the command visibly echoes back intact; a
// mangled echo means the line is cleared (ctrl+u) and retyped, a few attempts.
// Submission is a real Enter key, never a trailing "\n" in the text — herdr
// pastes text, and an embedded newline is inserted literally once zsh's line
// editor is active rather than running the line (see sendInput). Every wait is
// best effort: once the retries are exhausted the last attempt is submitted
// blindly, so a shell that never echoes (or a very slow one) degrades to the
// old behavior rather than hanging.
func (c *herdrClient) runCommand(paneID, command string) error {
	// 1. Wait for the shell to be ready to receive input (its prompt is drawn).
	c.waitForPaneReady(paneID, 10*time.Second)

	probe := commandEchoProbe(command)
	const maxAttempts = 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			// The previous attempt left a mangled line at the prompt (e.g. a
			// leading character swallowed by shell startup). Clear it before
			// retyping. Best effort.
			_ = c.sendInput(paneID, "", keyCtrlU)
			time.Sleep(200 * time.Millisecond)
		}

		// 2. Type the command (no trailing newline).
		if err := c.sendInput(paneID, command); err != nil {
			return err
		}

		// 3. Only submit once the command visibly echoes back intact, proving
		// the line editor accepted every character.
		if c.waitForPaneText(paneID, probe, 3*time.Second) {
			return c.sendInput(paneID, "", "Enter")
		}
	}

	// Every verification failed — the pane may simply not echo (unusual shell,
	// full-screen program). Degrade to the old blind behavior rather than give
	// up: the last typed attempt is still at the prompt, submit it.
	return c.sendInput(paneID, "", "Enter")
}

// commandEchoProbe returns a short, stable fragment of a command to look for when
// confirming it was typed at the prompt: the first line, capped to a few
// characters so it stays on a single terminal row. A long command wraps across
// rows, so matching the whole string against the rendered screen would fail; a
// short leading fragment does not wrap and is specific enough on an otherwise
// empty fresh pane.
func commandEchoProbe(command string) string {
	probe := command
	if i := strings.IndexByte(probe, '\n'); i >= 0 {
		probe = probe[:i]
	}
	if len(probe) > 12 {
		probe = probe[:12]
	}
	return strings.TrimSpace(probe)
}

// waitForPaneReady blocks until the pane's visible content is non-blank AND
// quiescent — two consecutive identical reads ~200ms apart — or the timeout
// elapses. A fresh pane's shell paints in bursts while it starts up (compinit,
// plugin managers, prompt frameworks), and keystrokes sent into that window can
// be swallowed; first-paint is too early a signal, a settled screen is not.
// Best effort: a timeout just means we stop waiting and proceed.
func (c *herdrClient) waitForPaneReady(paneID string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	prev := ""
	for time.Now().Before(deadline) {
		text, err := c.paneRead(paneID, "visible", 10)
		if err == nil && strings.TrimSpace(text) != "" && text == prev {
			return
		}
		prev = text
		time.Sleep(200 * time.Millisecond)
	}
}

// waitForPaneText blocks until the pane's visible text contains probe, reporting
// whether it appeared before the timeout. An empty probe matches immediately.
func (c *herdrClient) waitForPaneText(paneID, probe string, timeout time.Duration) bool {
	if probe == "" {
		return true
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if text, err := c.paneRead(paneID, "visible", 20); err == nil && strings.Contains(text, probe) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// closePane terminates a pane and frees its terminal. Closing the focused pane
// returns focus to an adjacent pane.
func (c *herdrClient) closePane(paneID string) error {
	return c.call("pane.close", map[string]any{
		"pane_id": paneID,
	}, nil)
}

// paneInfo is the subset of herdr's pane metadata herdr-plus uses.
type paneInfo struct {
	PaneID        string `json:"pane_id"`
	TabID         string `json:"tab_id"`
	WorkspaceID   string `json:"workspace_id"`
	TerminalID    string `json:"terminal_id"`
	Cwd           string `json:"cwd"`
	ForegroundCwd string `json:"foreground_cwd"`
	Agent         string `json:"agent"`
	AgentSession  struct {
		Value string `json:"value"`
	} `json:"agent_session"`
}

// focusedPaneID returns the id of the currently focused pane. It is used when
// herdr-plus is launched outside a pane's own shell — for example from a
// keybinding or action, which run server-side and may not set HERDR_PANE_ID.
func (c *herdrClient) focusedPaneID() (string, error) {
	var out struct {
		Panes []struct {
			PaneID  string `json:"pane_id"`
			Focused bool   `json:"focused"`
		} `json:"panes"`
	}
	if err := c.call("pane.list", map[string]any{}, &out); err != nil {
		return "", err
	}
	for _, p := range out.Panes {
		if p.Focused {
			return p.PaneID, nil
		}
	}
	return "", errors.New("no focused pane")
}

// workspacePanes returns a validated inventory for one explicit workspace.
// Incomplete or duplicate identities cannot authorize applying a fresh layout.
func (c *herdrClient) workspacePanes(workspaceID string) ([]paneInfo, error) {
	var out struct {
		Panes *[]paneInfo `json:"panes"`
	}
	if err := c.call("pane.list", map[string]any{"workspace_id": workspaceID}, &out); err != nil {
		return nil, err
	}
	if out.Panes == nil {
		return nil, errors.New("malformed pane.list result: no panes array")
	}
	seen := make(map[string]bool)
	for _, pane := range *out.Panes {
		if pane.WorkspaceID != workspaceID ||
			!workspaceIDPattern.MatchString(pane.PaneID) ||
			!workspaceIDPattern.MatchString(pane.TabID) || seen[pane.PaneID] {
			return nil, errors.New("malformed pane.list result: incomplete, unrelated or duplicate pane identity")
		}
		seen[pane.PaneID] = true
	}
	return *out.Panes, nil
}

// paneGet fetches metadata for a single pane, including its working directory
// and the tab/workspace it belongs to.
func (c *herdrClient) paneGet(paneID string) (paneInfo, error) {
	var out struct {
		Pane paneInfo `json:"pane"`
	}
	err := c.call("pane.get", map[string]any{"pane_id": paneID}, &out)
	return out.Pane, err
}

// tabInfo is the subset of herdr's tab metadata herdr-plus uses.
type tabInfo struct {
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	Focused     bool   `json:"focused"`
}

// tabGet fetches metadata for a single tab, notably its human label.
func (c *herdrClient) tabGet(tabID string) (tabInfo, error) {
	var out struct {
		Tab tabInfo `json:"tab"`
	}
	err := c.call("tab.get", map[string]any{"tab_id": tabID}, &out)
	return out.Tab, err
}

// tabList returns the tabs of one workspace, in herdr's own order. It is how the
// Projects action finds the task tab to return to: labels are read from live
// native metadata rather than remembered, so a tab the user renamed or closed is
// seen as it is now.
// A missing or null tabs array is an error for the same reason workspaceList
// rejects one: the caller reads an empty list as "that tab is not there", which
// reports a renamed or closed tab to the user. A present, empty array is a
// legitimate answer.
func (c *herdrClient) tabList(workspaceID string) ([]tabInfo, error) {
	var out struct {
		Tabs *[]tabInfo `json:"tabs"`
	}
	if err := c.call("tab.list", map[string]any{"workspace_id": workspaceID}, &out); err != nil {
		return nil, err
	}
	if out.Tabs == nil {
		return nil, errors.New("malformed tab.list result: no tabs array")
	}
	for i, t := range *out.Tabs {
		if strings.TrimSpace(t.TabID) == "" {
			return nil, fmt.Errorf("malformed tab.list result: tab %d has no tab_id", i+1)
		}
	}
	return *out.Tabs, nil
}

// tabFocus brings an existing tab to the front. It changes focus only: no pane
// is created, resized, relaid or written to, so returning to a tab leaves
// whatever is running in it exactly as it was.
func (c *herdrClient) tabFocus(tabID string) error {
	return c.call("tab.focus", map[string]any{"tab_id": tabID}, nil)
}

// worktreeProvenance is the checkout herdr records for a workspace: which
// repository it belongs to and, crucially, which checkout of it. CheckoutPath is
// the only identity herdr-plus matches on — RepoKey is shared by every worktree
// of a repository, so two sibling worktrees would collide under it, and a Label
// is a display string the user can change.
type worktreeProvenance struct {
	RepoKey          string `json:"repo_key"`
	RepoName         string `json:"repo_name"`
	RepoRoot         string `json:"repo_root"`
	CheckoutPath     string `json:"checkout_path"`
	IsLinkedWorktree bool   `json:"is_linked_worktree"`
}

// workspaceInfo is the subset of herdr's workspace metadata herdr-plus uses.
// Worktree is nil for a workspace herdr has no checkout provenance for (a plain
// folder), which is never treated as a match for anything.
type workspaceInfo struct {
	WorkspaceID string              `json:"workspace_id"`
	Label       string              `json:"label"`
	Focused     bool                `json:"focused"`
	Worktree    *worktreeProvenance `json:"worktree"`
}

// workspaceGet fetches metadata for a single workspace, notably its label —
// which herdr derives from the repo or folder name.
func (c *herdrClient) workspaceGet(workspaceID string) (workspaceInfo, error) {
	var out struct {
		Workspace workspaceInfo `json:"workspace"`
	}
	err := c.call("workspace.get", map[string]any{"workspace_id": workspaceID}, &out)
	return out.Workspace, err
}

// workspaceList returns every open workspace with the checkout provenance herdr
// holds for it. It is the inventory the "is this project already open?" question
// is answered from — herdr's own record of what each workspace checks out, not a
// registry herdr-plus keeps for itself.
//
// A successful but malformed reply is an error, never an empty inventory. The
// difference matters: the caller reads "no workspaces" as "nothing is open yet"
// and creates one, so a missing or null array — which plain decoding would hand
// back as an empty slice — would quietly produce the duplicate workspace the
// whole feature exists to prevent. An array that is present and genuinely empty
// is fine, and means what it says.
func (c *herdrClient) workspaceList() ([]workspaceInfo, error) {
	var out struct {
		// A pointer distinguishes "herdr sent an empty list" from "herdr sent no
		// list at all"; both decode to a nil slice otherwise.
		Workspaces *[]workspaceInfo `json:"workspaces"`
	}
	if err := c.call("workspace.list", map[string]any{}, &out); err != nil {
		return nil, err
	}
	if out.Workspaces == nil {
		return nil, errors.New("malformed workspace.list result: no workspaces array")
	}
	for i, ws := range *out.Workspaces {
		if strings.TrimSpace(ws.WorkspaceID) == "" {
			return nil, fmt.Errorf("malformed workspace.list result: workspace %d has no workspace_id", i+1)
		}
		// Provenance is optional (a plain folder has none), but a provenance block
		// that is present has to be usable as identity — an absolute checkout path.
		// Anything else cannot be compared, and must not be silently skipped over.
		if ws.Worktree == nil {
			continue
		}
		if path := strings.TrimSpace(ws.Worktree.CheckoutPath); path == "" || !filepath.IsAbs(path) {
			return nil, fmt.Errorf("malformed workspace.list result: workspace %s reports checkout path %q, which is not an absolute path", ws.WorkspaceID, ws.Worktree.CheckoutPath)
		}
	}
	return *out.Workspaces, nil
}

// workspaceFocus switches to an existing workspace. Like tabFocus it only moves
// focus: the workspace's tabs, panes and running processes are untouched.
func (c *herdrClient) workspaceFocus(workspaceID string) error {
	return c.call("workspace.focus", map[string]any{"workspace_id": workspaceID}, nil)
}

// workspaceCreate makes a brand-new workspace rooted at cwd with the given
// label, and returns the ids of the new workspace, its single root tab, and
// that tab's root pane. When focus is true the workspace becomes the active one
// (the user is switched to it); pass false to create it in the background.
func (c *herdrClient) workspaceCreate(cwd, label string, focus bool) (workspaceID, tabID, paneID string, err error) {
	var out struct {
		Workspace struct {
			WorkspaceID string `json:"workspace_id"`
		} `json:"workspace"`
		Tab struct {
			TabID string `json:"tab_id"`
		} `json:"tab"`
		RootPane struct {
			PaneID string `json:"pane_id"`
		} `json:"root_pane"`
	}
	err = c.call("workspace.create", map[string]any{
		"cwd":   cwd,
		"label": label,
		"focus": focus,
	}, &out)
	if err != nil {
		return "", "", "", err
	}
	return out.Workspace.WorkspaceID, out.Tab.TabID, out.RootPane.PaneID, nil
}

// worktreeEntry is one checkout Git has registered for a repository, as herdr
// reports it. OpenWorkspaceID is the workspace herdr considers that checkout
// open in, if any — herdr's own binding, including workspaces it has no stored
// checkout provenance for, which is exactly the case herdr-plus cannot see
// through workspace.list.
type worktreeEntry struct {
	Path             string  `json:"path"`
	Branch           *string `json:"branch"`
	IsBare           bool    `json:"is_bare"`
	IsDetached       bool    `json:"is_detached"`
	IsPrunable       bool    `json:"is_prunable"`
	IsLinkedWorktree bool    `json:"is_linked_worktree"`
	OpenWorkspaceID  *string `json:"open_workspace_id"`
	Label            string  `json:"label"`
}

// worktreeList asks herdr which checkouts of cwd's repository exist and which
// are already open. It is read-only: it registers nothing and opens nothing.
//
// cwd may be any checkout of the repository, linked or primary. A directory
// that is not inside a Git work tree comes back as the native "not_git_worktree"
// refusal, which callers treat as "this project is not a Git checkout" rather
// than as a failure.
//
// As with workspaceList, a missing or null array is an error, not an empty
// inventory: the caller uses emptiness to decide that nothing is open.
func (c *herdrClient) worktreeList(cwd string) ([]worktreeEntry, error) {
	var out struct {
		Worktrees *[]worktreeEntry `json:"worktrees"`
	}
	if err := c.call("worktree.list", map[string]any{"cwd": cwd}, &out); err != nil {
		return nil, err
	}
	if out.Worktrees == nil {
		return nil, errors.New("malformed worktree.list result: no worktrees array")
	}
	for i, entry := range *out.Worktrees {
		if !filepath.IsAbs(strings.TrimSpace(entry.Path)) {
			return nil, fmt.Errorf("malformed worktree.list result: worktree %d reports path %q, which is not an absolute path", i+1, entry.Path)
		}
	}
	return *out.Worktrees, nil
}

// worktreeOpenResult is the part of worktree.open herdr-plus checks.
type worktreeOpenResult struct {
	WorkspaceID string
	Path        string
	AlreadyOpen bool
}

// worktreeOpenPath asks herdr to open an already-registered checkout by its
// path. herdr binds the checkout to the workspace it is already open in when
// there is one — reporting already_open — and creates a workspace only when
// there is not. Panes in an already-open workspace are left untouched.
//
// It is addressed by path, never by branch: a path names exactly one registered
// checkout, works for a detached HEAD, and cannot be ambiguous the way a branch
// with several checkouts can. sourceCwd must be the repository's primary
// checkout — herdr refuses a linked worktree as the source of a worktree action.
//
// This does not create checkouts or branches. The one caller uses it to attach
// native checkout provenance to a workspace it just created.
func (c *herdrClient) worktreeOpenPath(sourceCwd, path string, focus bool) (worktreeOpenResult, error) {
	var out struct {
		Workspace struct {
			WorkspaceID string `json:"workspace_id"`
		} `json:"workspace"`
		Worktree struct {
			Path string `json:"path"`
		} `json:"worktree"`
		AlreadyOpen *bool `json:"already_open"`
	}
	if err := c.call("worktree.open", map[string]any{
		"cwd":   sourceCwd,
		"path":  path,
		"focus": focus,
	}, &out); err != nil {
		return worktreeOpenResult{}, err
	}
	if out.AlreadyOpen == nil {
		return worktreeOpenResult{}, errors.New("malformed worktree.open result: no already_open flag")
	}
	return worktreeOpenResult{
		WorkspaceID: out.Workspace.WorkspaceID,
		Path:        out.Worktree.Path,
		AlreadyOpen: *out.AlreadyOpen,
	}, nil
}

// tabCreateParams builds the tab.create payload. An empty cwd is omitted so the
// tab's root pane inherits the workspace's directory, which is what a tab that
// declares no working_dir of its own should do.
func tabCreateParams(workspaceID, label, cwd string, focus bool) map[string]any {
	params := map[string]any{
		"workspace_id": workspaceID,
		"label":        label,
		"focus":        focus,
	}
	if strings.TrimSpace(cwd) != "" {
		params["cwd"] = cwd
	}
	return params
}

// tabCreate adds a tab to an existing workspace and returns the new tab's id and
// its root pane's id. cwd is the directory the tab's root pane starts in; empty
// inherits the workspace's. focus controls whether the new tab is brought to the
// front — a project's later tabs are created with focus=false so the first tab
// stays active while the rest spin up behind it.
func (c *herdrClient) tabCreate(workspaceID, label, cwd string, focus bool) (tabID, paneID string, err error) {
	var out struct {
		Tab struct {
			TabID string `json:"tab_id"`
		} `json:"tab"`
		RootPane struct {
			PaneID string `json:"pane_id"`
		} `json:"root_pane"`
	}
	err = c.call("tab.create", tabCreateParams(workspaceID, label, cwd, focus), &out)
	if err != nil {
		return "", "", err
	}
	return out.Tab.TabID, out.RootPane.PaneID, nil
}

// tabRename changes a tab's human label. A freshly created workspace's root tab
// is named "1"; callers rename it to the first tab's name.
func (c *herdrClient) tabRename(tabID, label string) error {
	return c.call("tab.rename", map[string]any{
		"tab_id": tabID,
		"label":  label,
	}, nil)
}

// tabClose removes a tab and everything in it. Its only caller replaces a
// workspace's root tab with one rooted at a different directory: herdr has no way
// to change a tab's directory after the fact, so the tab is rebuilt and the
// original closed.
func (c *herdrClient) tabClose(tabID string) error {
	return c.call("tab.close", map[string]any{
		"tab_id": tabID,
	}, nil)
}

// paneRename sets a pane's human label — the name shown on pane borders and
// in pane lists.
func (c *herdrClient) paneRename(paneID, label string) error {
	return c.call("pane.rename", map[string]any{
		"pane_id": paneID,
		"label":   label,
	}, nil)
}

// workspaceClose tears down a whole workspace and all of its tabs and panes.
func (c *herdrClient) workspaceClose(workspaceID string) error {
	return c.call("workspace.close", map[string]any{
		"workspace_id": workspaceID,
	}, nil)
}
