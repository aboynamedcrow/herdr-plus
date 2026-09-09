package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// W is tied to the invoking pane, even if another client moves global focus.
func policyInvocation(client *herdrClient, pc pluginContext) (RunContext, error) {
	pane, err := verifyInvokingPane(client, pc, "worktree")
	if err != nil {
		return RunContext{}, err
	}
	return RunContext{WorkDir: pc.FocusedPaneCwd, PaneId: pc.FocusedPaneID, WorkspaceId: pc.WorkspaceID, TabId: pane.TabID}, nil
}

func launchPolicyWorktree() {
	pc, err := pluginContextFromEnv()
	if err != nil {
		actionErrExit(err)
	}
	client, err := newHerdrClient()
	if err != nil {
		actionErrExit(err)
	}
	client.timeout = 15 * time.Second
	ctx, err := policyInvocation(client, pc)
	if err != nil {
		actionErrExit(err)
	}
	encoded, err := ctx.encode()
	if err != nil {
		actionErrExit(err)
	}
	herdr := firstNonEmpty(os.Getenv("HERDR_BIN_PATH"), "herdr")
	deadline, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// This picker is always an overlay, and herdr refuses an explicit target
	// pane for one: it covers the active pane by design. policyInvocation has
	// already proved the invoking pane, so the checkout this plans against is
	// still that pane's, whatever herdr draws the overlay over.
	args := []string{"plugin", "pane", "open", "--plugin", pluginID,
		"--entrypoint", paneEntrypoint("worktree-picker"), "--placement", "overlay"}
	if placementAcceptsTargetPane("overlay") {
		args = append(args, "--target-pane", ctx.PaneId)
	}
	args = append(args, "--env", "HERDR_PLUS_CTX="+encoded)
	cmd := exec.CommandContext(deadline, herdr, args...)
	cmd.WaitDelay = time.Second
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		actionErrExit("open worktree picker:", err)
	}
}

type worktreePlanned struct {
	plan worktreePlan
	err  error
}
type worktreeApplied struct{ err error }
type worktreeUI struct {
	req           worktreeRequest
	input         textinput.Model
	list          fuzzyList
	plan          *worktreePlan
	busy          bool
	err           string
	width, height int
}

func newWorktreeUI(req worktreeRequest) worktreeUI {
	input := textinput.New()
	input.Placeholder = "short description or ISSUE-123 description"
	input.Focus()
	input.SetValue(req.Name)
	return worktreeUI{req: req, input: input, list: newFuzzyList("Filter branches…", nil), busy: strings.TrimSpace(req.Name) != ""}
}

func (m worktreeUI) planCommand() tea.Cmd {
	req := m.req
	return func() tea.Msg { p, err := planWorktree(req); return worktreePlanned{p, err} }
}

func (m worktreeUI) Init() tea.Cmd {
	if strings.TrimSpace(m.req.Name) != "" {
		return m.planCommand()
	}
	return textinput.Blink
}

func (m worktreeUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.Width = max(1, m.width-4)
		m.list.setViewport(max(1, m.height-9-listPromptLines), m.width)
		return m, nil
	case worktreePlanned:
		m.busy = false
		if msg.err != nil {
			m.err = msg.err.Error()
			m.plan = nil
			return m, nil
		}
		m.err = ""
		m.plan = &msg.plan
		items := make([]listItem, len(msg.plan.Candidates))
		for i, c := range msg.plan.Candidates {
			verb := "create"
			if c.Checkout {
				verb = "open existing checkout"
			} else if c.Existing {
				verb = "check out existing branch"
			}
			items[i] = listItem{name: c.Branch, desc: verb + " · " + c.Path, selectable: true, ref: i}
		}
		m.list = newFuzzyList("Filter branches…", items)
		m.list.setViewport(max(1, m.height-9-listPromptLines), m.width)
		return m, nil
	case worktreeApplied:
		m.busy = false
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		return m, tea.Quit
	case tea.KeyMsg:
		// A submitted native operation must return before the picker exits; leaving
		// midway could hide a created worktree behind an apparent cancellation.
		if m.busy {
			return m, nil
		}
		if msg.String() == "ctrl+c" || msg.String() == "esc" {
			return m, tea.Quit
		}
		if m.plan == nil {
			if msg.String() == "enter" {
				m.req.Name = strings.TrimSpace(m.input.Value())
				if m.req.Name == "" {
					m.err = "Enter a short description."
					return m, nil
				}
				m.busy = true
				return m, m.planCommand()
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		switch msg.String() {
		case "up", "ctrl+p":
			m.list.moveUp()
			return m, nil
		case "down", "ctrl+n":
			m.list.moveDown()
			return m, nil
		case "ctrl+r":
			m.busy = true
			m.err = ""
			return m, m.planCommand()
		case "enter":
			i := m.list.selectedIndex()
			if i < 0 {
				return m, nil
			}
			req := m.req
			req.Candidate = m.plan.Candidates[i].ID
			req.Fingerprint = m.plan.Fingerprint
			m.busy = true
			return m, func() tea.Msg {
				fresh, err := planWorktree(req)
				if err == nil {
					_, err = applyWorktreePlan(req, fresh)
				}
				return worktreeApplied{err}
			}
		}
		cmd := m.list.editQuery(msg)
		return m, cmd
	}
	if m.plan == nil {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m worktreeUI) View() string {
	body := headerBarStyle.Render("Herdr Plus · Worktree") + "\n\n"
	if m.plan == nil {
		body += "Name the work\n" + m.input.View() + "\n"
	} else {
		body += m.plan.Project + "\n"
		if m.plan.Base != "" {
			body += "New branch from origin/" + m.plan.Base + "\n"
		}
		body += m.list.view("No matching branches") + "\n"
	}
	if m.busy {
		body += "Working…\n"
	}
	if m.err != "" {
		body += m.err + "\n"
	}
	return body + footerStyle.Render("enter choose · ctrl+r refresh · esc cancel")
}

func runPolicyPicker(req worktreeRequest) error {
	if !filepath.IsAbs(req.Cwd) {
		return fmt.Errorf("worktree picker needs an explicit absolute invoking directory")
	}
	_, err := tea.NewProgram(newWorktreeUI(req), tea.WithAltScreen()).Run()
	return err
}
