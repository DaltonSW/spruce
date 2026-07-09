package backend

import (
	"reflect"
	"testing"

	"go.dalton.dog/spruce/internal/core"
)

// Real `npm outdated -g --json` shape: an object keyed by package name. Includes
// an already-current entry (same current/latest) and one with no installed
// version, both of which must be filtered out.
const npmOutdatedSample = `{
  "typescript": {
    "current": "5.3.3",
    "wanted": "5.4.5",
    "latest": "5.4.5",
    "location": "/home/u/.npm-global/lib/node_modules/typescript"
  },
  "eslint": {
    "current": "8.57.0",
    "wanted": "9.2.0",
    "latest": "9.2.0",
    "location": "/home/u/.npm-global/lib/node_modules/eslint"
  },
  "npm": {
    "current": "10.5.0",
    "wanted": "10.5.0",
    "latest": "10.5.0",
    "location": "/home/u/.npm-global/lib/node_modules/npm"
  },
  "ghost": {
    "wanted": "2.0.0",
    "latest": "2.0.0",
    "location": "/home/u/.npm-global/lib/node_modules/ghost"
  }
}`

func TestParseNpmOutdated(t *testing.T) {
	got, err := parseNpmOutdated([]byte(npmOutdatedSample))
	if err != nil {
		t.Fatalf("parseNpmOutdated: %v", err)
	}
	// Sorted by name; the current npm entry and the not-installed ghost entry are
	// dropped.
	want := []core.Update{
		{Name: "eslint", CurrentVersion: "8.57.0", NewVersion: "9.2.0", Source: "npm", Kind: "package"},
		{Name: "typescript", CurrentVersion: "5.3.3", NewVersion: "5.4.5", Source: "npm", Kind: "package"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseNpmOutdated:\n got %+v\nwant %+v", got, want)
	}
}

// No outdated packages: npm prints "{}" and exits 0; parsing yields nothing.
func TestParseNpmOutdatedEmpty(t *testing.T) {
	for _, in := range []string{"", "{}", "  {}\n"} {
		got, err := parseNpmOutdated([]byte(in))
		if err != nil {
			t.Fatalf("parseNpmOutdated(%q): %v", in, err)
		}
		if len(got) != 0 {
			t.Errorf("parseNpmOutdated(%q) = %+v, want none", in, got)
		}
	}
}
