package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

// Drive the real event entrypoint: failed inventory must never authorize a
// tab rename or split, even though a matching layout is configured.
func TestWorktreeEventInventoryGuard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix IPC fixture; Windows runtime is a separate gate")
	}
	bin := filepath.Join(t.TempDir(), "herdr-plus")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		name, reply string
		wantSuccess bool
		alreadyOpen bool
	}{
		{"native error", `{"error":{"code":"unavailable","message":"inventory unavailable"}}`, false, false},
		{"empty inventory", `{"result":{"panes":[]}}`, false, false},
		{"missing inventory", `{"result":{}}`, false, false},
		{"unrelated workspace only", `{"result":{"panes":[{"workspace_id":"w2","pane_id":"w2:p1"}]}}`, false, false},
		{"existing layout", `{"result":{"panes":[{"workspace_id":"w1","pane_id":"w1:p1"},{"workspace_id":"w1","pane_id":"w1:p2"}]}}`, true, false},
		{"native already open", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := t.TempDir()
			if err := os.Mkdir(filepath.Join(config, "worktrees"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(config, "worktrees", "crew.toml"), []byte("repo = \"*\"\n[[tabs]]\nname = \"Crew\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			// Short socket paths are required on macOS.
			dir, err := os.MkdirTemp("", "wtguard-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			socket := filepath.Join(dir, "s")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan []string, 1)
			go func() {
				var calls []string
				for {
					conn, err := listener.Accept()
					if err != nil {
						done <- calls
						return
					}
					_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
					var req struct {
						Method string         `json:"method"`
						Params map[string]any `json:"params"`
					}
					if json.NewDecoder(conn).Decode(&req) == nil {
						calls = append(calls, req.Method)
						if req.Method == "pane.list" {
							_, _ = conn.Write([]byte(tc.reply + "\n"))
						} else {
							_, _ = conn.Write([]byte("{\"error\":{\"code\":\"unexpected_mutation\",\"message\":\"inventory did not authorize layout\"}}\n"))
						}
					}
					conn.Close()
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, "on-worktree")
			event := `{"data":{"already_open":false,"workspace":{"worktree":{"repo_name":"fixture"}},"worktree":{"branch":"fixture"}}}`
			if tc.alreadyOpen {
				event = `{"data":{"already_open":true,"workspace":{"worktree":{"repo_name":"fixture"}},"worktree":{"branch":"fixture"}}}`
			}
			cmd.Env = []string{
				"PATH=" + os.Getenv("PATH"), "HOME=" + config,
				"HERDR_PLUGIN_CONFIG_DIR=" + config, "HERDR_SOCKET_PATH=" + socket,
				"HERDR_WORKSPACE_ID=w1", "HERDR_TAB_ID=w1:t1", "HERDR_PANE_ID=w1:p1",
				"HERDR_PLUGIN_EVENT_JSON=" + event,
			}
			out, runErr := cmd.CombinedOutput()
			listener.Close()
			calls := <-done
			if ctx.Err() != nil {
				t.Fatal("event handler exceeded fixture deadline")
			}
			if (runErr == nil) != tc.wantSuccess {
				t.Fatalf("success=%v want=%v: %s", runErr == nil, tc.wantSuccess, out)
			}
			var wantCalls []string
			if !tc.alreadyOpen {
				wantCalls = []string{"pane.list"}
			}
			if !reflect.DeepEqual(calls, wantCalls) {
				t.Fatalf("layout proceeded without inventory proof: %v; output=%s", calls, out)
			}
		})
	}
}
