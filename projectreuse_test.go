//
// Date: 2026-09-09
// Author: Spicer Matthews (spicer@cloudmanic.com)
// Copyright: 2026 Cloudmanic Labs, LLC. All rights reserved.
//

package main

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeHerdr is an in-process stand-in for a running herdr: a real unix socket
// speaking the same newline-delimited JSON protocol herdrClient writes, so these
// tests exercise the actual client, the actual request payloads, and the actual
// result decoding rather than a hand-mocked call layer. Every request is
// recorded, so a test can assert not only what happened but — just as important
// for reuse — that nothing was created, split, laid out or typed into.
//
// It is synthetic: no herdr instance, window, pane or workspace is touched. The
// live behavior of the real methods is root's native canary, not this suite's.
type fakeHerdr struct {
	t        *testing.T
	ln       net.Listener
	mu       sync.Mutex
	calls    []request
	handlers map[string]func(request) (any, *herdrError)
	done     chan struct{}
}

// startFakeHerdr listens on a short temporary socket path (the macOS sun_path
// limit is unforgiving) and points HERDR_SOCKET_PATH at it, so newHerdrClient
// connects to this fake exactly as it would to herdr.
func startFakeHerdr(t *testing.T) *fakeHerdr {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("synthetic unix-socket IPC boundary; the Windows named-pipe path is a separate native gate")
	}
	dir, err := os.MkdirTemp("", "hp-")
	if err != nil {
		t.Fatalf("temp socket dir: %v", err)
	}
	socket := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeHerdr{t: t, ln: ln, handlers: map[string]func(request) (any, *herdrError){}, done: make(chan struct{})}
	go f.serve()
	t.Setenv("HERDR_SOCKET_PATH", socket)
	t.Cleanup(func() {
		ln.Close()
		<-f.done
		os.RemoveAll(dir)
	})
	return f
}

// serve accepts one request per connection, exactly as herdr does.
func (f *fakeHerdr) serve() {
	defer close(f.done)
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
		var req request
		if err := json.NewDecoder(conn).Decode(&req); err == nil {
			f.mu.Lock()
			f.calls = append(f.calls, req)
			handler := f.handlers[req.Method]
			f.mu.Unlock()
			resp := response{ID: req.ID}
			if handler == nil {
				resp.Error = &herdrError{Code: "unknown_method", Message: "no handler for " + req.Method}
			} else if result, herr := handler(req); herr != nil {
				resp.Error = herr
			} else {
				raw, mErr := json.Marshal(result)
				if mErr != nil {
					f.t.Errorf("marshal %s result: %v", req.Method, mErr)
				}
				resp.Result = raw
			}
			_ = json.NewEncoder(conn).Encode(resp)
		}
		conn.Close()
	}
}

// handle registers a canned result for a method.
func (f *fakeHerdr) handle(method string, result any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[method] = func(request) (any, *herdrError) { return result, nil }
}

// handleFunc registers a handler that can inspect the request or fail.
func (f *fakeHerdr) handleFunc(method string, fn func(request) (any, *herdrError)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[method] = fn
}

// fail makes a method return a herdr error, standing in for a native lookup
// that cannot answer.
func (f *fakeHerdr) fail(method, message string) {
	f.handleFunc(method, func(request) (any, *herdrError) {
		return nil, &herdrError{Code: "native_failure", Message: message}
	})
}

// methods returns the methods called so far, in order.
func (f *fakeHerdr) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.Method
	}
	return out
}

// paramsFor returns the params of the single call to method, failing the test
// when it was not called exactly once.
func (f *fakeHerdr) paramsFor(method string) map[string]any {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var found []map[string]any
	for _, c := range f.calls {
		if c.Method == method {
			found = append(found, c.Params)
		}
	}
	if len(found) != 1 {
		f.t.Fatalf("want exactly one %s call, got %d (calls: %v)", method, len(found), f.calls)
	}
	return found[0]
}

// client builds a real herdrClient pointed at this fake.
func (f *fakeHerdr) client() *herdrClient {
	f.t.Helper()
	c, err := newHerdrClient()
	if err != nil {
		f.t.Fatalf("newHerdrClient: %v", err)
	}
	return c
}

// mutatingMethods are every native call that creates, reflows, or types into
// something. Reuse and failure paths must issue none of them: returning to work
// that already exists must never rebuild or disturb it.
var mutatingMethods = []string{
	"workspace.create", "workspace.close", "worktree.create", "worktree.open",
	"tab.create", "tab.close", "tab.rename", "pane.split", "pane.close",
	"pane.rename", "pane.send_input", "pane.read", "layout.apply", "pane.zoom",
}

// assertNoMutations fails when any workspace/tab/pane was created, destroyed,
// relaid, or sent input.
func (f *fakeHerdr) assertNoMutations() {
	f.t.Helper()
	for _, m := range f.methods() {
		for _, bad := range mutatingMethods {
			if m == bad {
				f.t.Fatalf("unexpected mutating native call %q; calls: %v", m, f.methods())
			}
		}
	}
}

// assertNotCalled fails when method was called at all — used to prove the
// action never falls back to another client's focused pane (pane.list).
func (f *fakeHerdr) assertNotCalled(method string) {
	f.t.Helper()
	for _, m := range f.methods() {
		if m == method {
			f.t.Fatalf("native call %q must not happen; calls: %v", method, f.methods())
		}
	}
}

// wsInfo builds one workspace.list / workspace.get entry with checkout
// provenance, the way herdr reports a workspace opened on a git checkout.
func wsInfo(id, label, checkout string, linked bool) map[string]any {
	return map[string]any{
		"workspace_id": id,
		"number":       1,
		"label":        label,
		"focused":      false,
		"pane_count":   1,
		"tab_count":    1,
		"worktree": map[string]any{
			"repo_key":           "repo-key",
			"repo_name":          "repo",
			"repo_root":          "/repo",
			"checkout_path":      checkout,
			"is_linked_worktree": linked,
		},
	}
}

// wsInfoNoProvenance builds a workspace herdr reports without any checkout
// provenance — a plain folder workspace, which must never be adopted as a match.
func wsInfoNoProvenance(id, label string) map[string]any {
	return map[string]any{
		"workspace_id": id,
		"number":       1,
		"label":        label,
		"focused":      false,
		"pane_count":   1,
		"tab_count":    1,
	}
}

// tabInfoJSON builds one tab.list entry.
func tabInfoJSON(id, workspaceID, label string) map[string]any {
	return map[string]any{
		"tab_id":       id,
		"workspace_id": workspaceID,
		"number":       1,
		"label":        label,
		"focused":      false,
		"pane_count":   1,
	}
}

// setPluginContext installs a HERDR_PLUGIN_CONTEXT_JSON describing the pane the
// action fired from — the explicit, per-invocation context herdr injects.
func setPluginContext(t *testing.T, ctx map[string]any) {
	t.Helper()
	raw, err := json.Marshal(ctx)
	if err != nil {
		t.Fatalf("marshal plugin context: %v", err)
	}
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", string(raw))
}

// linkedContext is the common case: the action fired from a pane in a linked
// worktree workspace.
func linkedContext(t *testing.T, workspaceID, paneID, checkout string) {
	t.Helper()
	setPluginContext(t, map[string]any{
		"workspace_id":    workspaceID,
		"workspace_label": "repo · branch",
		"workspace_cwd":   checkout,
		"focused_pane_id": paneID,
		"worktree": map[string]any{
			"repo_key":           "repo-key",
			"repo_name":          "repo",
			"repo_root":          "/repo",
			"checkout_path":      checkout,
			"is_linked_worktree": true,
		},
	})
}

// paneInfoJSON builds a pane.get result for the pane the context names.
func paneInfoJSON(paneID, tabID, workspaceID string) map[string]any {
	return map[string]any{
		"pane": map[string]any{
			"pane_id":      paneID,
			"tab_id":       tabID,
			"workspace_id": workspaceID,
			"terminal_id":  "t1",
			"cwd":          "/checkout",
		},
	}
}

// crewFixture wires the fake native side for a linked worktree workspace whose
// tabs are the given labels, and installs the matching injected context.
func crewFixture(t *testing.T, labels ...string) *fakeHerdr {
	t.Helper()
	f := startFakeHerdr(t)
	linkedContext(t, "w1", "w1:p1", "/checkout")
	f.handle("workspace.get", map[string]any{"workspace": wsInfo("w1", "repo · branch", "/checkout", true)})
	f.handle("pane.get", paneInfoJSON("w1:p1", "w1:t1", "w1"))
	tabs := make([]any, len(labels))
	for i, l := range labels {
		tabs[i] = tabInfoJSON("w1:t"+string(rune('1'+i)), "w1", l)
	}
	f.handle("tab.list", map[string]any{"tabs": tabs})
	f.handle("tab.focus", map[string]any{"type": "tab_focused"})
	return f
}

// TestReturnToCrewFocusesTheExactTab is the core contextual-P behavior: fired
// inside a linked worktree workspace whose Crew tab exists exactly once, the
// action focuses that tab and does nothing else — no picker, no creation, no
// layout, and no input into whatever is running there.
func TestReturnToCrewFocusesTheExactTab(t *testing.T) {
	f := crewFixture(t, "Editor", "Crew", "Logs")

	pc, err := pluginContextFromEnv()
	if err != nil {
		t.Fatalf("pluginContextFromEnv: %v", err)
	}
	focused, err := returnToCrew(f.client(), pc, "Crew")
	if err != nil {
		t.Fatalf("returnToCrew: %v", err)
	}
	if !focused {
		t.Fatal("returnToCrew reported not handled; want the Crew tab focused")
	}
	if got := f.paramsFor("tab.focus")["tab_id"]; got != "w1:t2" {
		t.Fatalf("focused tab_id = %v, want w1:t2 (the one tab labeled Crew)", got)
	}
	if got := f.paramsFor("workspace.get")["workspace_id"]; got != "w1" {
		t.Fatalf("workspace.get workspace_id = %v, want the injected w1", got)
	}
	if got := f.paramsFor("tab.list")["workspace_id"]; got != "w1" {
		t.Fatalf("tab.list workspace_id = %v, want the injected w1", got)
	}
	f.assertNoMutations()
	// Identity comes from the injected context, never from whatever pane another
	// client happens to have focused.
	f.assertNotCalled("pane.list")
}

// TestReturnToCrewIsIdempotent confirms pressing P again from the same context
// simply focuses the same tab: no second Crew, no layout, no drift.
func TestReturnToCrewIsIdempotent(t *testing.T) {
	f := crewFixture(t, "Crew")
	pc, err := pluginContextFromEnv()
	if err != nil {
		t.Fatalf("pluginContextFromEnv: %v", err)
	}
	for i := 0; i < 3; i++ {
		focused, err := returnToCrew(f.client(), pc, "Crew")
		if err != nil || !focused {
			t.Fatalf("attempt %d: focused=%v err=%v", i+1, focused, err)
		}
	}
	f.assertNoMutations()
	focuses := 0
	for _, m := range f.methods() {
		if m == "tab.focus" {
			focuses++
		}
	}
	if focuses != 3 {
		t.Fatalf("tab.focus calls = %d, want 3 (one per press, each a no-op re-focus)", focuses)
	}
}

// TestReturnToCrewIgnoresOtherClientsFocus proves the workspace another client
// is looking at — even one that also has a Crew tab — cannot capture P. The
// injected context is the only identity source.
func TestReturnToCrewIgnoresOtherClientsFocus(t *testing.T) {
	f := startFakeHerdr(t)
	linkedContext(t, "w1", "w1:p1", "/checkout")
	f.handleFunc("workspace.get", func(req request) (any, *herdrError) {
		if req.Params["workspace_id"] != "w1" {
			return nil, &herdrError{Code: "not_found", Message: "wrong workspace"}
		}
		return map[string]any{"workspace": wsInfo("w1", "repo · branch", "/checkout", true)}, nil
	})
	f.handle("pane.get", paneInfoJSON("w1:p1", "w1:t1", "w1"))
	f.handleFunc("tab.list", func(req request) (any, *herdrError) {
		if req.Params["workspace_id"] != "w1" {
			return nil, &herdrError{Code: "not_found", Message: "wrong workspace"}
		}
		return map[string]any{"tabs": []any{tabInfoJSON("w1:t9", "w1", "Crew")}}, nil
	})
	f.handle("tab.focus", map[string]any{})
	// Another client is focused on a different workspace that also has a Crew tab.
	f.handle("pane.list", map[string]any{"panes": []any{map[string]any{"pane_id": "w2:p1", "focused": true, "workspace_id": "w2"}}})

	pc, _ := pluginContextFromEnv()
	focused, err := returnToCrew(f.client(), pc, "Crew")
	if err != nil || !focused {
		t.Fatalf("focused=%v err=%v; want the injected workspace's Crew tab", focused, err)
	}
	if got := f.paramsFor("tab.focus")["tab_id"]; got != "w1:t9" {
		t.Fatalf("focused tab_id = %v, want w1:t9", got)
	}
	f.assertNotCalled("pane.list")
}

// TestReturnToCrewDiagnostics covers every case where identity or the Crew tab
// cannot be established: each must fail visibly, create nothing, and never be
// silently downgraded to "not linked" (which would open a picker and, one
// selection later, a duplicate workspace).
func TestReturnToCrewDiagnostics(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T) *fakeHerdr
		wantErr string
	}{
		{
			name:    "renamed or missing Crew tab",
			setup:   func(t *testing.T) *fakeHerdr { return crewFixture(t, "Editor", "crew-old") },
			wantErr: "no tab labeled",
		},
		{
			name:    "duplicate Crew tabs",
			setup:   func(t *testing.T) *fakeHerdr { return crewFixture(t, "Crew", "Crew") },
			wantErr: "2 tabs labeled",
		},
		{
			name: "workspace lookup failure is not 'not linked'",
			setup: func(t *testing.T) *fakeHerdr {
				f := startFakeHerdr(t)
				linkedContext(t, "w1", "w1:p1", "/checkout")
				f.fail("workspace.get", "socket exploded")
				return f
			},
			wantErr: "look up workspace",
		},
		{
			name: "tab lookup failure is not 'no Crew tab'",
			setup: func(t *testing.T) *fakeHerdr {
				f := startFakeHerdr(t)
				linkedContext(t, "w1", "w1:p1", "/checkout")
				f.handle("workspace.get", map[string]any{"workspace": wsInfo("w1", "repo", "/checkout", true)})
				f.handle("pane.get", paneInfoJSON("w1:p1", "w1:t1", "w1"))
				f.fail("tab.list", "socket exploded")
				return f
			},
			wantErr: "list tabs",
		},
		{
			name: "native answers about a different workspace",
			setup: func(t *testing.T) *fakeHerdr {
				f := startFakeHerdr(t)
				linkedContext(t, "w1", "w1:p1", "/checkout")
				f.handle("workspace.get", map[string]any{"workspace": wsInfo("w2", "other", "/other", true)})
				return f
			},
			wantErr: "identity",
		},
		{
			name: "context pane belongs to another workspace",
			setup: func(t *testing.T) *fakeHerdr {
				f := startFakeHerdr(t)
				linkedContext(t, "w1", "w1:p1", "/checkout")
				f.handle("workspace.get", map[string]any{"workspace": wsInfo("w1", "repo", "/checkout", true)})
				f.handle("pane.get", paneInfoJSON("w1:p1", "w2:t1", "w2"))
				return f
			},
			wantErr: "pane",
		},
		{
			name: "context checkout no longer matches the live workspace",
			setup: func(t *testing.T) *fakeHerdr {
				f := startFakeHerdr(t)
				linkedContext(t, "w1", "w1:p1", "/checkout")
				f.handle("workspace.get", map[string]any{"workspace": wsInfo("w1", "repo", "/somewhere-else", true)})
				f.handle("pane.get", paneInfoJSON("w1:p1", "w1:t1", "w1"))
				return f
			},
			wantErr: "checkout",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := c.setup(t)
			pc, err := pluginContextFromEnv()
			if err != nil {
				t.Fatalf("pluginContextFromEnv: %v", err)
			}
			focused, err := returnToCrew(f.client(), pc, "Crew")
			if err == nil {
				t.Fatalf("want a visible diagnostic, got focused=%v and no error", focused)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("error %q does not mention %q", err, c.wantErr)
			}
			if focused {
				t.Fatal("a failed lookup must not report the action handled")
			}
			f.assertNoMutations()
		})
	}
}

// TestReturnToCrewFallsThroughToPicker covers the contexts that are simply not
// task Crews: they hand the action back so the normal project picker opens.
func TestReturnToCrewFallsThroughToPicker(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T) *fakeHerdr
	}{
		{
			name: "workspace is not a linked worktree",
			setup: func(t *testing.T) *fakeHerdr {
				f := startFakeHerdr(t)
				linkedContext(t, "w1", "w1:p1", "/checkout")
				f.handle("workspace.get", map[string]any{"workspace": wsInfo("w1", "repo", "/checkout", false)})
				f.handle("pane.get", paneInfoJSON("w1:p1", "w1:t1", "w1"))
				return f
			},
		},
		{
			name: "workspace has no checkout provenance at all",
			setup: func(t *testing.T) *fakeHerdr {
				f := startFakeHerdr(t)
				setPluginContext(t, map[string]any{"workspace_id": "w1", "focused_pane_id": "w1:p1"})
				f.handle("workspace.get", map[string]any{"workspace": wsInfoNoProvenance("w1", "notes")})
				f.handle("pane.get", paneInfoJSON("w1:p1", "w1:t1", "w1"))
				return f
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := c.setup(t)
			pc, _ := pluginContextFromEnv()
			focused, err := returnToCrew(f.client(), pc, "Crew")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if focused {
				t.Fatal("want the picker to open (handled=false)")
			}
			f.assertNoMutations()
			f.assertNotCalled("tab.focus")
		})
	}
}

// TestPluginContextFromEnv covers the three shapes of the injected context: a
// real one, an absent one (which is no identity at all, not an error), and a
// malformed one — which must be a visible error rather than a zero context that
// would look like "no workspace" and open a picker on a guess.
func TestPluginContextFromEnv(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", "")
		pc, err := pluginContextFromEnv()
		if err != nil {
			t.Fatalf("absent context must not be an error: %v", err)
		}
		if pc.WorkspaceID != "" {
			t.Fatalf("WorkspaceID = %q, want empty", pc.WorkspaceID)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", "{not json")
		if _, err := pluginContextFromEnv(); err == nil {
			t.Fatal("malformed context must be a visible error")
		}
	})

	t.Run("linked worktree", func(t *testing.T) {
		linkedContext(t, "w7", "w7:p2", "/checkout")
		pc, err := pluginContextFromEnv()
		if err != nil {
			t.Fatalf("pluginContextFromEnv: %v", err)
		}
		if pc.WorkspaceID != "w7" || pc.FocusedPaneID != "w7:p2" {
			t.Fatalf("context = %+v, want w7 / w7:p2", pc)
		}
		if pc.Worktree == nil || !pc.Worktree.IsLinkedWorktree || pc.Worktree.CheckoutPath != "/checkout" {
			t.Fatalf("worktree provenance = %+v, want the linked /checkout", pc.Worktree)
		}
	})
}

// TestContextFromPluginEnvStillTolerantConfirms quick actions keep their
// launch-anyway behavior: a malformed context there still yields a usable
// (empty) RunContext rather than refusing to open the picker.
func TestContextFromPluginEnvStillTolerant(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", "{not json")
	if ctx := contextFromPluginEnv(); ctx.WorkspaceId != "" {
		t.Fatalf("RunContext = %+v, want the empty fallback", ctx)
	}
}

// TestParseProjectsArgs covers the explicit escape hatch: bare `projects` keeps
// the contextual behavior, `--pick` always opens the picker, and anything else
// is rejected rather than silently ignored.
func TestParseProjectsArgs(t *testing.T) {
	cases := []struct {
		args     []string
		wantPick bool
		wantErr  bool
	}{
		{args: nil, wantPick: false},
		{args: []string{}, wantPick: false},
		{args: []string{"--pick"}, wantPick: true},
		{args: []string{"--pick", "--pick"}, wantErr: true},
		{args: []string{"-pick"}, wantErr: true},
		{args: []string{"--pickle"}, wantErr: true},
		{args: []string{"pick"}, wantErr: true},
		{args: []string{"--pick", "extra"}, wantErr: true},
		{args: []string{""}, wantErr: true},
	}
	for _, c := range cases {
		pick, err := parseProjectsArgs(c.args)
		if c.wantErr {
			if err == nil {
				t.Fatalf("parseProjectsArgs(%q) = %v, want an error", c.args, pick)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parseProjectsArgs(%q): %v", c.args, err)
		}
		if pick != c.wantPick {
			t.Fatalf("parseProjectsArgs(%q) = %v, want %v", c.args, pick, c.wantPick)
		}
	}
}

// TestLaunchProjectsReturnsToCrew is the end-to-end action path: with crew_tab
// configured and a linked context, launchProjects focuses the Crew tab and never
// asks herdr to open a picker pane at all.
func TestLaunchProjectsReturnsToCrew(t *testing.T) {
	f := crewFixture(t, "Crew")
	configDir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", configDir)
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[projects]\ncrew_tab = \"Crew\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	record := filepath.Join(t.TempDir(), "args.txt")
	t.Setenv("HERDR_BIN_PATH", writeArgRecordingHerdr(t, record))

	launchProjects(nil)

	if _, err := os.Stat(record); err == nil {
		got, _ := os.ReadFile(record)
		t.Fatalf("herdr CLI was invoked (%q); returning to a Crew must not open a picker pane", got)
	}
	if got := f.paramsFor("tab.focus")["tab_id"]; got != "w1:t1" {
		t.Fatalf("focused tab_id = %v, want w1:t1", got)
	}
	f.assertNoMutations()
}

// TestLaunchProjectsPickEscape confirms `projects --pick` always opens the
// picker — even from a linked worktree context with a Crew tab present — and
// makes no native identity calls at all on the way.
func TestLaunchProjectsPickEscape(t *testing.T) {
	f := crewFixture(t, "Crew")
	configDir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", configDir)
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[projects]\ncrew_tab = \"Crew\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	record := filepath.Join(t.TempDir(), "args.txt")
	t.Setenv("HERDR_BIN_PATH", writeArgRecordingHerdr(t, record))

	launchProjects([]string{"--pick"})

	got, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("--pick must open the picker pane: %v", err)
	}
	if !strings.Contains(string(got), "pane\nopen") {
		t.Fatalf("recorded args %q do not open a plugin pane", got)
	}
	if len(f.methods()) != 0 {
		t.Fatalf("native calls %v; --pick must not consult native identity", f.methods())
	}
}

// TestLaunchProjectsForwardsCallerContext covers the non-linked path: the picker
// opens as before, and the caller's context rides along in HERDR_PLUS_CTX the way
// Quick Actions already forwards it — while the pane itself keeps running in the
// plugin directory, so the manifest's relative ./bin/herdr-plus still resolves.
func TestLaunchProjectsForwardsCallerContext(t *testing.T) {
	f := startFakeHerdr(t)
	setPluginContext(t, map[string]any{
		"workspace_id":     "w1",
		"workspace_label":  "notes",
		"focused_pane_id":  "w1:p1",
		"focused_pane_cwd": "/home/user/code",
	})
	f.handle("workspace.get", map[string]any{"workspace": wsInfoNoProvenance("w1", "notes")})
	f.handle("pane.get", paneInfoJSON("w1:p1", "w1:t1", "w1"))

	configDir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", configDir)
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[projects]\ncrew_tab = \"Crew\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	record := filepath.Join(t.TempDir(), "args.txt")
	t.Setenv("HERDR_BIN_PATH", writeArgRecordingHerdr(t, record))

	launchProjects(nil)

	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("a non-linked context must still open the picker: %v", err)
	}
	args := strings.Split(string(raw), "\n")
	var encoded string
	for _, a := range args {
		if strings.HasPrefix(a, "HERDR_PLUS_CTX=") {
			encoded = strings.TrimPrefix(a, "HERDR_PLUS_CTX=")
		}
		if a == "--cwd" {
			t.Fatal("--cwd must not be passed: the picker resolves ./bin/herdr-plus against the plugin directory")
		}
	}
	if encoded == "" {
		t.Fatalf("recorded args %q carry no HERDR_PLUS_CTX", raw)
	}
	if _, err := base64.StdEncoding.DecodeString(encoded); err != nil {
		t.Fatalf("HERDR_PLUS_CTX is not the encoded context: %v", err)
	}
	ctx, err := decodeRunContext(encoded)
	if err != nil {
		t.Fatalf("decodeRunContext: %v", err)
	}
	if ctx.WorkDir != "/home/user/code" || ctx.WorkspaceId != "w1" {
		t.Fatalf("forwarded context = %+v, want the caller's cwd and workspace", ctx)
	}
	f.assertNoMutations()
}

// TestLaunchProjectsWithoutCrewTabIsUnchanged confirms the upstream behavior is
// exactly preserved when the policy is not configured: the picker opens and no
// native identity lookup happens at all.
func TestLaunchProjectsWithoutCrewTabIsUnchanged(t *testing.T) {
	f := crewFixture(t, "Crew")
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	record := filepath.Join(t.TempDir(), "args.txt")
	t.Setenv("HERDR_BIN_PATH", writeArgRecordingHerdr(t, record))

	launchProjects(nil)

	if _, err := os.Stat(record); err != nil {
		t.Fatalf("the picker must open when no crew_tab is configured: %v", err)
	}
	if len(f.methods()) != 0 {
		t.Fatalf("native calls %v; an unconfigured Crew policy must consult nothing", f.methods())
	}
}

// --- Task 2: reuse the exact selected checkout -----------------------------

// reuseProject is a minimal single-tab project rooted at dir.
func reuseProject(name, dir string) Project {
	return Project{Name: name, WorkingDir: dir, Tabs: []ProjectTab{{Name: "shell"}}}
}

// TestOpenProjectReusesExactCheckout covers the unique-match case: the selected
// preset's directory is already open as a workspace, so that workspace is
// focused and nothing is created, laid out, or started.
func TestOpenProjectReusesExactCheckout(t *testing.T) {
	dir := t.TempDir()
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	f := startFakeHerdr(t)
	f.handle("workspace.list", map[string]any{"workspaces": []any{
		wsInfo("w1", "other", filepath.Join(canonical, "elsewhere"), true),
		wsInfo("w2", "repo", canonical, false),
	}})
	f.handle("workspace.get", map[string]any{"workspace": wsInfo("w2", "repo", canonical, false)})
	f.handle("workspace.focus", map[string]any{})

	// Repeat it: reuse must stay a focus, never a second workspace.
	for i := 0; i < 2; i++ {
		if err := openProject(f.client(), reuseProject("repo", dir), reuseOptions{enabled: true}); err != nil {
			t.Fatalf("attempt %d: openProject: %v", i+1, err)
		}
	}
	focuses := 0
	for _, c := range f.calls {
		if c.Method != "workspace.focus" {
			continue
		}
		focuses++
		if got := c.Params["workspace_id"]; got != "w2" {
			t.Fatalf("focused workspace_id = %v, want w2 (the workspace holding this exact checkout)", got)
		}
	}
	if focuses != 2 {
		t.Fatalf("workspace.focus calls = %d, want 2 — one per open, each a plain focus", focuses)
	}
	f.assertNoMutations()
}

// TestOpenProjectReuseMatchesThroughSymlink confirms identity is the canonical
// path: a preset pointing at a symlinked alias of an open checkout is the same
// checkout, not a new one.
func TestOpenProjectReuseMatchesThroughSymlink(t *testing.T) {
	real := t.TempDir()
	canonical, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	f := startFakeHerdr(t)
	f.handle("workspace.list", map[string]any{"workspaces": []any{wsInfo("w2", "repo", canonical, true)}})
	f.handle("workspace.get", map[string]any{"workspace": wsInfo("w2", "repo", canonical, true)})
	f.handle("workspace.focus", map[string]any{})

	if err := openProject(f.client(), reuseProject("repo", alias), reuseOptions{enabled: true}); err != nil {
		t.Fatalf("openProject: %v", err)
	}
	if got := f.paramsFor("workspace.focus")["workspace_id"]; got != "w2" {
		t.Fatalf("focused workspace_id = %v, want w2 via the symlinked alias", got)
	}
	f.assertNoMutations()
}

// TestOpenProjectReuseIgnoresSiblingWorktrees is the identity rule that matters
// most: two worktrees of one repository share a repo_key but are different
// checkouts, so opening the second must build it rather than focus the first.
func TestOpenProjectReuseIgnoresSiblingWorktrees(t *testing.T) {
	parent := t.TempDir()
	open := filepath.Join(parent, "worktree-a")
	wanted := filepath.Join(parent, "worktree-b")
	for _, d := range []string{open, wanted} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	canonicalOpen, err := filepath.EvalSymlinks(open)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	f := startFakeHerdr(t)
	// Same repo_key, same label, different checkout — not a match.
	f.handle("workspace.list", map[string]any{"workspaces": []any{wsInfo("w1", "repo", canonicalOpen, true)}})
	f.handle("workspace.create", map[string]any{
		"workspace": map[string]any{"workspace_id": "w9"},
		"tab":       map[string]any{"tab_id": "w9:t1"},
		"root_pane": map[string]any{"pane_id": "w9:p1"},
	})
	f.handle("tab.rename", map[string]any{})

	if err := openProject(f.client(), reuseProject("repo", wanted), reuseOptions{enabled: true}); err != nil {
		t.Fatalf("openProject: %v", err)
	}
	f.assertNotCalled("workspace.focus")
	if got := f.paramsFor("workspace.create")["cwd"]; got != wanted {
		t.Fatalf("created workspace cwd = %v, want %v", got, wanted)
	}
}

// TestOpenProjectReuseFailuresNeverCreate covers the cases where the inventory
// or the identity cannot be trusted: none of them may be read as "zero matches"
// and quietly create a duplicate workspace.
func TestOpenProjectReuseFailuresNeverCreate(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T, dir string) *fakeHerdr
		wantErr string
	}{
		{
			name: "failed native inventory",
			setup: func(t *testing.T, dir string) *fakeHerdr {
				f := startFakeHerdr(t)
				f.fail("workspace.list", "socket exploded")
				return f
			},
			wantErr: "list workspaces",
		},
		{
			name: "the chosen workspace vanished before focus",
			setup: func(t *testing.T, dir string) *fakeHerdr {
				canonical, _ := filepath.EvalSymlinks(dir)
				f := startFakeHerdr(t)
				f.handle("workspace.list", map[string]any{"workspaces": []any{wsInfo("w2", "repo", canonical, true)}})
				f.fail("workspace.get", "no such workspace")
				return f
			},
			wantErr: "revalidate",
		},
		{
			name: "the chosen workspace changed checkout before focus",
			setup: func(t *testing.T, dir string) *fakeHerdr {
				canonical, _ := filepath.EvalSymlinks(dir)
				f := startFakeHerdr(t)
				f.handle("workspace.list", map[string]any{"workspaces": []any{wsInfo("w2", "repo", canonical, true)}})
				f.handle("workspace.get", map[string]any{"workspace": wsInfo("w2", "repo", "/somewhere-else", true)})
				return f
			},
			wantErr: "no longer",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			f := c.setup(t, dir)
			err := openProject(f.client(), reuseProject("repo", dir), reuseOptions{enabled: true})
			if err == nil {
				t.Fatal("want a visible error, got none")
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("error %q does not mention %q", err, c.wantErr)
			}
			f.assertNoMutations()
			f.assertNotCalled("workspace.focus")
		})
	}
}

// TestOpenProjectReuseDisabledKeepsCreating confirms the default: with the
// policy off, a project opens a fresh workspace exactly as before and native
// provenance is never even consulted.
func TestOpenProjectReuseDisabledKeepsCreating(t *testing.T) {
	dir := t.TempDir()
	f := startFakeHerdr(t)
	f.handle("workspace.create", map[string]any{
		"workspace": map[string]any{"workspace_id": "w9"},
		"tab":       map[string]any{"tab_id": "w9:t1"},
		"root_pane": map[string]any{"pane_id": "w9:p1"},
	})
	f.handle("tab.rename", map[string]any{})

	if err := openProject(f.client(), reuseProject("repo", dir), reuseOptions{}); err != nil {
		t.Fatalf("openProject: %v", err)
	}
	f.assertNotCalled("workspace.list")
	f.assertNotCalled("workspace.focus")
}

// TestOpenProjectReuseIgnoresWorkspacesWithoutProvenance confirms a workspace
// herdr reports no checkout for is never adopted, even when it is the only one
// open and its label matches.
func TestOpenProjectReuseIgnoresWorkspacesWithoutProvenance(t *testing.T) {
	dir := t.TempDir()
	f := startFakeHerdr(t)
	f.handle("workspace.list", map[string]any{"workspaces": []any{wsInfoNoProvenance("w1", "repo")}})
	f.handle("workspace.create", map[string]any{
		"workspace": map[string]any{"workspace_id": "w9"},
		"tab":       map[string]any{"tab_id": "w9:t1"},
		"root_pane": map[string]any{"pane_id": "w9:p1"},
	})
	f.handle("tab.rename", map[string]any{})

	if err := openProject(f.client(), reuseProject("repo", dir), reuseOptions{enabled: true}); err != nil {
		t.Fatalf("openProject: %v", err)
	}
	f.assertNotCalled("workspace.focus")
}

// TestOpenProjectAmbiguousMatchRequiresChoice covers two open workspaces on the
// very same checkout: the user chooses explicitly, and the choice is revalidated
// before it is focused. Nothing is created either way.
func TestOpenProjectAmbiguousMatchRequiresChoice(t *testing.T) {
	dir := t.TempDir()
	canonical, _ := filepath.EvalSymlinks(dir)

	t.Run("explicit choice is focused", func(t *testing.T) {
		f := startFakeHerdr(t)
		f.handle("workspace.list", map[string]any{"workspaces": []any{
			wsInfo("w1", "repo", canonical, true),
			wsInfo("w2", "repo", canonical, true),
		}})
		f.handle("workspace.get", map[string]any{"workspace": wsInfo("w2", "repo", canonical, true)})
		f.handle("workspace.focus", map[string]any{})

		var offered []workspaceCandidate
		choose := func(c []workspaceCandidate) (workspaceCandidate, bool, error) {
			offered = c
			return c[1], true, nil
		}
		if err := openProject(f.client(), reuseProject("repo", dir), reuseOptions{enabled: true, choose: choose}); err != nil {
			t.Fatalf("openProject: %v", err)
		}
		if len(offered) != 2 || offered[0].WorkspaceID != "w1" || offered[1].WorkspaceID != "w2" {
			t.Fatalf("offered candidates = %+v, want both native workspace ids", offered)
		}
		if offered[0].Label != "repo" || offered[0].CheckoutPath == "" {
			t.Fatalf("candidate %+v must carry the native label and checkout for display", offered[0])
		}
		if got := f.paramsFor("workspace.focus")["workspace_id"]; got != "w2" {
			t.Fatalf("focused workspace_id = %v, want the chosen w2", got)
		}
		f.assertNoMutations()
	})

	t.Run("cancel is a no-op", func(t *testing.T) {
		f := startFakeHerdr(t)
		f.handle("workspace.list", map[string]any{"workspaces": []any{
			wsInfo("w1", "repo", canonical, true),
			wsInfo("w2", "repo", canonical, true),
		}})
		choose := func([]workspaceCandidate) (workspaceCandidate, bool, error) {
			return workspaceCandidate{}, false, nil
		}
		if err := openProject(f.client(), reuseProject("repo", dir), reuseOptions{enabled: true, choose: choose}); err != nil {
			t.Fatalf("cancel must be a silent no-op: %v", err)
		}
		f.assertNoMutations()
		f.assertNotCalled("workspace.focus")
	})

	t.Run("no chooser fails visibly", func(t *testing.T) {
		f := startFakeHerdr(t)
		f.handle("workspace.list", map[string]any{"workspaces": []any{
			wsInfo("w1", "repo", canonical, true),
			wsInfo("w2", "repo", canonical, true),
		}})
		err := openProject(f.client(), reuseProject("repo", dir), reuseOptions{enabled: true})
		if err == nil || !strings.Contains(err.Error(), "w1") || !strings.Contains(err.Error(), "w2") {
			t.Fatalf("headless ambiguity must name both workspaces, got %v", err)
		}
		f.assertNoMutations()
	})
}

// TestWorkspacePickerChoiceAndCancel drives the selection UI itself: enter picks
// the highlighted workspace, esc cancels, and neither invents a default. The
// list never pre-selects "the first match" on the user's behalf — cancelling
// leaves nothing chosen at all.
func TestWorkspacePickerChoiceAndCancel(t *testing.T) {
	candidates := []workspaceCandidate{
		{WorkspaceID: "w1", Label: "repo", CheckoutPath: "/checkout"},
		{WorkspaceID: "w2", Label: "repo", CheckoutPath: "/checkout"},
	}

	m := newWorkspacePickerModel(candidates, "/checkout")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated, _ = updated.(workspacePickerModel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(workspacePickerModel)
	if got.chosen == nil || got.chosen.WorkspaceID != "w2" {
		t.Fatalf("chosen = %+v, want w2", got.chosen)
	}
	if !strings.Contains(got.View(), "w2") {
		t.Fatalf("the picker must show native workspace ids; view:\n%s", got.View())
	}

	m = newWorkspacePickerModel(candidates, "/checkout")
	cancelled, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if c := cancelled.(workspacePickerModel); c.chosen != nil {
		t.Fatalf("esc must choose nothing, got %+v", c.chosen)
	}
}

// TestMalformedInventoryIsNeverAbsence covers the difference between "herdr says
// nothing is open" and "herdr's answer cannot be read". Only the first may lead
// to creating a workspace; the second must be an error, or a broken reply would
// quietly produce the duplicate this whole feature exists to prevent.
func TestMalformedInventoryIsNeverAbsence(t *testing.T) {
	cases := []struct {
		name    string
		result  any
		wantErr string
	}{
		{
			name:    "no workspaces key at all",
			result:  map[string]any{"type": "workspace_list"},
			wantErr: "no workspaces array",
		},
		{
			name:    "a null workspaces array",
			result:  map[string]any{"workspaces": nil},
			wantErr: "no workspaces array",
		},
		{
			name:    "a workspace with no id",
			result:  map[string]any{"workspaces": []any{map[string]any{"label": "repo"}}},
			wantErr: "no workspace_id",
		},
		{
			name: "provenance with an unusable checkout path",
			result: map[string]any{"workspaces": []any{map[string]any{
				"workspace_id": "w1",
				"label":        "repo",
				"worktree":     map[string]any{"repo_key": "k", "checkout_path": "", "is_linked_worktree": true},
			}}},
			wantErr: "absolute path",
		},
		{
			name: "provenance with a relative checkout path",
			result: map[string]any{"workspaces": []any{map[string]any{
				"workspace_id": "w1",
				"label":        "repo",
				"worktree":     map[string]any{"repo_key": "k", "checkout_path": "relative/checkout", "is_linked_worktree": true},
			}}},
			wantErr: "absolute path",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := startFakeHerdr(t)
			f.handle("workspace.list", c.result)
			err := openProject(f.client(), reuseProject("repo", t.TempDir()), reuseOptions{enabled: true})
			if err == nil {
				t.Fatal("want an error; a malformed inventory must never read as an empty one")
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("error %q does not mention %q", err, c.wantErr)
			}
			f.assertNoMutations()
		})
	}

	t.Run("a genuinely empty inventory still creates", func(t *testing.T) {
		f := startFakeHerdr(t)
		f.handle("workspace.list", map[string]any{"type": "workspace_list", "workspaces": []any{}})
		f.handle("workspace.create", map[string]any{
			"workspace": map[string]any{"workspace_id": "w9"},
			"tab":       map[string]any{"tab_id": "w9:t1"},
			"root_pane": map[string]any{"pane_id": "w9:p1"},
		})
		f.handle("tab.rename", map[string]any{})
		if err := openProject(f.client(), reuseProject("repo", t.TempDir()), reuseOptions{enabled: true}); err != nil {
			t.Fatalf("an empty list means nothing is open, which is not an error: %v", err)
		}
		if got := f.paramsFor("workspace.create")["label"]; got != "repo" {
			t.Fatalf("workspace.create label = %v, want repo", got)
		}
	})
}

// TestMalformedTabListIsNeverAMissingTab is the same rule one level down: an
// unreadable tab.list reply must not be reported to the user as a renamed or
// closed Crew tab.
func TestMalformedTabListIsNeverAMissingTab(t *testing.T) {
	f := startFakeHerdr(t)
	linkedContext(t, "w1", "w1:p1", "/checkout")
	f.handle("workspace.get", map[string]any{"workspace": wsInfo("w1", "repo", "/checkout", true)})
	f.handle("pane.get", paneInfoJSON("w1:p1", "w1:t1", "w1"))
	f.handle("tab.list", map[string]any{"type": "tab_list"})

	pc, _ := pluginContextFromEnv()
	focused, err := returnToCrew(f.client(), pc, "Crew")
	if err == nil || !strings.Contains(err.Error(), "no tabs array") {
		t.Fatalf("want a malformed-response error, got focused=%v err=%v", focused, err)
	}
	if strings.Contains(err.Error(), "no tab labeled") {
		t.Fatalf("a broken reply must not be reported as a renamed tab: %v", err)
	}
	f.assertNoMutations()
}

// TestReturnToCrewChecksPaneIdentity confirms the pane lookup is an identity
// check, not just a liveness one: an answer about some other pane cannot stand
// in for the pane the action was actually invoked from.
func TestReturnToCrewChecksPaneIdentity(t *testing.T) {
	f := startFakeHerdr(t)
	linkedContext(t, "w1", "w1:p1", "/checkout")
	f.handle("workspace.get", map[string]any{"workspace": wsInfo("w1", "repo", "/checkout", true)})
	// Right workspace, wrong pane.
	f.handle("pane.get", paneInfoJSON("w1:p9", "w1:t1", "w1"))

	pc, _ := pluginContextFromEnv()
	focused, err := returnToCrew(f.client(), pc, "Crew")
	if err == nil || !strings.Contains(err.Error(), "pane identity") {
		t.Fatalf("want a pane identity error, got focused=%v err=%v", focused, err)
	}
	f.assertNoMutations()
	f.assertNotCalled("tab.focus")
}
