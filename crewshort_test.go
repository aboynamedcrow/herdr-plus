package main

import (
	"strings"
	"testing"
)

func TestCrewShortTask(t *testing.T) {
	a24, a25 := strings.Repeat("a", 24), strings.Repeat("a", 25)
	family := "\U0001F468‍\U0001F469‍\U0001F467‍\U0001F466"
	flag := "\U0001F1F0\U0001F1F7"
	thumb := "\U0001F44D\U0001F3FD"
	keycap := "1️⃣"
	accent := "é"
	for _, c := range []struct{ name, want string }{
		{"", ""},
		{"IC-220", "IC-220"},
		{"IC-220 🧌", "IC-220 🧌"},
		{"IC-220 🧙‍♂️ short", "IC-220 🧙‍♂️ short"},
		{"IC-220 abcdefghijklmnopqrstuvwxy", "IC-220 abcdefghijklmnopqrstuvwxy"},
		{"IC-220 abcdefghijklmnopqrstuvwxyz", "IC-220 abcdefghijklmnopqrstuvwxy…"},
		{"IC-220 🧌 abcdefghijklmnopqrstuvwxyz", "IC-220 🧌 abcdefghijklmnopqrstuvwxy…"},
		{"IC-220 🧙‍♂️ abcdefghijklmnopqrstuvwxyz", "IC-220 🧙‍♂️ abcdefghijklmnopqrstuvwxy…"},
		// A short task stays the same when it is cut again.
		{"IC-220 🧙‍♂️ abcdefghijklmnopqrstuvwxy…", "IC-220 🧙‍♂️ abcdefghijklmnopqrstuvwxy…"},
		{"IC-220 🧌 slack-1234abcd abcdefghijklmnopqrstuvwxyz", "IC-220 🧌 slack-1234abcd abcdefghij…"},
		{"IC-220 " + strings.Repeat("界", 26), "IC-220 " + strings.Repeat("界", 25) + "…"},
		{"IC-220 🧙‍♂️ " + strings.Repeat("é", 26), "IC-220 🧙‍♂️ " + strings.Repeat("é", 25) + "…"},
		// A marker counts only after an issue ID.
		{"🧌 abcdefghijklmnopqrstuvwxyz", "🧌 abcdefghijklmnopqrstuvw…"},
		// An issue ID must end at a space.
		{"IC-220abcdefghijklmnopqrstuvwxyz", "IC-220abcdefghijklmnopqrs…"},
		{"scratch task", "scratch task"},
		{"abcdefghijklmnopqrstuvwxy", "abcdefghijklmnopqrstuvwxy"},
		{"abcdefghijklmnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxy…"},
		{strings.Repeat("界", 26), strings.Repeat("界", 25) + "…"},
		// Trailing space before the cut point goes.
		{"IC-220 " + a24 + " b", "IC-220 " + a24 + "…"},
		{a24 + " b", a24 + "…"},
		{"IC-220 🧌 " + a24 + "\t b", "IC-220 🧌 " + a24 + "…"},
		// A cluster at the cut point stays whole or goes whole.
		{a24 + family, a24 + family},
		{a24 + family + "b", a24 + family + "…"},
		{a25 + family, a25 + "…"},
		{"IC-220 " + a24 + flag + "b", "IC-220 " + a24 + flag + "…"},
		{"IC-220 " + a25 + flag, "IC-220 " + a25 + "…"},
		{"IC-220 " + a24 + thumb + "b", "IC-220 " + a24 + thumb + "…"},
		{"IC-220 " + a25 + thumb, "IC-220 " + a25 + "…"},
		{"IC-220 " + a24 + keycap + "b", "IC-220 " + a24 + keycap + "…"},
		{"IC-220 " + a25 + keycap, "IC-220 " + a25 + "…"},
		{"IC-220 " + a24 + accent + "b", "IC-220 " + a24 + accent + "…"},
		{"IC-220 " + a25 + accent, "IC-220 " + a25 + "…"},
	} {
		if got := crewShortTask(c.name); got != c.want {
			t.Errorf("crewShortTask(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}
