package tui

import (
	"regexp"
	"strconv"
)

func partialJSONField(partialJSON, field string) (string, bool) {
	// Both patterns share the same core: an escape sequence (\\.) takes
	// priority over a plain non-quote char ([^"]), so escaped quotes inside
	// the value are consumed rather than terminating the match.
	//
	// Complete: requires an unescaped closing quote at the end.
	// We validate with Unquote because the greedy engine can still produce a
	// spurious match when the buffer ends mid-escape (e.g. content ends with \\").
	completeRe := regexp.MustCompile(
		`"` + regexp.QuoteMeta(field) + `"` + `\s*:\s*"((?:\\.|[^"])*)"`,
	)
	for _, m := range completeRe.FindAllStringSubmatch(partialJSON, -1) {
		if s, err := strconv.Unquote(`"` + m[1] + `"`); err == nil {
			return s, s != ""
		}
	}

	// Partial: value still open — no unescaped closing quote yet.
	// [^"\\] excludes both quote and backslash so we never cross an escape
	// boundary. The trailing \\? absorbs a lone \\ at end-of-buffer (the
	// second byte of an escape pair hasn't arrived yet).
	partialRe := regexp.MustCompile(
		`"` + regexp.QuoteMeta(field) + `"` + `\s*:\s*"((?:\\.|[^"\\])*)\\?$`,
	)
	if m := partialRe.FindStringSubmatch(partialJSON); len(m) == 2 && m[1] != "" {
		if s, err := strconv.Unquote(`"` + m[1] + `"`); err == nil {
			return s, s != ""
		}
		return m[1], true
	}

	return "", false
}
