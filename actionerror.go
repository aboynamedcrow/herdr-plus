package main

import (
	"fmt"
	"strings"
	"time"
)

// Plugin actions run without a terminal. Herdr records their stderr in a log,
// so also show their failure in the UI. A notification failure must never retry
// the action or hide its original diagnostic.
func actionErrExit(args ...any) {
	if client, err := newHerdrClient(); err == nil {
		client.timeout = 2 * time.Second
		message := []rune(strings.TrimSpace(fmt.Sprintln(args...)))
		if len(message) > 1000 {
			message = append(message[:997], '.', '.', '.')
		}
		var result struct{}
		_ = client.call("notification.show", map[string]any{
			"title": "Herdr Plus", "body": string(message),
		}, &result)
	}
	errExit(args...)
}
