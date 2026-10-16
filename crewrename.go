package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Rename events use their workspace ID, never the user's current focus.
func runOnWorkspaceRename() {
	var event struct {
		Data struct {
			WorkspaceID string `json:"workspace_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(os.Getenv("HERDR_PLUGIN_EVENT_JSON")), &event); err != nil {
		errExit(err)
	}
	if !workspaceIDPattern.MatchString(event.Data.WorkspaceID) || strings.Contains(event.Data.WorkspaceID, ":") {
		errExit("rename event has no workspace ID")
	}
	client, err := newHerdrClient()
	if err != nil {
		errExit(err)
	}
	client.timeout = 5 * time.Second
	if err := syncCrewName(client, event.Data.WorkspaceID); err != nil {
		errExit(err)
	}
}

// Each handler reads the current name after acquiring the endpoint/workspace lock.
// A delayed event therefore cannot restore an older workspace name.
func syncCrewName(client *herdrClient, id string) error {
	directory, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	directory = filepath.Join(directory, "herdr-plus", "crew-names")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	key := fmt.Sprintf("%x.lock", sha256.Sum256([]byte(client.socketPath+"\x00"+id)))
	lock, err := os.OpenFile(filepath.Join(directory, key), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := lockCrewFile(lock); err != nil {
		return err
	}
	defer unlockCrewFile(lock)
	journalPath := filepath.Join(directory, strings.TrimSuffix(key, ".lock")+".json")
	journal := map[string]crewRenameEntry{}
	if data, err := os.ReadFile(journalPath); err == nil {
		if err := json.Unmarshal(data, &journal); err != nil {
			return err
		}
		if journal == nil {
			journal = map[string]crewRenameEntry{}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	ws, err := client.workspaceGet(id)
	if err != nil {
		return err
	}
	if ws.WorkspaceID != id {
		return fmt.Errorf("workspace identity changed")
	}
	old := ws.Tokens["crew_task"]
	task := strings.Join(strings.Fields(ws.Label), " ")
	if old == "" || task == "" {
		return nil
	}
	// Herdr caps a token at 80 characters, and older task starts named some
	// labels from the raw ask. A label for the same issue therefore can differ
	// from crew_task, so the issue ID also marks a label as derived.
	issue := crewIssue(task)
	tabs, err := client.tabList(id)
	if err != nil {
		return err
	}
	panes, err := client.workspacePanes(id)
	if err != nil {
		return err
	}
	for _, tab := range tabs {
		prefix := ""
		for role, label := range map[string]string{"crew": "Crew", "usage": "Usage", "workers": "Workers"} {
			if tab.TabID == ws.Tokens["crew_"+role+"_tab"] {
				prefix = label
			}
		}
		if head, _, _ := strings.Cut(tab.Label, " · "); prefix == "" && crewTabPrefixes[head] {
			prefix = head
		}
		if prefix != "" {
			suffix, derived := crewJournalSuffix(tab.Label, journal[tab.TabID]), crewDerivedLabel(tab.Label, prefix+" · "+old, journal[tab.TabID])
			if !derived {
				suffix, derived = crewIssueLabel(tab.Label, prefix, issue)
			}
			if !derived {
				continue
			}
			current, err := client.tabGet(tab.TabID)
			if err != nil {
				return err
			}
			if current.WorkspaceID != id || current.TabID != tab.TabID || current.Label != tab.Label {
				continue
			}
			next := crewLabel(prefix, task, suffix)
			if next == tab.Label {
				continue
			}
			journal[tab.TabID] = crewRenameEntry{Before: tab.Label, After: next, Suffix: suffix}
			if err := saveCrewRename(journalPath, journal); err != nil {
				return err
			}
			if err := client.tabRename(tab.TabID, next); err != nil {
				return err
			}
		}
	}
	for _, pane := range panes {
		prefix := ""
		for role, label := range map[string]string{"orchestrator": "Orchestrator", "memory": "Memory", "issue": "Issue / Utility"} {
			if pane.PaneID == ws.Tokens["crew_"+role+"_pane"] {
				prefix = label
			}
		}
		if pane.TabID == ws.Tokens["crew_usage_tab"] {
			prefix = "Usage"
		}
		if role := pane.Tokens["crew_worker_role"]; role != "" && pane.Tokens["crew_role_number"] != "" {
			prefix = role + " " + pane.Tokens["crew_role_number"]
		}
		// A dispatch names a seat pane "<Role> <n> · <task> · <provider> <model> <effort>" with no tokens.
		if head, _, _ := strings.Cut(pane.Label, " · "); prefix == "" && (crewPanePrefixes[head] || crewSeatPrefix.MatchString(head)) {
			prefix = head
		}
		if prefix == "" {
			continue
		}
		suffix, derived := crewJournalSuffix(pane.Label, journal[pane.PaneID]), crewDerivedLabel(pane.Label, prefix+" · "+old, journal[pane.PaneID])
		if !derived {
			suffix, derived = crewIssueLabel(pane.Label, prefix, issue)
		}
		if !derived {
			continue
		}
		current, err := client.paneGet(pane.PaneID)
		if err != nil {
			return err
		}
		if current.WorkspaceID != id || current.PaneID != pane.PaneID || current.Label != pane.Label {
			continue
		}
		next := crewLabel(prefix, task, suffix)
		if next == pane.Label {
			continue
		}
		journal[pane.PaneID] = crewRenameEntry{Before: pane.Label, After: next, Suffix: suffix}
		if err := saveCrewRename(journalPath, journal); err != nil {
			return err
		}
		if err := client.paneRename(pane.PaneID, next); err != nil {
			return err
		}
	}
	if err := client.call("workspace.report_metadata", map[string]any{
		"workspace_id": id, "source": "plugin:cloudmanic.herdr-plus", "tokens": map[string]string{"crew_task": task},
	}, nil); err != nil {
		return err
	}
	if err := os.Remove(journalPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Task commands add these tabs and panes without role tokens.
var (
	crewTabPrefixes  = map[string]bool{"Workers": true, "Archive": true, "Review": true}
	crewPanePrefixes = map[string]bool{"Orchestrator": true, "Memory": true, "Issue / Utility": true, "Usage": true}
	crewSeatPrefix   = regexp.MustCompile(`^[A-Z][a-z]+ [1-9][0-9]*$`)
	crewIssueID      = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[1-9][0-9]*$`)
)

// crewIssue returns the issue ID that starts a task name, or "".
func crewIssue(task string) string {
	if first, _, _ := strings.Cut(task, " "); crewIssueID.MatchString(first) {
		return first
	}
	return ""
}

// crewIssueLabel reports whether a label has the form
// "<prefix> · <issue> <name>[ · <suffix>]" and returns the suffix.
// A label with the bare issue ID and no name is not derived from the task.
func crewIssueLabel(label, prefix, issue string) (string, bool) {
	rest, found := strings.CutPrefix(label, prefix+" · ")
	if !found || issue == "" {
		return "", false
	}
	name, suffix, _ := strings.Cut(rest, " · ")
	return suffix, strings.HasPrefix(name, issue+" ")
}

func crewLabel(prefix, task, suffix string) string {
	if suffix == "" {
		return prefix + " · " + task
	}
	return prefix + " · " + task + " · " + suffix
}

type crewRenameEntry struct {
	Before string `json:"before"`
	After  string `json:"after"`
	// Suffix is the seat suffix that the rename kept. A retry that finds the
	// label by this entry keeps it again.
	Suffix string `json:"suffix,omitempty"`
}

func crewJournalSuffix(label string, pending crewRenameEntry) string {
	if label != "" && (label == pending.Before || label == pending.After) {
		return pending.Suffix
	}
	return ""
}

func crewDerivedLabel(label, previous string, pending crewRenameEntry) bool {
	return label == previous || (pending.Before != "" && label == pending.Before) || (pending.After != "" && label == pending.After)
}

// Persist intent before the native write. A lost reply can mean the write
// succeeded. Both labels remain recognizable until the task record commits.
func saveCrewRename(file string, entries map[string]crewRenameEntry) error {
	data, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".crew-rename-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}
