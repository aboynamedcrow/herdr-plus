package main

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// dot names new Workers and Archive tabs with a short task. These values match
// dot bin/task_tab_title.py short_task_title.
var (
	// crewShortMarkers comes from dot bin/slack_profiles.py MARKERS.
	crewShortMarkers = []string{"🧌", "🧙‍♂️"}
	crewShortTabs    = map[string]bool{"Workers": true, "Archive": true}
	crewShortIssue   = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[1-9][0-9]*`)
)

// crewShortLimit is the number of grapheme clusters that a short task keeps
// after its issue ID and marker.
const crewShortLimit = 25

// crewShortTask keeps the issue ID, its marker and at most crewShortLimit
// grapheme clusters of the rest. A cut removes trailing space and adds "…".
func crewShortTask(name string) string {
	end := 0
	if loc := crewShortIssue.FindStringIndex(name); loc != nil {
		if next, _ := utf8.DecodeRuneInString(name[loc[1]:]); loc[1] == len(name) || unicode.IsSpace(next) {
			end = loc[1]
			rest := strings.TrimLeftFunc(name[end:], unicode.IsSpace)
			for _, marker := range crewShortMarkers {
				if rest == marker || strings.HasPrefix(rest, marker+" ") {
					end = len(name) - len(rest) + len(marker)
					break
				}
			}
		}
	}
	rest, prefix := name, ""
	if end > 0 {
		rest, prefix = strings.TrimLeftFunc(name[end:], unicode.IsSpace), name[:end]+" "
	}
	if uniseg.GraphemeClusterCount(rest) <= crewShortLimit {
		return name
	}
	tail, state := rest, -1
	for range crewShortLimit {
		_, tail, _, state = uniseg.FirstGraphemeClusterInString(tail, state)
	}
	return prefix + strings.TrimRightFunc(rest[:len(rest)-len(tail)], unicode.IsSpace) + "…"
}
