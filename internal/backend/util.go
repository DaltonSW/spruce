package backend

import (
	"os"
	"strings"
)

// envBase returns a copy of the current environment for child processes.
func envBase() []string {
	return append([]string(nil), os.Environ()...)
}

// lastOf returns the last element of a slice, or "" if empty. brew reports
// installed_versions as a list; the newest is last.
func lastOf(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	return xs[len(xs)-1]
}

// firstField returns the first whitespace-delimited token of s.
func firstField(s string) string {
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i]
	}
	return s
}
