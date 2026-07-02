package version

import "testing"

func TestIsNewer(t *testing.T) {
	cases := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{"patch bump", "v1.2.3", "v1.2.4", true},
		{"minor bump", "v1.2.3", "v1.3.0", true},
		{"major bump", "v1.2.3", "v2.0.0", true},
		{"same version", "v1.2.3", "v1.2.3", false},
		{"older release", "v1.2.3", "v1.2.2", false},
		{"no v prefix on latest", "v1.2.3", "1.2.4", true},
		{"no v prefix on either", "1.2.3", "1.3.0", true},
		{"different lengths", "1.2", "1.2.1", true},
		{"different lengths equal", "1.2.0", "1.2", false},
		{"empty current", "", "v1.0.0", false},
		{"empty latest", "v1.0.0", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNewer(tc.current, tc.latest); got != tc.want {
				t.Errorf("isNewer(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
			}
		})
	}
}

func TestShouldCheck(t *testing.T) {
	cases := []struct {
		current string
		want    bool
	}{
		{"v1.2.3", true},
		{"1.0.0", true},
		{"dev", false},
		{"", false},
		{"  ", false},
		{"v1.2.3-abc1234-dev", false},
		{"abc1234-dev", false},
	}
	for _, tc := range cases {
		if got := shouldCheck(tc.current); got != tc.want {
			t.Errorf("shouldCheck(%q) = %v, want %v", tc.current, got, tc.want)
		}
	}
}
