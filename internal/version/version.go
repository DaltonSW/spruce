// Package version checks whether a newer spruce release is available on
// GitHub. It is a read-only network lookup against the public releases API
// (https://api.github.com/repos/DaltonSW/spruce/releases/latest); the result
// is surfaced in the TUI header as a one-line "update available" notice.
//
// The check never blocks the UI: the caller runs it in a Bubble Tea command,
// and any failure (offline, rate-limited, unparseable response) is swallowed
// so the app behaves exactly as before when no result can be obtained.
package version

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
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

// ResolveDev builds a descriptive version string for a local ("dev") build by
// querying the git working tree: the most recent tag, the short commit hash,
// and a "-dev" suffix. The result is e.g. "v1.2.3-abc1234-dev". When there are
// no tags yet it falls back to just the commit hash: "abc1234-dev".
//
// If git is unavailable or the directory isn't a repo, it returns "dev" so the
// app still works — just without the richer version label. It never errors;
// callers can use the result unconditionally as the version string.
func ResolveDev() string {
	tag := gitTag()
	commit := gitShortCommit()
	if tag == "" && commit == "" {
		return "dev"
	}
	parts := []string{tag, commit, "dev"}
	return strings.Join(parts, "-")
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
// is newer than current. current is the build-time version stamp ("dev" for a
// local build, or a goreleaser tag like "v1.2.3"). A "dev" build is always
// considered up-to-date so the notice never fires for developers running from
// source.
//
// The HTTP request is bounded by ctx; a 10s timeout is applied as a safety net
// so a stalled connection can't hold the command open indefinitely. Any error
// returns a zero Result (Available: false) rather than propagating, so a
// failed check is invisible to the user.
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

// shouldCheck reports whether a version check is worth performing. "dev" (the
// default for a local `go run`/`go build` with no ldflags), the resolved dev
// form ("v1.2.3-abc1234-dev"), and empty strings all skip the check — there's
// no meaningful comparison to make against a release tag, and developers don't
// need a "new release" banner on every run.
func shouldCheck(current string) bool {
	current = strings.TrimSpace(current)
	if current == "" || current == "dev" {
		return false
	}
	return !strings.HasSuffix(current, "-dev")
}

// segment is one dotted piece of a version string, split into its leading
// numeric part and whatever remains (suffix, pre-release label, etc.).
type segment struct {
	num  int
	rest string
}

// isNewer reports whether latest is a higher version than current, comparing
// them as trimmed version tags (both may carry a leading "v"). The comparison
// walks the dotted numeric segments left to right; the first segment that
// differs decides. Tags that don't parse as numbers fall back to a plain
// string comparison on the remainder so a non-numeric tag never causes a
// false "update available".
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
