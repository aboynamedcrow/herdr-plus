#!/bin/sh
#
# Date: 2026-06-15
# Author: Spicer Matthews (spicer@cloudmanic.com)
# Copyright: 2026 Cloudmanic Labs, LLC. All rights reserved.
#
# Build herdr-plus for `herdr plugin install`. herdr runs this as the manifest's
# [[build]] step after cloning the repo, in the plugin root, with no plugin
# context.
#
# A Go toolchain is required. The plugin must be the exact source herdr just
# checked out, so this script only ever runs `go build` against this directory.
# There is deliberately no fallback to a prebuilt release binary: a published
# release does not contain the code in this checkout, so installing it would
# silently run something other than what was reviewed. A missing toolchain or a
# failed compile fails the install instead. The result is ./bin/herdr-plus,
# which the manifest's actions and panes invoke.

set -eu

if ! command -v go >/dev/null 2>&1; then
	printf '%s\n' \
		'herdr-plus: no Go toolchain found on PATH.' \
		'' \
		'This plugin is built from the source herdr just checked out, so a Go' \
		'toolchain is required. There is no prebuilt-binary fallback, because a' \
		'published release does not contain the code in this checkout.' \
		'' \
		'Install Go from https://go.dev/dl/, make sure `go` is on PATH, then run' \
		'`herdr plugin install` again.' >&2
	exit 1
fi

mkdir -p bin

echo "herdr-plus: building from source (go build)…" >&2
exec go build -o bin/herdr-plus .
