//
// Date: 2026-06-15
// Author: Spicer Matthews (spicer@cloudmanic.com)
// Copyright: 2026 Cloudmanic Labs, LLC. All rights reserved.
//

package main

import (
	"context"
	"os"
	"os/exec"
	"time"
)

// launchQuickActions is the Quick Actions action's entry point. herdr runs it
// server-side (from the plugin action / keybinding), so it has no terminal of its
// own. It verifies the pane the action fired from, then asks herdr to open the
// action picker as a pane over that pane — overlay by default, or the placement
// set by [quick_actions].placement in config.toml — passing the encoded context
// along so the chosen command runs in the directory you launched from, not the
// picker's. herdr creates the pane and, when the picker exits, tears it down and
// restores your previous focus.
//
// The invoking pane is established explicitly, exactly as the worktree action
// does it: the picker is placed with --target-pane over the pane named in the
// action context, never over whatever pane holds global focus by the time herdr
// gets here — that may belong to another client's window. Failures are reported
// through actionErrExit so they appear as a notification rather than only in the
// plugin log, since this action has no terminal to print to.
func launchQuickActions() {
	pc, err := pluginContextFromEnv()
	if err != nil {
		actionErrExit(err)
	}
	client, err := newHerdrClient()
	if err != nil {
		actionErrExit(err)
	}
	client.timeout = 15 * time.Second
	ctx, err := quickActionsInvocation(client, pc)
	if err != nil {
		actionErrExit(err)
	}
	enc, err := ctx.encode()
	if err != nil {
		actionErrExit("could not encode run context:", err)
	}

	cfg, err := loadPluginConfig()
	if err != nil {
		actionErrExit(err)
	}
	placement := resolvePlacement(cfg.QuickActions.Placement, "overlay")

	// HERDR_BIN_PATH points at the running herdr binary; it is the portable way to
	// call back into the CLI from a plugin command.
	herdr := os.Getenv("HERDR_BIN_PATH")
	if herdr == "" {
		herdr = "herdr"
	}

	args := []string{
		"plugin", "pane", "open",
		"--plugin", pluginID,
		"--entrypoint", paneEntrypoint("quick-actions-picker"),
		"--placement", placement,
		// Place the picker over the pane that invoked the action. target_pane_id
		// is independent of placement in the native schema, so this pins where the
		// overlay lands without changing which placement the user configured.
		"--target-pane", ctx.PaneId,
		// Hand the launch context to the picker as a single shell-safe env var.
		"--env", "HERDR_PLUS_CTX=" + enc,
	}
	// IMPORTANT: do not add --cwd here. The manifest registers this pane with a
	// relative command (./bin/herdr-plus), which herdr resolves against the pane's
	// working directory — so the pane must run in the plugin's own install dir for
	// that path to resolve. Passing --cwd <launch dir> made herdr look for
	// ./bin/herdr-plus inside the launch directory and fail to spawn the picker.
	// The launch directory still reaches the picker — and the action it runs —
	// through HERDR_PLUS_CTX (ctx.WorkDir), which sets each command's cmd.Dir and
	// the per-repo action lookup. So --cwd was purely cosmetic; dropping it loses
	// nothing and matches how the projects pane (which never set it) already works.

	// Bounded like the worktree action's launch: a herdr CLI that never returns
	// must not leave the action hanging with no terminal to interrupt.
	deadline, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(deadline, herdr, args...)
	cmd.WaitDelay = time.Second
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		actionErrExit("could not open the quick-actions picker:", err)
	}
}
