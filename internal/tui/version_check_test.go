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

func TestVersionNoticeContent(t *testing.T) {
	// Update available + version → both lines.
	m := New(context.TODO(), func() {}, Options{Version: "v1.0.0"})
	m.updateVer = &versionResult{Available: true, Latest: "v1.2.3"}
	notice := stripANSI(m.versionNotice())
	if !strings.Contains(notice, "v1.2.3") {
		t.Errorf("versionNotice should mention the latest version, got:\n%s", notice)
	}
	if !strings.Contains(notice, "available") {
		t.Errorf("versionNotice should say 'available', got:\n%s", notice)
	}
	if !strings.Contains(notice, "v1.0.0") {
		t.Errorf("versionNotice should mention the current version, got:\n%s", notice)
	}

	// No update, just the version.
	m2 := New(context.TODO(), func() {}, Options{Version: "v1.0.0"})
	notice2 := stripANSI(m2.versionNotice())
	if strings.Contains(notice2, "available") {
		t.Errorf("versionNotice should not mention 'available' when no update, got:\n%s", notice2)
	}
	if !strings.Contains(notice2, "v1.0.0") {
		t.Errorf("versionNotice should mention the current version, got:\n%s", notice2)
	}

	// No version, no update → empty.
	m3 := New(context.TODO(), func() {}, Options{})
	if got := m3.versionNotice(); got != "" {
		t.Errorf("versionNotice should be empty with no version and no update, got %q", got)
	}
}

func TestHeaderContainsVersionNotice(t *testing.T) {
	m := New(context.TODO(), func() {}, Options{Version: "v1.0.0"})
	m.width, m.height = 100, 30
	m.updateVer = &versionResult{Available: true, Latest: "v1.2.3"}

	hdr := stripANSI(m.headerView())
	if !strings.Contains(hdr, "v1.0.0") {
		t.Errorf("header should contain the build version, got:\n%s", hdr)
	}
	if !strings.Contains(hdr, "available") {
		t.Errorf("header should contain the update notice, got:\n%s", hdr)
	}

	noticeLines := len(strings.Split(m.versionNotice(), "\n"))
	if want := len(bannerLines) + noticeLines + 2; m.headerHeight(m.width) != want {
		t.Errorf("headerHeight = %d, want %d", m.headerHeight(m.width), want)
	}
}

func TestViewFitsTerminalWithVersionNotice(t *testing.T) {
	m := gridModel(map[string]int{"system": 220, "brew": 6, "flatpak": 3, "snap": 2})
	m.updateVer = &versionResult{Available: true, Latest: "v9.9.9"}

	view := m.View()
	out := strings.TrimRight(view.Content, "\n")
	raw := strings.TrimRight(m.viewSelecting(), "\n")
	if out != raw {
		t.Errorf("View() should match viewSelecting() exactly, got:\n%s\nwant:\n%s", out, raw)
	}
	if !strings.Contains(out, "v9.9.9") {
		t.Errorf("View() should contain the update notice text")
	}
}
