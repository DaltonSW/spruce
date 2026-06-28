package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"go.dalton.dog/spruce/internal/core"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	groupStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("75"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	pinStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)
	helpStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

func (m Model) View() tea.View {
	var body string
	switch m.state {
	case stateDiscovering:
		body = m.viewDiscovering()
	case stateSelecting:
		body = m.viewSelecting()
	case stateReviewing:
		body = m.viewReviewing()
	case stateApplying, stateDone:
		body = m.viewApplying()
	}

	v := tea.NewView(titleStyle.Render("spruce") + "\n\n" + body)
	v.AltScreen = true
	return v
}

func (m Model) spin() string { return spinnerFrames[m.spinner%len(spinnerFrames)] }

func (m Model) viewDiscovering() string {
	return fmt.Sprintf("%s Looking for available package managers…", m.spin())
}

func (m Model) viewSelecting() string {
	if len(m.rows) == 0 {
		return okStyle.Render("Everything is up to date. 🎉") + "\n\n" +
			helpStyle.Render("q quit")
	}

	ps := m.panels()
	availH := m.selectAvailHeight()
	totalW := m.width
	if totalW <= 0 {
		totalW = 80
	}

	var grid string
	if len(ps) == 1 {
		grid = m.renderPanel(ps[0], totalW, availH, m.focus == 0)
	} else {
		leftW := totalW / 2
		rightW := totalW - leftW - 1 // 1-col gap
		left := m.renderPanel(ps[0], leftW, availH, m.focus == 0)

		rights := ps[1:]
		boxes := make([]string, len(rights))
		base, rem := availH/len(rights), availH%len(rights)
		for i, s := range rights {
			h := base
			if i < rem {
				h++
			}
			boxes[i] = m.renderPanel(s, rightW, h, m.focus == i+1)
		}
		right := lipgloss.JoinVertical(lipgloss.Left, boxes...)
		grid = lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
	}

	var b strings.Builder
	b.WriteString(grid + "\n")
	for name, e := range m.errs {
		b.WriteString(errStyle.Render(fmt.Sprintf("! %s check failed: %s", name, e)) + "\n")
	}
	b.WriteString(dimStyle.Render(fmt.Sprintf("%d of %d selected", m.countSelected(), len(m.rows))) + "\n")
	b.WriteString(helpStyle.Render(
		"↑/↓ move · ←/→/tab panel · space toggle · a all · N none · enter review · q quit"))
	return b.String()
}

// panels returns the backends to render, in display order: the largest (most
// rows) first — shown as the tall left column — then the rest, stacked on the
// right. With a single backend it's just that one, full width.
func (m Model) panels() []string {
	srcs := m.orderedSources()
	if len(srcs) <= 1 {
		return srcs
	}
	counts := map[string]int{}
	for _, r := range m.rows {
		counts[r.source]++
	}
	primary := srcs[0]
	for _, s := range srcs {
		if counts[s] > counts[primary] {
			primary = s
		}
	}
	out := make([]string, 0, len(srcs))
	out = append(out, primary)
	for _, s := range srcs {
		if s != primary {
			out = append(out, s)
		}
	}
	return out
}

func (m Model) countSelected() int {
	n := 0
	for _, r := range m.rows {
		if m.selected[r.update.ID()] {
			n++
		}
	}
	return n
}

// sourceRows returns the rows belonging to one backend, in list order.
func (m Model) sourceRows(src string) []row {
	var out []row
	for _, r := range m.rows {
		if r.source == src {
			out = append(out, r)
		}
	}
	return out
}

// selectAvailHeight is the height available to the panel grid, after the title
// block above and the error/count/help lines below.
func (m Model) selectAvailHeight() int {
	return max(m.height-4-len(m.errs), 6)
}

// panelTotalHeight is the bordered height (rows incl. border) of one panel.
func (m Model) panelTotalHeight(src string) int {
	ps := m.panels()
	availH := m.selectAvailHeight()
	if len(ps) <= 1 || src == ps[0] {
		return availH // single, or the tall left column
	}
	rights := ps[1:]
	base, rem := availH/len(rights), availH%len(rights)
	for i, s := range rights {
		if s == src {
			if i < rem {
				return base + 1
			}
			return base
		}
	}
	return base
}

// panelRowCap is how many package rows fit in a panel's inner area, reserving a
// line for the header and (when the list overflows) the scroll status line.
func (m Model) panelRowCap(src string) int {
	innerH := max(m.panelTotalHeight(src)-2, 1) // minus top/bottom border
	contentH := max(innerH-1, 1)                // minus header
	if len(m.sourceRows(src)) > contentH {
		return max(contentH-1, 1) // reserve the "↑/↓" status line
	}
	return contentH
}

// clampPanel keeps a panel's cursor in range and scrolled into view.
func (m *Model) clampPanel(src string) {
	n := len(m.sourceRows(src))
	if n == 0 {
		m.panelCursor[src], m.panelOffset[src] = 0, 0
		return
	}
	c := min(max(m.panelCursor[src], 0), n-1)
	m.panelCursor[src] = c

	capRows := m.panelRowCap(src)
	o := min(m.panelOffset[src], c) // scroll up to keep the cursor in view
	if c >= o+capRows {
		o = c - capRows + 1 // scroll down
	}
	o = min(o, n-capRows)
	o = max(o, 0)
	m.panelOffset[src] = o
}

func (m *Model) clampAllPanels() {
	if m.focus >= len(m.panels()) {
		m.focus = 0
	}
	for _, s := range m.orderedSources() {
		m.clampPanel(s)
	}
}

// renderPanel draws one backend's box at the given total size. Content lines are
// each padded to the inner width and counted exactly so the rounded border wraps
// to (totalW × totalH) precisely, keeping the grid aligned.
func (m Model) renderPanel(src string, totalW, totalH int, focused bool) string {
	innerW := max(totalW-2, 8)
	innerH := max(totalH-2, 1)
	rs := m.sourceRows(src)

	sel := 0
	for _, r := range rs {
		if m.selected[r.update.ID()] {
			sel++
		}
	}

	// Header: "SYSTEM  3/210".
	count := fmt.Sprintf(" %d/%d", sel, len(rs))
	title := truncate(strings.ToUpper(src), max(innerW-lipgloss.Width(count), 1))
	header := padRight(groupStyle.Render(title)+dimStyle.Render(count), innerW)

	lines := make([]string, 0, innerH)
	lines = append(lines, header)

	contentH := max(innerH-1, 1)
	overflow := len(rs) > contentH
	rowCap := contentH
	if overflow {
		rowCap = max(contentH-1, 1)
	}

	offset := m.panelOffset[src]
	if offset < 0 || offset >= len(rs) {
		offset = 0
	}
	end := min(offset+rowCap, len(rs))
	nameW, curW, newW := panelColumns(rs, innerW)

	for i := offset; i < end; i++ {
		isCur := focused && i == m.panelCursor[src]
		lines = append(lines, padRight(m.renderPanelRow(rs[i], isCur, nameW, curW, newW), innerW))
	}
	for k := end - offset; k < rowCap; k++ {
		lines = append(lines, padRight("", innerW))
	}
	if overflow {
		status := fmt.Sprintf("  ↑ %d   ↓ %d", offset, len(rs)-end)
		lines = append(lines, padRight(dimStyle.Render(status), innerW))
	}

	border := lipgloss.NewStyle().Border(lipgloss.RoundedBorder())
	if focused {
		border = border.BorderForeground(lipgloss.Color("212"))
	} else {
		border = border.BorderForeground(lipgloss.Color("240"))
	}
	return border.Render(strings.Join(lines, "\n"))
}

func (m Model) renderPanelRow(r row, isCursor bool, nameW, curW, newW int) string {
	cursor := "  "
	if isCursor {
		cursor = cursorStyle.Render("▶ ")
	}
	check := "[ ]"
	if m.selected[r.update.ID()] {
		check = okStyle.Render("[x]")
	}
	name := padRight(truncate(r.update.Name, nameW), nameW)
	cur := padRight(truncate(displayVersion(r.update.CurrentVersion), curW), curW)
	nv := truncate(displayVersion(r.update.NewVersion), newW)

	line := fmt.Sprintf("%s%s %s  %s%s%s",
		cursor, check, name, dimStyle.Render(cur), dimStyle.Render(" → "), nv)
	if r.update.Pinned {
		line += " " + pinStyle.Render("(pin)")
	}
	return line
}

// panelColumns sizes the name / current / new columns to fit innerW, sized from
// the data but capped so the row never overflows the panel.
func panelColumns(rs []row, innerW int) (nameW, curW, newW int) {
	const overhead = 2 + 3 + 1 + 2 + 3 // cursor, check, space, gap, " → "
	pinSlack := 0
	for _, r := range rs {
		if w := lipgloss.Width(r.update.Name); w > nameW {
			nameW = w
		}
		if w := lipgloss.Width(displayVersion(r.update.CurrentVersion)); w > curW {
			curW = w
		}
		if w := lipgloss.Width(displayVersion(r.update.NewVersion)); w > newW {
			newW = w
		}
		if r.update.Pinned {
			pinSlack = 6 // " (pin)"
		}
	}
	curW = min(curW, 10)
	newW = min(newW, 12)

	avail := max(innerW-overhead-pinSlack, 6)
	nameW = min(nameW, avail-curW-newW)
	if nameW < 4 {
		nameW = 4
		// Shrink versions (new first, then current) to make room.
		if d := nameW + curW + newW - avail; d > 0 {
			if newW-d >= 3 {
				newW -= d
			} else {
				d -= newW - 3
				newW = 3
				curW = max(curW-d, 3)
			}
		}
	}
	return nameW, curW, newW
}

func (m Model) viewReviewing() string {
	sel := m.selectionByBackend()
	var b strings.Builder
	b.WriteString(titleStyle.Render("Review") + "\n\n")

	total := 0
	for _, ups := range sel {
		total += len(ups)
	}

	// Bound the detail so a large selection can't scroll off; we already showed
	// the full list on the previous screen, so a capped summary is enough here.
	budget := max(m.height-9, 3)
	shown := 0

	for _, name := range m.orderedSources() {
		ups := sel[name]
		if len(ups) == 0 {
			continue
		}
		b.WriteString(groupStyle.Render(strings.ToUpper(name)) +
			dimStyle.Render(fmt.Sprintf("  %d package(s)", len(ups))) + "\n")
		for _, u := range ups {
			if shown >= budget {
				b.WriteString(dimStyle.Render(fmt.Sprintf("  … and %d more", total-shown)) + "\n")
				goto footer
			}
			b.WriteString("  • " + u.Name + " " + versionDiff(u) + "\n")
			shown++
		}
	}

footer:
	b.WriteString("\n" + dimStyle.Render(fmt.Sprintf("%d package(s) total. Nothing has changed yet.", total)) + "\n")
	b.WriteString(helpStyle.Render("y/enter apply · esc back · q quit"))
	return b.String()
}

// orderedSources returns the backend names in first-appearance order, so views
// that group by source render in a stable order (maps don't).
func (m Model) orderedSources() []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range m.rows {
		if !seen[r.source] {
			seen[r.source] = true
			out = append(out, r.source)
		}
	}
	return out
}

func (m Model) viewApplying() string {
	var b strings.Builder
	header := "Applying updates"
	if m.state == stateDone {
		header = "Done"
	}
	b.WriteString(titleStyle.Render(header) + "\n\n")

	// Per-source status lines, in a stable order.
	for _, name := range m.orderedSources() {
		b.WriteString(m.sourceStatusLine(name, m.progress[name]) + "\n")
	}

	// Log tail.
	if len(m.logs) > 0 {
		b.WriteString("\n" + dimStyle.Render("— output —") + "\n")
		tail := m.logs
		if len(tail) > 12 {
			tail = tail[len(tail)-12:]
		}
		for _, l := range tail {
			b.WriteString(dimStyle.Render(truncate(l, max(10, m.width-2))) + "\n")
		}
	}

	b.WriteString("\n")
	if m.state == stateDone {
		b.WriteString(helpStyle.Render("q/enter quit"))
	} else {
		b.WriteString(helpStyle.Render("ctrl+c cancel"))
	}
	return b.String()
}

func (m Model) sourceStatusLine(name string, st *srcState) string {
	label := groupStyle.Render(strings.ToUpper(name))
	if st == nil {
		return fmt.Sprintf("%s  %s", label, dimStyle.Render("waiting…"))
	}
	switch {
	case st.failed:
		return fmt.Sprintf("%s  %s", label, errStyle.Render("failed: "+st.errText))
	case st.finished:
		return fmt.Sprintf("%s  %s", label, okStyle.Render(fmt.Sprintf("✓ done (%d upgraded)", st.done)))
	default:
		phase := st.phase
		if phase == "" {
			phase = "working"
		}
		item := ""
		if st.item != "" {
			item = " " + st.item
		}
		return fmt.Sprintf("%s  %s %s%s %s", label, m.spin(), phase, item,
			dimStyle.Render(fmt.Sprintf("(%d done)", st.done)))
	}
}

// versionDiff renders "1.0 → 1.1", dimming the current version and arrow.
func versionDiff(u core.Update) string {
	return versionDiffAligned(u, 0)
}

// versionDiffAligned is versionDiff with the current-version column right-padded
// to curW so the arrows line up across rows. curW of 0 disables padding.
func versionDiffAligned(u core.Update, curW int) string {
	cur := displayVersion(u.CurrentVersion)
	nv := displayVersion(u.NewVersion)
	if curW > 0 {
		cur = padRight(truncate(cur, curW), curW)
	}
	return dimStyle.Render(cur) + dimStyle.Render(" → ") + nv
}

func displayVersion(v string) string {
	if v == "" {
		return "?"
	}
	return v
}

// padRight pads s with spaces to a display width of w (width-aware, so styled
// or wide-rune strings still align).
func padRight(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
