package tui

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

// stripANSI removes ANSI escape sequences so tests can check the visible text
// of styled output (e.g. the gradient-rendered version string).
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// footerNotice returns the version (and update hint) text that View composites
// at the bottom-right of the screen. When a newer release is available the
// "update available" line appears above the version line. The version line is
// rendered with the banner gradient, so substring checks strip ANSI codes first.
func TestFooterNoticeContent(t *testing.T) {
	// Update available + version → both lines.
	m := New(context.TODO(), func() {}, Options{Version: "v1.0.0"})
	m.updateVer = &versionResult{Available: true, Latest: "v1.2.3"}
	notice := stripANSI(m.footerNotice())
	if !strings.Contains(notice, "v1.2.3") {
		t.Errorf("footerNotice should mention the latest version, got:\n%s", notice)
	}
	if !strings.Contains(notice, "available") {
		t.Errorf("footerNotice should say 'available', got:\n%s", notice)
	}
	if !strings.Contains(notice, "v1.0.0") {
		t.Errorf("footerNotice should mention the current version, got:\n%s", notice)
	}

	// No update, just the version.
	m2 := New(context.TODO(), func() {}, Options{Version: "v1.0.0"})
	notice2 := stripANSI(m2.footerNotice())
	if strings.Contains(notice2, "available") {
		t.Errorf("footerNotice should not mention 'available' when no update, got:\n%s", notice2)
	}
	if !strings.Contains(notice2, "v1.0.0") {
		t.Errorf("footerNotice should mention the current version, got:\n%s", notice2)
	}

	// No version, no update → empty.
	m3 := New(context.TODO(), func() {}, Options{})
	if got := m3.footerNotice(); got != "" {
		t.Errorf("footerNotice should be empty with no version and no update, got %q", got)
	}
}

// The header must not contain the version or update notice — those live in the
// bottom-right footer overlay, not the banner.
func TestHeaderDoesNotContainVersionOrNotice(t *testing.T) {
	m := New(context.TODO(), func() {}, Options{Version: "v1.0.0"})
	m.width, m.height = 100, 30
	m.updateVer = &versionResult{Available: true, Latest: "v1.2.3"}

	hdr := m.headerView(m.width)
	if strings.Contains(hdr, "v1.0.0") {
		t.Errorf("header should not contain the build version, got:\n%s", hdr)
	}
	if strings.Contains(hdr, "available") {
		t.Errorf("header should not contain the update notice, got:\n%s", hdr)
	}

	// headerHeight is just the banner, no extra lines for the notice.
	want := len(bannerLines)
	if got := m.headerHeight(m.width); got != want {
		t.Errorf("headerHeight = %d, want %d (notice is a footer overlay, not a header line)", got, want)
	}
}

// The footer notice is composited at the bottom-right without adding extra
// lines beyond what the body already produces, and the notice text appears in
// the rendered output.
func TestViewFitsTerminalWithFooterNotice(t *testing.T) {
	m := gridModel(map[string]int{"system": 220, "brew": 6, "flatpak": 3, "snap": 2})
	m.updateVer = &versionResult{Available: true, Latest: "v9.9.9"}

	view := m.View()
	out := strings.TrimRight(view.Content, "\n")

	// The compositor must not add lines beyond the raw header+body.
	raw := m.headerView(m.width) + "\n\n" + m.viewSelecting()
	rawLines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	viewLines := strings.Split(out, "\n")
	if len(viewLines) > len(rawLines) {
		t.Errorf("View() has %d lines, raw has %d — compositor added lines", len(viewLines), len(rawLines))
	}

	// The notice text must be present in the composited output.
	if !strings.Contains(out, "v9.9.9") {
		t.Errorf("View() should contain the update notice text")
	}
}
