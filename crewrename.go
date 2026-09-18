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
	ws, err := client.workspaceGet(id)
	if err != nil {
		return err
	}
	if ws.WorkspaceID != id {
		return fmt.Errorf("workspace identity changed")
	}
	old := ws.Tokens["crew_task"]
	task := strings.Join(strings.Fields(ws.Label), " ")
	if old == "" || task == "" || task == old {
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
			if tab.Label != prefix+" · "+old {
				continue
			}
			current, err := client.tabGet(tab.TabID)
			if err != nil {
				return err
			}
			if current.WorkspaceID != id || current.TabID != tab.TabID || current.Label != tab.Label {
				continue
			}
			if err := client.tabRename(tab.TabID, prefix+" · "+task); err != nil {
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
		if prefix == "" || pane.Label != prefix+" · "+old {
			continue
		}
		current, err := client.paneGet(pane.PaneID)
		if err != nil {
			return err
		}
		if current.WorkspaceID != id || current.PaneID != pane.PaneID || current.Label != pane.Label {
			continue
		}
		if err := client.paneRename(pane.PaneID, prefix+" · "+task); err != nil {
			return err
		}
	}
	return client.call("workspace.report_metadata", map[string]any{
		"workspace_id": id, "source": "plugin:cloudmanic.herdr-plus", "tokens": map[string]string{"crew_task": task},
	}, nil)
}
