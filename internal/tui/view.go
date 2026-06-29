package tui

import (
	"fmt"
	"math"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	colorful "github.com/lucasb-eyer/go-colorful"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// gradPalette is the cyclic colour loop the loading border sweeps through —
// spruce's blue/purple/pink accents. mustHex panics only on a bad literal.
var gradPalette = []colorful.Color{
	mustHex("#5fd7ff"), // cyan-blue
	mustHex("#af87ff"), // purple
	mustHex("#ff5fd7"), // pink
}

func mustHex(s string) colorful.Color {
	c, err := colorful.Hex(s)
	if err != nil {
		panic(err)
	}
	return c
}

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
	if len(m.panelSources()) == 0 {
		return dimStyle.Render("No supported package managers found.") + "\n\n" +
			helpStyle.Render("q quit")
	}

	ps := m.panels()
	focusedSrc := ""
	if m.focus >= 0 && m.focus < len(ps) {
		focusedSrc = ps[m.focus]
	}
	grid := renderColumns(ps, m.width, m.selectAvailHeight(), func(src string, w, h int) string {
		return m.renderPanel(src, w, h, src == focusedSrc)
	})

	var b strings.Builder
	b.WriteString(grid + "\n")
	status := dimStyle.Render(fmt.Sprintf("%d of %d selected", m.countSelected(), len(m.rows)))
	if n := len(m.checking); n > 0 {
		status += dimStyle.Render(fmt.Sprintf("  ·  %s checking %d…", m.spin(), n))
	}
	status += m.dryRunBadge()
	b.WriteString(status + "\n")
	b.WriteString(helpStyle.Render(
		"↑/↓ move · ←/→/tab panel · space toggle · a all · N none · d dry-run · enter review · q quit"))
	return b.String()
}

// panelSources is every backend that should get a panel: all detected backends
// (even those still checking or with zero updates). Falls back to the
// rows-derived order when discovered isn't populated (e.g. in tests).
func (m Model) panelSources() []string {
	if len(m.discovered) > 0 {
		return m.discovered
	}
	return m.orderedSources()
}

// panels returns the backends to render, in display order. Available() yields
// the system backend (PackageKit, the big one) first, so it lands in the tall
// left column with the rest stacked on the right. Using this fixed order rather
// than a live row-count keeps the layout stable while results stream in.
func (m Model) panels() []string {
	return m.panelSources()
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
// block above (2) and the count/help lines below (2).
func (m Model) selectAvailHeight() int {
	return max(m.height-4, 6)
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
	for _, s := range m.panelSources() {
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

	checking := m.checking[src]
	errText, errored := m.errs[src]

	// Header: "SYSTEM  3/210"; while checking, a spinner stands in for the count.
	right := fmt.Sprintf(" %d/%d", sel, len(rs))
	if checking {
		right = " " + m.spin()
	}
	title := truncate(strings.ToUpper(src), max(innerW-lipgloss.Width(right), 1))
	header := padRight(groupStyle.Render(title)+dimStyle.Render(right), innerW)

	lines := make([]string, 0, innerH)
	lines = append(lines, header)

	contentH := max(innerH-1, 1)
	switch {
	case checking:
		fillCentered(&lines, contentH, innerW, dimStyle.Render(m.spin()+" checking for updates…"))
	case errored:
		fillCentered(&lines, contentH, innerW, errStyle.Render(truncate("✗ "+errText, max(innerW-2, 1))))
	case len(rs) == 0:
		// Detected but nothing to upgrade: show the box with a reassuring note.
		fillCentered(&lines, contentH, innerW, okStyle.Render("Everything up-to-date!"))
	default:
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
	}

	// While checking, draw a gradient border whose phase rotates each tick;
	// otherwise a solid border (pink when focused, dim otherwise).
	if checking {
		return gradientBox(lines, innerW, innerH, float64(m.spinner)*0.03)
	}
	return solidBox(lines, focused)
}

// solidBox wraps content lines in a rounded border: pink when focused, dim
// otherwise.
func solidBox(content []string, focused bool) string {
	border := lipgloss.NewStyle().Border(lipgloss.RoundedBorder())
	if focused {
		border = border.BorderForeground(lipgloss.Color("212"))
	} else {
		border = border.BorderForeground(lipgloss.Color("240"))
	}
	return border.Render(strings.Join(content, "\n"))
}

// renderColumns lays panels out as a grid: the first source fills the tall left
// column, the rest stack on the right. render draws one panel at a given size.
func renderColumns(sources []string, totalW, availH int, render func(src string, w, h int) string) string {
	if totalW <= 0 {
		totalW = 80
	}
	if len(sources) == 1 {
		return render(sources[0], totalW, availH)
	}
	leftW := totalW / 2
	rightW := totalW - leftW - 1 // 1-col gap
	left := render(sources[0], leftW, availH)

	rights := sources[1:]
	boxes := make([]string, len(rights))
	base, rem := availH/len(rights), availH%len(rights)
	for i, s := range rights {
		h := base
		if i < rem {
			h++
		}
		boxes[i] = render(s, rightW, h)
	}
	right := lipgloss.JoinVertical(lipgloss.Left, boxes...)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
}

// gradientBox wraps content (exactly innerH lines, each innerW wide) in a
// rounded border whose colour sweeps the palette around the perimeter. phase
// (in palette-loops) rotates the gradient; advance it over time to animate.
func gradientBox(content []string, innerW, innerH int, phase float64) string {
	w, h := innerW+2, innerH+2
	perim := 2*w + 2*(h-2)

	cell := func(rowt, col int, r rune) string {
		t := float64(perimIndex(rowt, col, w, h))/float64(perim) + phase
		c := loopColor(t)
		return lipgloss.NewStyle().Foreground(lipgloss.Color(c.Hex())).Render(string(r))
	}

	var b strings.Builder
	b.WriteString(cell(0, 0, '╭'))
	for col := 1; col < w-1; col++ {
		b.WriteString(cell(0, col, '─'))
	}
	b.WriteString(cell(0, w-1, '╮') + "\n")

	for r := range innerH {
		b.WriteString(cell(r+1, 0, '│'))
		b.WriteString(content[r])
		b.WriteString(cell(r+1, w-1, '│') + "\n")
	}

	b.WriteString(cell(h-1, 0, '╰'))
	for col := 1; col < w-1; col++ {
		b.WriteString(cell(h-1, col, '─'))
	}
	b.WriteString(cell(h-1, w-1, '╯'))
	return b.String()
}

// perimIndex maps a border cell to its clockwise position around the perimeter,
// starting at the top-left corner, so adjacent cells get adjacent gradient stops.
func perimIndex(row, col, w, h int) int {
	switch {
	case row == 0: // top edge, left → right
		return col
	case col == w-1: // right edge, top → bottom (corners counted in top/bottom)
		return w + (row - 1)
	case row == h-1: // bottom edge, right → left
		return w + (h - 2) + (w - 1 - col)
	default: // left edge, bottom → top
		return 2*w + (h - 2) + ((h - 2) - row)
	}
}

// loopColor samples the palette as a closed loop at fractional position t,
// blending in HCL space for an even sweep.
func loopColor(t float64) colorful.Color {
	n := len(gradPalette)
	t -= math.Floor(t) // wrap to [0,1)
	x := t * float64(n)
	i := int(x) % n
	j := (i + 1) % n
	return gradPalette[i].BlendHcl(gradPalette[j], x-math.Floor(x)).Clamped()
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

// viewReviewing keeps the Selecting grid as a backdrop and floats a small
// confirmation modal over it, summarizing how much will change per backend.
func (m Model) viewReviewing() string {
	backdrop := m.viewSelecting()
	modal := m.reviewModal()

	x := max((m.width-lipgloss.Width(modal))/2, 0)
	y := max((lipgloss.Height(backdrop)-lipgloss.Height(modal))/2, 0)

	bg := lipgloss.NewLayer(backdrop)
	fg := lipgloss.NewLayer(modal).X(x).Y(y).Z(1)
	return lipgloss.NewCompositor(bg, fg).Render()
}

// reviewModal is the floating confirmation box: one line per backend with its
// count, a total, and the confirm/cancel hint.
func (m Model) reviewModal() string {
	sel := m.selectionByBackend()

	var rows []string
	total := 0
	for _, s := range m.panelSources() {
		n := len(sel[s])
		if n == 0 {
			continue
		}
		total += n
		rows = append(rows, fmt.Sprintf("%s  %s",
			padRight(groupStyle.Render(strings.ToUpper(s)), 10),
			fmt.Sprintf("%d package%s", n, plural(n))))
	}

	body := []string{titleStyle.Render("Apply updates?") + m.dryRunBadge(), ""}
	if total == 0 {
		body = append(body, dimStyle.Render("Nothing selected."))
	} else {
		body = append(body, rows...)
		body = append(body, "",
			fmt.Sprintf("%s across %d package manager%s",
				okStyle.Render(fmt.Sprintf("%d package%s", total, plural(total))),
				len(rows), plural(len(rows))))
	}
	body = append(body, "", helpStyle.Render("enter/y apply   ·   d dry run   ·   esc cancel"))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("212")).
		Background(lipgloss.Color("236")).
		Padding(1, 3).
		Render(lipgloss.JoinVertical(lipgloss.Left, body...))
}

// dryRunBadge returns a " DRY RUN" tag for the headers when simulating.
func (m Model) dryRunBadge() string {
	if !m.dryRun {
		return ""
	}
	return "  " + pinStyle.Render("DRY RUN")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
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
	b.WriteString(titleStyle.Render(header) + m.dryRunBadge() + "\n\n")

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

// fillCentered appends h lines of width w, with msg centered on the middle one.
func fillCentered(lines *[]string, h, w int, msg string) {
	mid := h / 2
	for k := range h {
		if k == mid {
			*lines = append(*lines, padCenter(msg, w))
		} else {
			*lines = append(*lines, padRight("", w))
		}
	}
}

// padCenter pads s with spaces on both sides to a display width of w.
func padCenter(s string, w int) string {
	d := w - lipgloss.Width(s)
	if d <= 0 {
		return s
	}
	left := d / 2
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", d-left)
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
