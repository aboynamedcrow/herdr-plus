package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestActionFailureIsVisibleAndNotificationIsBounded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket fixture")
	}
	bin := filepath.Join(t.TempDir(), "herdr-plus")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	// The first two replies are what a delivered notification actually looks
	// like: the request's own id echoed back — the client rejects any other as a
	// mismatched reply — and the notification_show result herdr sends. The rest
	// cover a refusal and a reply that never arrives.
	const shown = `{"id":%q,"result":{"type":"notification_show","shown":true,"reason":"shown"}}`
	for _, tc := range []struct{ action, reply string }{
		{"projects", shown},
		{"worktree", shown},
		{"quick-actions", shown},
		{"worktree", `{"error":{"code":"unavailable","message":"notifications unavailable"}}`},
		{"quick-actions", `{"error":{"code":"unavailable","message":"notifications unavailable"}}`},
		{"worktree", ""},
	} {
		t.Run(tc.action+tc.reply, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "action-error-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[projects]\ncrew_tab=\"Crew\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			socket := filepath.Join(dir, "s")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			type request struct {
				ID     string
				Method string
				Params map[string]any
			}
			seen := make(chan request, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
				var req request
				if json.NewDecoder(conn).Decode(&req) != nil {
					return
				}
				seen <- req
				if tc.reply == shown {
					_, _ = fmt.Fprintf(conn, tc.reply+"\n", req.ID)
				} else if tc.reply != "" {
					_, _ = conn.Write([]byte(tc.reply + "\n"))
				} else {
					// Wait for the client's deadline to close this connection.
					_, _ = conn.Read(make([]byte, 1))
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, tc.action)
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir,
				"HERDR_PLUGIN_CONFIG_DIR=" + dir, "HERDR_SOCKET_PATH=" + socket,
				"HERDR_PLUGIN_CONTEXT_JSON={invalid"}
			out, err := cmd.CombinedOutput()
			if err == nil || ctx.Err() != nil || !strings.Contains(string(out), "herdr-plus:") {
				t.Fatalf("original action failure was lost or unbounded: %v, %v, %s", err, ctx.Err(), out)
			}
			_ = listener.Close()
			<-done
			select {
			case req := <-seen:
				if req.Method != "notification.show" || req.Params["title"] != "Herdr Plus" {
					t.Fatalf("unexpected action instead of notification: %+v", req)
				}
				body, ok := req.Params["body"].(string)
				if !ok || body == "" || !strings.Contains(string(out), body) {
					t.Fatalf("notification did not preserve the original failure: %+v, %s", req, out)
				}
			default:
				t.Fatal("action failed silently")
			}
		})
	}
}
