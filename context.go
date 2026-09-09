//
// Date: 2026-06-15
// Author: Spicer Matthews (spicer@cloudmanic.com)
// Copyright: 2026 Cloudmanic Labs, LLC. All rights reserved.
//

package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RunContext is the bag of variables herdr-plus exposes to an action's command.
// It is gathered when the quick-actions action fires — from the focused pane herdr
// reports — then serialized and handed to the picker. The picker substitutes
// these values into the chosen command (as Go template fields) and also exports
// them to the command's environment.
type RunContext struct {
	// Value is the dynamic input for the action: the chosen option's value for a
	// "select" action, or the entered string for a "form" action. It is empty for
	// a plain "command" action.
	Value string `json:"value"`

	// WorkDir is the working directory the user invoked herdr-plus from. Actions
	// run with this as their working directory and can read it as {{.WorkDir}}.
	WorkDir string `json:"work_dir"`

	// herdr session/pane metadata.
	PaneId         string `json:"pane_id"`
	TabId          string `json:"tab_id"`
	TabLabel       string `json:"tab_label"`
	WorkspaceId    string `json:"workspace_id"`
	WorkspaceLabel string `json:"workspace_label"`
	TerminalId     string `json:"terminal_id"`
	Agent          string `json:"agent"`
	AgentSessionId string `json:"agent_session_id"`
}

// SessionTitle is a friendly alias for the workspace label — herdr's closest
// notion of "what am I working on". Templates can use {{.SessionTitle}}.
func (c RunContext) SessionTitle() string { return c.WorkspaceLabel }

// SessionId is a friendly alias for the workspace id, available as {{.SessionId}}.
func (c RunContext) SessionId() string { return c.WorkspaceId }

// Home returns the current user's home directory, available as {{.Home}}.
func (c RunContext) Home() string {
	h, _ := os.UserHomeDir()
	return h
}

// envPairs renders the context as KEY=VALUE strings so any spawned command can
// read the same variables from its environment (handy for scripts that would
// rather not bother with templating). Every field is prefixed HERDR_PLUS_ to
// avoid colliding with herdr's own HERDR_ variables.
func (c RunContext) envPairs() []string {
	return []string{
		"HERDR_PLUS_VALUE=" + c.Value,
		"HERDR_PLUS_WORKDIR=" + c.WorkDir,
		"HERDR_PLUS_PANE_ID=" + c.PaneId,
		"HERDR_PLUS_TAB_ID=" + c.TabId,
		"HERDR_PLUS_TAB_LABEL=" + c.TabLabel,
		"HERDR_PLUS_WORKSPACE_ID=" + c.WorkspaceId,
		"HERDR_PLUS_WORKSPACE_LABEL=" + c.WorkspaceLabel,
		"HERDR_PLUS_SESSION_TITLE=" + c.WorkspaceLabel,
		"HERDR_PLUS_SESSION_ID=" + c.WorkspaceId,
		"HERDR_PLUS_TERMINAL_ID=" + c.TerminalId,
		"HERDR_PLUS_AGENT=" + c.Agent,
		"HERDR_PLUS_AGENT_SESSION_ID=" + c.AgentSessionId,
	}
}

// encode serializes the context to a base64 JSON blob so the launching action can
// pass it to the picker pane as a single, shell-safe environment variable.
func (c RunContext) encode() (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// decodeRunContext is the inverse of encode. An empty string yields a zero
// context rather than an error so the picker can still run with no metadata.
func decodeRunContext(s string) (RunContext, error) {
	var c RunContext
	if s == "" {
		return c, nil
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(b, &c)
	return c, err
}

// pluginContext mirrors the subset of HERDR_PLUGIN_CONTEXT_JSON herdr-plus reads.
// herdr injects this when it runs a plugin action, describing the pane that was
// focused when the action fired — exactly the context a quick action wants.
type pluginContext struct {
	WorkspaceID      string `json:"workspace_id"`
	WorkspaceLabel   string `json:"workspace_label"`
	WorkspaceCwd     string `json:"workspace_cwd"`
	TabID            string `json:"tab_id"`
	TabLabel         string `json:"tab_label"`
	FocusedPaneID    string `json:"focused_pane_id"`
	FocusedPaneCwd   string `json:"focused_pane_cwd"`
	FocusedPaneAgent string `json:"focused_pane_agent"`

	// Worktree is the checkout herdr says the invoking workspace holds, nil when
	// it holds none. It is a claim made when the action fired, so the Projects
	// action re-reads the same provenance live before acting on it.
	Worktree *worktreeProvenance `json:"worktree"`
}

// pluginContextFromEnv decodes HERDR_PLUGIN_CONTEXT_JSON, the explicit
// per-invocation context herdr injects describing the pane the action fired
// from. Unlike contextFromPluginEnv it is strict: an unset variable yields an
// empty context (the action simply has no identity to work from), but a
// malformed one is an error. That distinction matters for the Projects action —
// silently reading a broken context as "no workspace" would look exactly like
// "not a task workspace" and open a picker that goes on to create a duplicate.
func pluginContextFromEnv() (pluginContext, error) {
	var pc pluginContext
	raw := strings.TrimSpace(os.Getenv("HERDR_PLUGIN_CONTEXT_JSON"))
	if raw == "" {
		return pc, nil
	}
	if err := json.Unmarshal([]byte(raw), &pc); err != nil {
		return pluginContext{}, fmt.Errorf("malformed HERDR_PLUGIN_CONTEXT_JSON: %w", err)
	}
	return pc, nil
}

// verifyInvokingPane proves that the pane herdr named as the action's invoker is
// still that pane, in that workspace, in that directory.
//
// A plugin action runs server-side with no terminal of its own, and global focus
// belongs to whichever client moved it last. So the pane named in the action
// context is the only safe thing to place an overlay over and the only
// trustworthy working directory: anything read from live focus could belong to
// another client's window by the time the picker opens. An incomplete context,
// or a pane that has since moved, is reported rather than guessed around.
//
// action names the caller for the diagnostic; the checks are identical for
// every action that uses it.
func verifyInvokingPane(client *herdrClient, pc pluginContext, action string) (paneInfo, error) {
	if pc.WorkspaceID == "" || pc.FocusedPaneID == "" || !filepath.IsAbs(pc.FocusedPaneCwd) {
		return paneInfo{}, fmt.Errorf("%s action needs an explicit invoking workspace, pane and directory", action)
	}
	pane, err := client.paneGet(pc.FocusedPaneID)
	if err != nil {
		return paneInfo{}, err
	}
	if pane.PaneID != pc.FocusedPaneID || pane.WorkspaceID != pc.WorkspaceID || !samePath(firstNonEmpty(pane.ForegroundCwd, pane.Cwd), pc.FocusedPaneCwd) {
		return paneInfo{}, fmt.Errorf("invoking pane changed; invoke the %s action again", action)
	}
	return pane, nil
}

// quickActionsInvocation is contextFromPluginEnv with the invoking pane proven
// first. Quick Actions places its picker over that pane and runs the chosen
// command in its directory, so an unverified context is both a placement and a
// working directory this plugin cannot stand behind.
//
// Every field herdr supplied is still carried through: the picker and the
// actions it runs see the same tab and workspace labels, agent, and — because
// the guard requires the focused pane's own absolute cwd — the same working
// directory contextFromPluginEnv would have produced for any launch that this
// guard admits.
func quickActionsInvocation(client *herdrClient, pc pluginContext) (RunContext, error) {
	if _, err := verifyInvokingPane(client, pc, "quick actions"); err != nil {
		return RunContext{}, err
	}
	return RunContext{
		WorkDir:        pc.FocusedPaneCwd,
		PaneId:         pc.FocusedPaneID,
		TabId:          pc.TabID,
		TabLabel:       pc.TabLabel,
		WorkspaceId:    pc.WorkspaceID,
		WorkspaceLabel: pc.WorkspaceLabel,
		Agent:          pc.FocusedPaneAgent,
	}, nil
}

// contextFromPluginEnv builds a RunContext from HERDR_PLUGIN_CONTEXT_JSON, which
// herdr sets when it runs a plugin action. The working directory is the focused
// pane's cwd (the user's real directory), falling back to the workspace cwd. Any
// field herdr does not supply is left empty — a partial context is far better
// than refusing to launch.
//
// It is the unverified reading, used where no pane is placed over and nothing is
// run in that directory. Quick Actions does both, and uses
// quickActionsInvocation instead.
func contextFromPluginEnv() RunContext {
	// A malformed context is deliberately ignored here: a quick action would
	// rather launch with no metadata than refuse to open. The Projects action
	// calls pluginContextFromEnv directly, where the same input is an error.
	pc, _ := pluginContextFromEnv()
	return RunContext{
		WorkDir:        firstNonEmpty(pc.FocusedPaneCwd, pc.WorkspaceCwd),
		PaneId:         pc.FocusedPaneID,
		TabId:          pc.TabID,
		TabLabel:       pc.TabLabel,
		WorkspaceId:    pc.WorkspaceID,
		WorkspaceLabel: pc.WorkspaceLabel,
		Agent:          pc.FocusedPaneAgent,
	}
}
