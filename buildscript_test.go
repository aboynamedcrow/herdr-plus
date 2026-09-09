package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The Unix plugin build entrypoint is a shell script, so this suite runs the
// real scripts/build.sh rather than a Go restatement of it. Each fixture is a
// disposable copy of a plugin root whose PATH holds nothing but the two
// coreutils the script legitimately needs and deliberate fakes: a `go` that
// records its argv and can be told to fail, plus stand-ins for the standalone
// installer and every downloader the retired release-binary fallback reached
// for. Nothing here compiles Go, touches the network, or inherits the
// developer's PATH — a build that tried to fetch a prebuilt binary would leave
// a recorded attempt instead of succeeding quietly.

// wantBuildArgv is the exact command the plugin build must run: an in-place
// build of this checkout into the path the manifest's actions and panes invoke.
var wantBuildArgv = []string{"build", "-o", "bin/herdr-plus", "."}

// fakeGoScript is the stand-in toolchain. It records its argv NUL-separated
// (so an unexpected empty or embedded-newline argument is still visible),
// honours FAKE_GO_EXIT to simulate a failed compile, and otherwise emulates
// just enough of `go build -o <path>` to prove the script produced the binary.
const fakeGoScript = `set -eu
printf '%s\0' "$@" >"$HERDR_TEST_DIR/go-argv"
if [ "${FAKE_GO_EXIT:-0}" -ne 0 ]; then
	echo "fake go: deliberate build failure" >&2
	exit "$FAKE_GO_EXIT"
fi
out=""
prev=""
for arg in "$@"; do
	if [ "$prev" = "-o" ]; then
		out="$arg"
	fi
	prev="$arg"
done
if [ -z "$out" ]; then
	echo "fake go: no -o argument" >&2
	exit 9
fi
printf 'fake herdr-plus binary\n' >"$out"
`

// attemptsFile records every forbidden fetch helper the script invoked.
const attemptsFile = "network-attempts"

type buildFixture struct {
	t      *testing.T
	dir    string // disposable plugin root; the script's working directory
	bin    string // the process's sole PATH entry
	sh     string // real POSIX shell, resolved once outside the fixture PATH
	goExit string // FAKE_GO_EXIT for the fake toolchain, empty when unset
	hasGo  bool
}

// newBuildFixture copies the real build script into a temporary plugin root and
// walls it off from the host: only sh and mkdir are reachable, install.sh and
// the usual downloaders are recording stand-ins, and there is no Go toolchain
// until a test installs one.
func newBuildFixture(t *testing.T) *buildFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("scripts/build.sh is the POSIX build entrypoint; the Windows manifest step runs `go build` directly")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatalf("no POSIX shell available: %v", err)
	}
	f := &buildFixture{t: t, dir: t.TempDir(), sh: sh}
	f.bin = filepath.Join(f.dir, "fakebin")
	if err := os.MkdirAll(filepath.Join(f.dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.bin, 0o755); err != nil {
		t.Fatal(err)
	}

	script, err := os.ReadFile(filepath.Join("scripts", "build.sh"))
	if err != nil {
		t.Fatalf("read the script under test: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "scripts", "build.sh"), script, 0o644); err != nil {
		t.Fatal(err)
	}

	// The script may legitimately reach for these; anything else on PATH is a
	// fake. sh is present so the retired `sh install.sh` fallback would still
	// run (and be recorded) rather than failing for want of a shell.
	for _, tool := range []string{"sh", "mkdir"} {
		real, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("look up %s: %v", tool, err)
		}
		if err := os.Symlink(real, filepath.Join(f.bin, tool)); err != nil {
			t.Fatal(err)
		}
	}

	// Fetching a prebuilt binary is the behaviour that was removed: record any
	// attempt instead of performing one.
	for _, name := range []string{"curl", "wget", "fetch", "ftp"} {
		f.forbid(filepath.Join(f.bin, name), name)
	}
	f.forbid(filepath.Join(f.dir, "install.sh"), "install.sh")
	return f
}

// forbid installs an executable stand-in that records its own invocation and
// exits successfully, so a script that calls it gets no error to hide behind.
func (f *buildFixture) forbid(path, name string) {
	f.t.Helper()
	f.writeScript(path, "set -eu\nprintf '%s\\n' "+name+" >>\"$HERDR_TEST_DIR/"+attemptsFile+"\"\n")
}

func (f *buildFixture) writeScript(path, body string) {
	f.t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

// fakeGo puts the stand-in toolchain on the fixture PATH. exit is the status it
// reports; 0 emulates a successful build.
func (f *buildFixture) fakeGo(exit string) {
	f.t.Helper()
	f.writeScript(filepath.Join(f.bin, "go"), fakeGoScript)
	f.goExit, f.hasGo = exit, true
}

// run executes the real build script the way the manifest does (`sh
// scripts/build.sh` from the plugin root) and returns its streams and exit
// status. A nonzero status is a result to assert on, not a test failure.
func (f *buildFixture) run() (stdout, stderr string, code int) {
	f.t.Helper()
	cmd := exec.Command(f.sh, "scripts/build.sh")
	cmd.Dir = f.dir
	cmd.Env = []string{
		"PATH=" + f.bin,
		"HOME=" + f.dir,
		"HERDR_TEST_DIR=" + f.dir,
	}
	if f.hasGo {
		cmd.Env = append(cmd.Env, "FAKE_GO_EXIT="+f.goExit)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		f.t.Fatalf("run scripts/build.sh: %v", err)
	}
	return out.String(), errOut.String(), code
}

// goArgv returns the arguments the fake toolchain was called with, or nil when
// it was never invoked.
func (f *buildFixture) goArgv() []string {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, "go-argv"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	argv := strings.Split(string(raw), "\x00")
	return argv[:len(argv)-1] // trailing separator, not an empty argument
}

// requireNoFetch fails when the build ran the standalone installer or any
// downloader. This is the guard the exact-source requirement turns on: the
// plugin must be the checked-out code, never an upstream release binary.
func (f *buildFixture) requireNoFetch() {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, attemptsFile))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Errorf("build tried to fetch a prebuilt binary; ran: %s", strings.Join(strings.Fields(string(raw)), ", "))
}

// output returns the built binary's contents, or "" when it was not produced.
func (f *buildFixture) output() string {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, "bin", "herdr-plus"))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return string(raw)
}

// TestBuildScriptBuildsCheckedOutSource is the success path herdr takes at
// install time: with a toolchain present the script builds this directory into
// bin/herdr-plus, passes exactly the expected arguments, keeps its progress
// chatter on stderr, and downloads nothing.
func TestBuildScriptBuildsCheckedOutSource(t *testing.T) {
	f := newBuildFixture(t)
	f.fakeGo("0")

	stdout, stderr, code := f.run()

	if code != 0 {
		t.Fatalf("exit status = %d, want 0\nstderr: %s", code, stderr)
	}
	if argv := f.goArgv(); !slices.Equal(argv, wantBuildArgv) {
		t.Errorf("go argv = %q, want %q", argv, wantBuildArgv)
	}
	if got := f.output(); got != "fake herdr-plus binary\n" {
		t.Errorf("bin/herdr-plus = %q, want the toolchain's output", got)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want it empty so herdr's captured output stays clean", stdout)
	}
	if !strings.Contains(stderr, "building from source") {
		t.Errorf("stderr = %q, want it to report the source build", stderr)
	}
	f.requireNoFetch()
}

// TestBuildScriptPropagatesGoBuildFailure covers a checkout that does not
// compile: the failure must stop the install with the toolchain's own status
// and message, never fall back to a release binary that would compile.
func TestBuildScriptPropagatesGoBuildFailure(t *testing.T) {
	f := newBuildFixture(t)
	f.fakeGo("3")

	_, stderr, code := f.run()

	if code != 3 {
		t.Errorf("exit status = %d, want the toolchain's 3\nstderr: %s", code, stderr)
	}
	if argv := f.goArgv(); !slices.Equal(argv, wantBuildArgv) {
		t.Errorf("go argv = %q, want %q", argv, wantBuildArgv)
	}
	if !strings.Contains(stderr, "fake go: deliberate build failure") {
		t.Errorf("stderr = %q, want the toolchain's diagnostic preserved", stderr)
	}
	if got := f.output(); got != "" {
		t.Errorf("bin/herdr-plus = %q, want no binary from a failed build", got)
	}
	f.requireNoFetch()
}

// TestBuildScriptRequiresGoToolchain covers the machine the fallback used to
// serve: no Go on PATH. The install must fail with an actionable diagnostic
// rather than silently installing an upstream binary that lacks this
// checkout's code, and must leave no half-built bin directory behind.
func TestBuildScriptRequiresGoToolchain(t *testing.T) {
	f := newBuildFixture(t) // deliberately no fakeGo

	stdout, stderr, code := f.run()

	if code == 0 {
		t.Fatalf("exit status = 0, want a failure when no Go toolchain is present\nstderr: %s", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want the diagnostic on stderr only", stdout)
	}
	for _, want := range []string{"no Go toolchain", "https://go.dev", "PATH"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", stderr, want)
		}
	}
	if got := f.output(); got != "" {
		t.Errorf("bin/herdr-plus = %q, want no binary without a toolchain", got)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "bin")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("bin/ exists after a failed build (stat err %v), want the tree left untouched", err)
	}
	f.requireNoFetch()
}
