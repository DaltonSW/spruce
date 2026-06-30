package backend

import (
	"reflect"
	"testing"
)

// Real `brew upgrade --dry-run acl` output: the formula we asked for plus the
// outdated dependent (vim) brew pulls in, wrapped in the usual tap-trust noise.
const brewDryRunSample = `Warning: The following taps are not trusted:
  dart-lang/dart
Homebrew is currently ignoring formulae from these taps.
==> Downloading bottle manifests
==> Would upgrade 2 outdated packages
acl  2.3.2    -> 2.4.0 (156.7KB)
vim  9.2.0700 -> 9.2.0750 (15.2MB)
`

func TestParseBrewUpgrades(t *testing.T) {
	got := parseBrewUpgrades(brewDryRunSample)
	want := []string{
		"acl 2.3.2 -> 2.4.0 (156.7KB)",
		"vim 9.2.0700 -> 9.2.0750 (15.2MB)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseBrewUpgrades:\n got %q\nwant %q", got, want)
	}
}

// A run with nothing to do (or no recognizable block) yields no lines, so Plan
// adds no notes.
func TestParseBrewUpgradesEmpty(t *testing.T) {
	for _, in := range []string{
		"",
		"==> Downloading bottle manifests\nAlready up-to-date.\n",
	} {
		if got := parseBrewUpgrades(in); len(got) != 0 {
			t.Errorf("parseBrewUpgrades(%q) = %q, want none", in, got)
		}
	}
}
