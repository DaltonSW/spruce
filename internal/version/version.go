// Package version checks whether a newer spruce release is available on
// GitHub, surfaced in the TUI header as a one-line "update available" notice.
// The caller runs it as a Bubble Tea command; any failure (offline,
// rate-limited, unparseable) is swallowed rather than shown.
package version

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"runtime/debug"
	"strings"
	"time"
)

// latestReleaseURL is the public GitHub API endpoint for spruce's newest
// non-prerelease. It requires no authentication (subject to the usual
// unauthenticated rate limits, which are plenty for a once-per-launch check).
const latestReleaseURL = "https://api.github.com/repos/DaltonSW/spruce/releases/latest"

// githubRelease is the subset of the releases JSON we care about.
type githubRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

// Result is the outcome of a check. Latest is the newest release tag (e.g.
// "v1.2.3") and URL is its GitHub page; when Available is false the other
// fields are zero values.
type Result struct {
	Available bool
	Latest    string
	URL       string
}

// ResolveDev builds a descriptive version string for a local ("dev") build,
// i.e. one where the ldflags-stamped cmd.Version was left at "dev". It
// prefers the version info Go embeds in the binary at build time (works
// regardless of the process's cwd, so it's correct for `go install`d
// binaries run from anywhere) and only falls back to shelling out to git
// against the current working directory — which only produces a real answer
// when run from inside the source checkout — as a last resort. Never errors.
func ResolveDev() string {
	if v := fromBuildInfo(); v != "" {
		return v
	}

	tag := gitTag()
	commit := gitShortCommit()
	parts := make([]string, 0, 3)
	if tag != "" {
		parts = append(parts, tag)
	}
	if commit != "" {
		parts = append(parts, commit)
	}
	if len(parts) == 0 {
		return "dev"
	}
	parts = append(parts, "dev")
	return strings.Join(parts, "-")
}

// fromBuildInfo derives a version string from the Go module/VCS info that
// the toolchain embeds in the binary at build time. When installed via
// `go install module@version`, Main.Version is the resolved tag or
// pseudo-version (e.g. "v1.3.0") and is returned as-is. When built locally
// without a version query (`go build .`, `go install .` from a checkout),
// Main.Version is the literal "(devel)"; in that case the embedded
// vcs.revision/vcs.modified settings (stamped from the local git repo at
// build time, not read at runtime) are used to build a "<rev>-dev" string.
// Returns "" if build info has nothing usable.
func fromBuildInfo() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}

	var revision string
	var modified bool
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if revision == "" {
		return ""
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if modified {
		return revision + "-dev-dirty"
	}
	return revision + "-dev"
}

// gitTag returns the most recent tag reachable from HEAD (e.g. "v1.2.3"), or
// "" if there are no tags or git fails.
func gitTag() string {
	out, err := exec.Command("git", "describe", "--tags", "--abbrev=0").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitShortCommit returns the abbreviated commit hash of HEAD (e.g. "abc1234"),
// or "" if git fails.
func gitShortCommit() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Check fetches the latest spruce release from GitHub and reports whether it
// is newer than current (the build-time version stamp, "dev" for a local
// build). A "dev" build always counts as up-to-date. The request is bounded
// by a 10s timeout; any error returns a zero Result rather than propagating.
func Check(ctx context.Context, current string) Result {
	if !shouldCheck(current) {
		return Result{}
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseURL, nil)
	if err != nil {
		return Result{}
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Result{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return Result{}
	}

	var rel githubRelease
	if err := json.Unmarshal(body, &rel); err != nil {
		return Result{}
	}
	rel.TagName = strings.TrimSpace(rel.TagName)
	if rel.TagName == "" {
		return Result{}
	}

	if !isNewer(current, rel.TagName) {
		return Result{}
	}
	return Result{Available: true, Latest: rel.TagName, URL: rel.HTMLURL}
}

// shouldCheck reports whether a version check is worth performing. An empty
// string is the only thing that skips it — there's nothing to compare
// against. Even a "dev"-flavored current version (e.g. "abc1234-dev") is
// still worth diffing against the latest release: isNewer treats its
// non-numeric leading segment as lower than any numbered release, so it
// correctly surfaces "update available" instead of being silently skipped.
func shouldCheck(current string) bool {
	return strings.TrimSpace(current) != ""
}

// segment is one dotted piece of a version string, split into its leading
// numeric part and whatever remains (suffix, pre-release label, etc.).
type segment struct {
	num  int
	rest string
}

// isNewer reports whether latest is a higher version than current (both may
// carry a leading "v"), comparing dotted numeric segments left to right; the
// first that differs decides. Non-numeric segments fall back to a plain
// string comparison.
func isNewer(current, latest string) bool {
	current = strings.TrimPrefix(strings.TrimSpace(current), "v")
	latest = strings.TrimPrefix(strings.TrimSpace(latest), "v")
	if current == "" || latest == "" {
		return false
	}

	c := splitVersion(current)
	l := splitVersion(latest)
	n := max(len(c), len(l))
	for i := range n {
		cv, lv := segAt(c, i), segAt(l, i)
		if cv.num != lv.num {
			return lv.num > cv.num
		}
		if cv.rest != lv.rest {
			return lv.rest > cv.rest
		}
	}
	return false
}

func splitVersion(s string) []segment {
	parts := strings.Split(s, ".")
	out := make([]segment, len(parts))
	for i, p := range parts {
		out[i] = parseSegment(p)
	}
	return out
}

func segAt(segs []segment, i int) segment {
	if i < len(segs) {
		return segs[i]
	}
	return segment{}
}

// parseSegment splits one dotted piece into a leading integer and the
// remaining text. "2" → {2, ""}; "1rc1" → {1, "rc1"}; "alpha" → {0, "alpha"}.
func parseSegment(s string) segment {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	num := 0
	for _, ch := range s[:i] {
		num = num*10 + int(ch-'0')
	}
	return segment{num: num, rest: s[i:]}
}
