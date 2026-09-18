package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	if old == "" || task == "" || (task == old && len(journal) == 0) {
		return nil
	}
	tabs, err := client.tabList(id)
	if err != nil {
		return err
	}
	panes, err := client.workspacePanes(id)
	if err != nil {
		return err
	}
	for _, tab := range tabs {
		for _, role := range []string{"crew", "usage", "workers"} {
			if tab.TabID != ws.Tokens["crew_"+role+"_tab"] {
				continue
			}
			prefix := map[string]string{"crew": "Crew", "usage": "Usage", "workers": "Workers"}[role]
			if !crewDerivedLabel(tab.Label, prefix+" · "+old, journal[tab.TabID]) {
				continue
			}
			current, err := client.tabGet(tab.TabID)
			if err != nil {
				return err
			}
			if current.WorkspaceID != id || current.TabID != tab.TabID || current.Label != tab.Label {
				continue
			}
			next := prefix + " · " + task
			if next == tab.Label {
				continue
			}
			journal[tab.TabID] = crewRenameEntry{Before: tab.Label, After: next}
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
		if prefix == "" || !crewDerivedLabel(pane.Label, prefix+" · "+old, journal[pane.PaneID]) {
			continue
		}
		current, err := client.paneGet(pane.PaneID)
		if err != nil {
			return err
		}
		if current.WorkspaceID != id || current.PaneID != pane.PaneID || current.Label != pane.Label {
			continue
		}
		next := prefix + " · " + task
		if next == pane.Label {
			continue
		}
		journal[pane.PaneID] = crewRenameEntry{Before: pane.Label, After: next}
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

type crewRenameEntry struct {
	Before string `json:"before"`
	After  string `json:"after"`
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
