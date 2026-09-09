package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// This suite runs the built CLI with real disposable Git repositories. Only
// deliberately broken Git queries and most fetches are replaced; one bounded
// fixture fetches a temporary file origin. IPC is always a local fake.
// Neither the user's Git configuration nor a live Herdr endpoint is inherited.
func TestEnsureWorktree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic Unix socket and sh Git boundary; Windows native gate is separate")
	}
	bin := filepath.Join(t.TempDir(), "herdr-plus")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}

	t.Run("selection", func(t *testing.T) {
		for _, tc := range []struct {
			name, state                string
			path, base, focus, symlink bool
		}{
			{name: "registered preserves old path", state: "registered", path: true, base: true},
			{name: "registered needs no defaults", state: "registered"},
			{name: "locked checkout with newline path", state: "locked"},
			{name: "local branch without checkout", state: "local"},
			{name: "local branch explicit path ignores base", state: "local", path: true, base: true},
			{name: "new branch fetches explicit base", state: "new", path: true, base: true},
			{name: "exact match a is not ab", state: "prefix", path: true, base: true},
			{name: "canonical cwd and explicit focus", state: "local", symlink: true, focus: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newEnsureFixture(t, bin)
				oldPath := filepath.Join(f.dir, "old checkout with spaces")
				switch tc.state {
				case "registered":
					f.git("worktree", "add", "-b", "feature/a", oldPath)
				case "locked":
					oldPath += "\nwith a quote \""
					f.git("worktree", "add", "-b", "feature/a", oldPath)
					f.git("worktree", "lock", "--reason", "locked\nreason", oldPath)
					f.git("worktree", "add", "--detach", filepath.Join(f.dir, "detached"))
				case "local":
					f.git("branch", "feature/a")
				case "prefix":
					f.git("worktree", "add", "-b", "feature/ab", oldPath)
				}
				before := f.git("show-ref", "--heads")
				cwd := f.repo
				if tc.symlink {
					cwd = filepath.Join(f.dir, "repo alias")
					if err := os.Symlink(f.repo, cwd); err != nil {
						t.Fatal(err)
					}
				}
				args := []string{"--cwd", cwd, "--branch", "feature/a"}
				want := map[string]any{"cwd": f.repo, "branch": "feature/a", "focus": tc.focus}
				method := "worktree.create"
				resultPath := f.target
				if tc.path {
					args = append(args, "--path", f.target)
				}
				if tc.base {
					args = append(args, "--base", "main")
				}
				if tc.focus {
					args = append(args, "--focus")
				}
				if tc.state == "registered" || tc.state == "locked" {
					method, resultPath = "worktree.open", oldPath
				} else if tc.path {
					want["path"] = f.target
				}
				fetch := tc.state == "new" || tc.state == "prefix"
				if fetch {
					want["base"] = "origin/main"
				}
				result := ensureResult(resultPath)
				if method == "worktree.open" {
					result["already_open"] = true
				}
				f.reply = map[string]any{"id": "herdr-plus", "result": result}
				stdout, stderr, err := f.run(args...)
				if err != nil {
					t.Fatalf("CLI: %v; stderr=%s; stdout=%s", err, stderr, stdout)
				}
				if stderr != "" {
					t.Fatalf("unexpected stderr: %s", stderr)
				}
				var got map[string]any
				if err := json.Unmarshal([]byte(stdout), &got); err != nil {
					t.Fatalf("stdout must be one JSON object: %q: %v", stdout, err)
				}
				if !reflect.DeepEqual(got, map[string]any{"result": result}) {
					t.Fatalf("result changed: %#v", got)
				}
				f.assertCalls(method, want)
				log := f.log()
				if strings.Contains(log, "fetch origin refs/heads/main:refs/remotes/origin/main\n") != fetch {
					t.Fatalf("fetch selection: %q", log)
				}
				if fetch && !strings.Contains(log, "fetch origin refs/heads/main:refs/remotes/origin/main\nNATIVE worktree.create\n") {
					t.Fatalf("fetch must precede native call: %q", log)
				}
				if after := f.git("show-ref", "--heads"); after != before {
					t.Fatalf("local branch refs changed: before=%s after=%s", before, after)
				}
				if _, err := os.Lstat(f.target); !os.IsNotExist(err) {
					t.Fatalf("Plus created target directly: %v", err)
				}
			})
		}
	})

	t.Run("cwd overrides inherited repository selectors", func(t *testing.T) {
		for _, selectors := range []string{"directory and worktree", "common directory", "config count", "config parameters"} {
			t.Run(selectors, func(t *testing.T) {
				a, b := newEnsureFixture(t, bin), newEnsureFixture(t, bin)
				oldPath := filepath.Join(a.dir, "registered in A")
				a.git("worktree", "add", "-b", "feature/a", oldPath)
				b.git("worktree", "add", "-b", "feature/a", filepath.Join(b.dir, "registered in B"))
				a.cliEnv = []string{"GIT_DIR=" + filepath.Join(b.repo, ".git"), "GIT_WORK_TREE=" + b.repo}
				if selectors == "common directory" {
					a.cliEnv = []string{"GIT_COMMON_DIR=" + filepath.Join(b.repo, ".git")}
				}
				if selectors == "config count" {
					a.cliEnv = []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.worktree", "GIT_CONFIG_VALUE_0=" + b.repo}
				}
				if selectors == "config parameters" {
					a.cliEnv = []string{"GIT_CONFIG_PARAMETERS='core.worktree=" + b.repo + "'"}
				}
				if strings.HasPrefix(selectors, "config ") {
					// Verify that Git actually received the override; a benign or
					// ineffective test environment would leave the doubt unresolved.
					cmd := exec.Command(a.realGit, "config", "--get", "core.worktree")
					cmd.Dir, cmd.Env = a.repo, append(a.env(), a.cliEnv...)
					out, err := cmd.CombinedOutput()
					if err != nil || strings.TrimSpace(string(out)) != b.repo {
						t.Fatalf("injected config missing: out=%q err=%v", out, err)
					}
					cmd = exec.Command(a.realGit, "rev-parse", "--show-toplevel")
					cmd.Dir, cmd.Env = a.repo, append(a.env(), a.cliEnv...)
					out, err = cmd.CombinedOutput()
					t.Logf("unfiltered Git root=%q err=%v; explicit A=%q", out, err, a.repo)
				}
				a.reply = map[string]any{"id": "herdr-plus", "result": ensureResult(oldPath)}
				stdout, stderr, err := a.run("--cwd", a.repo, "--branch", "feature/a")
				if err != nil {
					t.Fatalf("explicit A must succeed: %v stdout=%q stderr=%q", err, stdout, stderr)
				}
				a.assertCalls("worktree.open", map[string]any{"cwd": a.repo, "branch": "feature/a", "focus": false})
				if strings.Contains(a.log(), "fetch ") {
					t.Fatal("registered branch fetched")
				}
			})
		}
	})

	t.Run("Git paths ignore inherited selectors", func(t *testing.T) {
		for _, tc := range []struct{ variable, path string }{
			{"GIT_INDEX_FILE", "index"},
			{"GIT_OBJECT_DIRECTORY", "objects"},
			{"GIT_GRAFT_FILE", "info/grafts"},
		} {
			t.Run(tc.variable, func(t *testing.T) {
				f := newEnsureFixture(t, bin)
				t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
				t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
				t.Setenv(tc.variable, filepath.Join(f.dir, "foreign"))
				out, err := ensureGit(f.repo, 10*time.Second, "rev-parse", "--path-format=absolute", "--git-path", tc.path)
				want := filepath.Join(f.repo, ".git", tc.path)
				if err != nil || strings.TrimSpace(string(out)) != want {
					t.Fatalf("Git must use cwd's %s: got=%q err=%v want=%q", tc.path, out, err, want)
				}
			})
		}
	})

	t.Run("Git object lookup ignores inherited alternates", func(t *testing.T) {
		a, b := newEnsureFixture(t, bin), newEnsureFixture(t, bin)
		b.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "only B")
		oid := strings.TrimSpace(b.git("rev-parse", "HEAD"))
		t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
		t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
		t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", filepath.Join(b.repo, ".git", "objects"))
		if _, err := ensureGit(a.repo, 10*time.Second, "cat-file", "-e", oid); err == nil {
			t.Fatal("cwd A unexpectedly sees an object present only in B")
		}
	})

	t.Run("Git retains supplied configuration", func(t *testing.T) {
		f := newEnsureFixture(t, bin)
		t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
		t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
		t.Setenv("GIT_CONFIG_COUNT", "1")
		t.Setenv("GIT_CONFIG_KEY_0", "ensure.fixture")
		t.Setenv("GIT_CONFIG_VALUE_0", "retained")
		out, err := ensureGit(f.repo, 10*time.Second, "config", "--get", "ensure.fixture")
		if err != nil || string(out) != "retained\n" {
			t.Fatalf("inherited configuration lost: out=%q err=%v", out, err)
		}
	})

	t.Run("Git ignores inherited replacement namespace", func(t *testing.T) {
		f := newEnsureFixture(t, bin)
		original := strings.TrimSpace(f.git("rev-parse", "HEAD"))
		f.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "replacement")
		f.git("update-ref", "refs/foreign-replacements/"+original, "HEAD")
		t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
		t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
		t.Setenv("GIT_REPLACE_REF_BASE", "refs/foreign-replacements/")
		out, err := ensureGit(f.repo, 10*time.Second, "log", "-1", "--format=%s", original)
		if err != nil || string(out) != "fixture\n" {
			t.Fatalf("foreign replacement changed object: got=%q err=%v", out, err)
		}
	})

	t.Run("local origin refreshes stale base with narrowed mapping", func(t *testing.T) {
		f, origin := newEnsureFixture(t, bin), newEnsureFixture(t, bin)
		f.git("remote", "add", "origin", origin.repo)
		// Establish actual shared history before advancing origin. Independently
		// created fixture commits need not have the same timestamp or object ID.
		f.git("fetch", "origin", "refs/heads/main:refs/remotes/origin/main")
		stale := strings.TrimSpace(f.git("rev-parse", "refs/remotes/origin/main"))
		f.git("config", "remote.origin.fetch", "+refs/heads/other:refs/remotes/origin/other")
		origin.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "new origin base")
		fresh := strings.TrimSpace(origin.git("rev-parse", "HEAD"))
		if stale == fresh {
			t.Fatal("fixture base must start stale")
		}
		origin.git("merge-base", "--is-ancestor", stale, fresh)
		before := f.git("show-ref", "--heads")
		f.localOrigin, f.wantBase = origin.repo, fresh
		stdout, stderr, err := f.run("--cwd", f.repo, "--branch", "feature/a", "--base", "main", "--path", f.target)
		if err != nil {
			t.Fatalf("local fetch: %v stdout=%q stderr=%q", err, stdout, stderr)
		}
		if got := strings.TrimSpace(f.git("rev-parse", "refs/remotes/origin/main")); got != fresh {
			t.Fatalf("origin/main stayed stale: got=%s want=%s", got, fresh)
		}
		f.assertCalls("worktree.create", map[string]any{"cwd": f.repo, "branch": "feature/a", "base": "origin/main", "path": f.target, "focus": false})
		if got := f.git("show-ref", "--heads"); got != before {
			t.Fatalf("local branches changed: before=%s after=%s", before, got)
		}
		if strings.Count(f.log(), "fetch ") != 1 {
			t.Fatalf("want one fetch: %s", f.log())
		}
	})

	t.Run("local origin missing base stops before native without retry", func(t *testing.T) {
		f, origin := newEnsureFixture(t, bin), newEnsureFixture(t, bin)
		f.git("remote", "add", "origin", origin.repo)
		f.git("fetch", "origin", "refs/heads/main:refs/remotes/origin/main")
		stale := strings.TrimSpace(f.git("rev-parse", "refs/remotes/origin/main"))
		f.git("config", "remote.origin.fetch", "+refs/heads/other:refs/remotes/origin/other")
		origin.git("branch", "-m", "other")
		f.localOrigin = origin.repo
		before := f.git("show-ref", "--heads")
		f.assertFailure("git fetch", 0, "--cwd", f.repo, "--branch", "feature/a", "--base", "main", "--path", f.target)
		if strings.Count(f.log(), "fetch ") != 1 {
			t.Fatalf("failed fetch must not retry: %s", f.log())
		}
		if got := strings.TrimSpace(f.git("rev-parse", "refs/remotes/origin/main")); got != stale {
			t.Fatalf("failed fetch changed stale ref: got=%s want=%s", got, stale)
		}
		if got := f.git("show-ref", "--heads"); got != before {
			t.Fatalf("local branches changed: before=%s after=%s", before, got)
		}
	})

	t.Run("invalid inputs fail before mutation", func(t *testing.T) {
		for _, tc := range []struct{ name, flag, value, diagnostic string }{
			{"missing cwd", "--cwd", "OMIT", "cwd"},
			{"relative cwd", "--cwd", ".", "cwd"},
			{"empty cwd", "--cwd", "", "cwd"},
			{"not a repo", "--cwd", "DIR", "git"},
			{"missing branch", "--branch", "OMIT", "branch"},
			{"empty branch", "--branch", "", "branch"},
			{"invalid branch", "--branch", "a..b", "branch"},
			{"leading option branch", "--branch", "--help", "branch"},
			{"branch expression", "--branch", "@{-1}", "branch"},
			{"missing base", "--base", "OMIT", "base"},
			{"empty base", "--base", "", "base"},
			{"invalid base", "--base", "main:other", "base"},
			{"leading option base", "--base", "--upload-pack=evil", "base"},
			{"leading plus base", "--base", "+main", "base"},
			{"missing path", "--path", "OMIT", "path"},
			{"empty path", "--path", "", "path"},
			{"relative path", "--path", "relative", "path"},
			{"existing directory", "--path", "DIR", "path"},
			{"dangling symlink", "--path", "DANGLING", "path"},
			{"dangling symlink trailing separator", "--path", "DANGLING-SLASH", "path"},
			{"existing file", "--path", "FILE", "path"},
			{"unknown flag", "--unknown", "yes", "flag"},
			{"invalid focus", "--focus=maybe", "OMIT", "focus"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newEnsureFixture(t, bin)
				value := tc.value
				switch value {
				case "DIR":
					value = f.dir
				case "DANGLING", "DANGLING-SLASH":
					value = f.target
					if err := os.Symlink(filepath.Join(f.dir, "missing"), value); err != nil {
						t.Fatal(err)
					}
					if tc.value == "DANGLING-SLASH" {
						value += string(filepath.Separator)
					}
				case "FILE":
					value = f.target
					if err := os.WriteFile(value, nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
				flags := []string{"--cwd", "--branch", "--base", "--path"}
				values := []string{f.repo, "feature/a", "main", f.target}
				var args []string
				for i, flag := range flags {
					if flag == tc.flag {
						if value != "OMIT" {
							args = append(args, flag, value)
						}
						continue
					}
					args = append(args, flag, values[i])
				}
				if strings.HasPrefix(tc.flag, "--unknown") {
					args = append(args, tc.flag, value)
				}
				if strings.HasPrefix(tc.flag, "--focus") {
					args = append(args, tc.flag)
				}
				f.assertFailure(tc.diagnostic, 0, args...)
				if strings.Contains(f.log(), "fetch ") {
					t.Fatalf("invalid input fetched: %s", f.log())
				}
			})
		}
	})

	t.Run("validate unused and repeated arguments", func(t *testing.T) {
		for _, extra := range [][]string{
			{"--base", "bad..base"}, {"--base", "+main"}, {"--path", "relative"}, {"--branch", "--evil", "--branch", "feature/a"},
			{"--base", "", "--base", "main"}, {"--focus", "unexpected"},
		} {
			t.Run(strings.Join(extra, " "), func(t *testing.T) {
				f := newEnsureFixture(t, bin)
				f.git("worktree", "add", "-b", "feature/a", filepath.Join(f.dir, "old"))
				f.assertFailure("", 0, append([]string{"--cwd", f.repo, "--branch", "feature/a"}, extra...)...)
				if strings.Contains(f.log(), "fetch ") {
					t.Fatal("invalid unused argument fetched")
				}
			})
		}
	})

	t.Run("failed Git queries never become absence", func(t *testing.T) {
		for _, mode := range []string{"list-fail", "list-empty", "list-malformed", "list-truncated", "list-invalid-head", "list-missing-branch", "list-invalid-branch", "list-duplicate", "show-ref-fail", "fetch-fail"} {
			t.Run(mode, func(t *testing.T) {
				f := newEnsureFixture(t, bin)
				f.mode = mode
				f.assertFailure("git", 0, "--cwd", f.repo, "--branch", "feature/a", "--base", "main", "--path", f.target)
				if mode != "fetch-fail" && strings.Contains(f.log(), "fetch ") {
					t.Fatalf("query failure fetched: %s", f.log())
				}
			})
		}
	})

	t.Run("native failures never emit success or retry", func(t *testing.T) {
		for _, mode := range []string{"error", "malformed-json", "wrong-id", "missing-id", "null", "missing-workspace", "missing-pane", "missing-path", "relative-path", "missing-branch", "wrong-branch", "wrong-path", "bad-already-open"} {
			t.Run(mode, func(t *testing.T) {
				f := newEnsureFixture(t, bin)
				f.git("branch", "feature/a")
				result := ensureResult(f.target)
				f.reply = map[string]any{"id": "herdr-plus", "result": result}
				switch mode {
				case "error":
					f.reply = map[string]any{"error": map[string]any{"code": "path_exists", "message": "race-time native refusal"}}
				case "malformed-json":
					f.rawReply = "not json\n"
				case "wrong-id":
					f.reply["id"] = "unrelated-request"
				case "missing-id":
					delete(f.reply, "id")
				case "null":
					f.reply["result"] = nil
				case "missing-workspace":
					delete(result, "workspace")
				case "missing-pane":
					delete(result, "root_pane")
				case "missing-path":
					delete(result["worktree"].(map[string]any), "path")
				case "relative-path":
					result["worktree"].(map[string]any)["path"] = "relative"
				case "missing-branch":
					delete(result["worktree"].(map[string]any), "branch")
				case "wrong-branch":
					result["worktree"].(map[string]any)["branch"] = "feature/ab"
				case "wrong-path":
					result["worktree"].(map[string]any)["path"] = filepath.Join(f.dir, "other")
				case "bad-already-open":
					result["already_open"] = "yes"
				}
				message := ""
				if mode == "error" {
					message = "path_exists: race-time native refusal"
				}
				f.assertFailure(message, 1, "--cwd", f.repo, "--branch", "feature/a", "--path", f.target)
				if strings.Contains(f.log(), "fetch ") {
					t.Fatal("native error fetched existing branch")
				}
			})
		}
	})

	t.Run("bounded waits", func(t *testing.T) {
		for _, mode := range []string{"list-stall", "native-stall"} {
			t.Run(mode, func(t *testing.T) {
				f := newEnsureFixture(t, bin)
				f.git("branch", "feature/a")
				f.mode = mode
				calls := 0
				if mode == "native-stall" {
					calls = 1
				}
				f.assertFailure("deadline exceeded", calls, "--cwd", f.repo, "--branch", "feature/a")
				if strings.Contains(f.log(), "fetch ") {
					t.Fatal("timeout fetched existing branch")
				}
			})
		}
	})
}

type ensureFixture struct {
	t                                                          *testing.T
	bin, dir, repo, target, realGit, wrapperDir, logPath, mode string
	reply                                                      map[string]any
	rawReply                                                   string
	cliEnv                                                     []string
	localOrigin, wantBase                                      string
	mu                                                         sync.Mutex
	calls                                                      []request
}

func newEnsureFixture(t *testing.T, bin string) *ensureFixture {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	f := &ensureFixture{t: t, bin: bin, dir: dir, realGit: git, repo: filepath.Join(dir, "repo with spaces"), target: filepath.Join(dir, "new checkout with spaces"), wrapperDir: filepath.Join(dir, "bin"), logPath: filepath.Join(dir, "git.log")}
	for _, path := range []string{f.repo, f.wrapperDir} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	f.git("init", "-b", "main")
	f.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "fixture")
	// Fetch is synthetic unless this fixture explicitly permits a temporary file
	// origin. Even then, deny all other protocols and unexpected arguments.
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$ENSURE_LOG"
case "$1" in
fetch)
  if [ "$ENSURE_MODE" = fetch-fail ]; then echo 'synthetic fetch failure' >&2; exit 1; fi
  [ "$#" = 3 ] && [ "$2" = origin ] || exit 91
  case "$3" in main|+main|refs/heads/main:refs/remotes/origin/main) ;; *) exit 91;; esac
  if [ -n "$ENSURE_LOCAL_ORIGIN" ]; then
    [ "$("$ENSURE_REAL_GIT" remote get-url origin)" = "$ENSURE_LOCAL_ORIGIN" ] || exit 94
    export GIT_ALLOW_PROTOCOL=file
    exec "$ENSURE_REAL_GIT" "$@"
  fi
  exit 0;;
worktree)
  [ "$2" = list ] || exit 92
  case "$ENSURE_MODE" in
    list-fail) echo 'synthetic listing failure' >&2; exit 1;;
    list-empty) exit 0;;
    list-malformed) printf 'garbage\000\000'; exit 0;;
    list-truncated) printf 'worktree /tmp/example\000HEAD 123'; exit 0;;
    list-invalid-head) printf 'worktree /tmp/example\000HEAD nope\000branch refs/heads/main\000\000'; exit 0;;
    list-missing-branch) printf 'worktree /tmp/example\000HEAD 1111111111111111111111111111111111111111\000\000'; exit 0;;
    list-invalid-branch) printf 'worktree /tmp/example\000HEAD 1111111111111111111111111111111111111111\000branch refs/heads/bad..branch\000\000'; exit 0;;
    list-duplicate) for n in 1 2; do printf 'worktree /tmp/example\000HEAD 1111111111111111111111111111111111111111\000branch refs/heads/feature/a\000\000'; done; exit 0;;
    list-stall) exec sleep 60;;
  esac;;
show-ref)
  if [ "$ENSURE_MODE" = show-ref-fail ]; then echo 'synthetic ref failure' >&2; exit 128; fi;;
rev-parse|check-ref-format) ;;
*) echo 'unexpected git mutation' >&2; exit 93;;
esac
exec "$ENSURE_REAL_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(f.wrapperDir, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	f.reply = map[string]any{"id": "herdr-plus", "result": ensureResult(f.target)}
	return f
}

func (f *ensureFixture) env() []string {
	return []string{"PATH=" + f.wrapperDir + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + f.dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=file", "ENSURE_REAL_GIT=" + f.realGit, "ENSURE_LOG=" + f.logPath, "ENSURE_MODE=" + f.mode, "ENSURE_LOCAL_ORIGIN=" + f.localOrigin}
}

func (f *ensureFixture) git(args ...string) string {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.realGit, args...)
	cmd.WaitDelay = time.Second
	cmd.Dir, cmd.Env = f.repo, f.env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("fixture git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func ensureResult(path string) map[string]any {
	// root_pane carries its own workspace_id, as PaneInfo does in the 0.9.0 API
	// schema, so the fixture proves the workspace and pane are one result.
	return map[string]any{"workspace": map[string]any{"workspace_id": "w1"}, "root_pane": map[string]any{"pane_id": "w1:p1", "workspace_id": "w1"}, "worktree": map[string]any{"path": path, "branch": "feature/a"}, "native_extra": "preserved"}
}

func (f *ensureFixture) run(args ...string) (string, string, error) {
	f.t.Helper()
	// Keep socket paths under the Unix length limit on macOS.
	dir, err := os.MkdirTemp("", "ew-")
	if err != nil {
		f.t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		f.t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(40 * time.Second))
			var req request
			if err := json.NewDecoder(conn).Decode(&req); err == nil {
				f.mu.Lock()
				f.calls = append(f.calls, req)
				f.mu.Unlock()
				file, err := os.OpenFile(f.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
				if err == nil {
					fmt.Fprintln(file, "NATIVE", req.Method)
					file.Close()
				}
				if f.mode == "native-stall" {
					_, _ = io.Copy(io.Discard, conn)
				} else if f.rawReply != "" {
					fmt.Fprint(conn, f.rawReply)
				} else {
					if f.wantBase != "" {
						cmd := exec.Command(f.realGit, "rev-parse", "refs/remotes/origin/main")
						cmd.Dir, cmd.Env = f.repo, f.env()
						out, err := cmd.Output()
						if err != nil || strings.TrimSpace(string(out)) != f.wantBase {
							f.t.Errorf("base must be fresh before native call: got=%q err=%v want=%s", out, err, f.wantBase)
						}
					}
					_ = json.NewEncoder(conn).Encode(f.reply)
				}
			}
			conn.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.bin, append([]string{"ensure-worktree"}, args...)...)
	cmd.Env = append(append(f.env(), f.cliEnv...), "HERDR_SOCKET_PATH="+socket)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	listener.Close()
	<-done
	if ctx.Err() != nil {
		f.t.Fatalf("CLI exceeded test deadline: %v", ctx.Err())
	}
	return stdout.String(), stderr.String(), err
}

func (f *ensureFixture) assertFailure(diagnostic string, calls int, args ...string) {
	f.t.Helper()
	stdout, stderr, err := f.run(args...)
	if err == nil || stdout != "" || stderr == "" || !strings.Contains(strings.ToLower(stderr), strings.ToLower(diagnostic)) {
		f.t.Fatalf("want failure containing %q with no stdout: err=%v stdout=%q stderr=%q", diagnostic, err, stdout, stderr)
	}
	if len(f.calls) != calls {
		f.t.Fatalf("native calls=%v; want count %d", f.calls, calls)
	}
}

func (f *ensureFixture) assertCalls(method string, params map[string]any) {
	f.t.Helper()
	if len(f.calls) != 1 || f.calls[0].Method != method || !reflect.DeepEqual(f.calls[0].Params, params) {
		f.t.Fatalf("native calls=%#v; want %s %#v", f.calls, method, params)
	}
}

func (f *ensureFixture) log() string {
	f.t.Helper()
	b, err := os.ReadFile(f.logPath)
	if err != nil && !os.IsNotExist(err) {
		f.t.Fatal(err)
	}
	return string(b)
}
