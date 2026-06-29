package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/dustin/go-humanize"
	colorful "github.com/lucasb-eyer/go-colorful"

	"go.dalton.dog/spruce/internal/core"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// dimBorder is the resting border colour for an unfocused panel (xterm 240).
// It anchors the loading gradient so the bright accents sweep over a dim base.
const dimBorder = "#585858"

// gradPalette is the cyclic colour loop the loading border sweeps through. The
// dim unselected-border colour dominates so the bright blue/purple/pink accents
// form a small, compact highlight — a "comet" — that sweeps over a dim base,
// making the motion read clearly instead of blending into a uniform glow. The
// run of dim stops keeps the bright arc to a small fraction of the perimeter.
// mustHex panics only on a bad literal.
var gradPalette = []colorful.Color{
	mustHex(dimBorder),
	mustHex(dimBorder),
	mustHex(dimBorder),
	mustHex(dimBorder),
	mustHex(dimBorder),
	mustHex(dimBorder),
	mustHex(dimBorder),
	mustHex("#5fd7ff"), // cyan-blue
	mustHex("#af87ff"), // purple
	mustHex("#ff5fd7"), // pink
	mustHex(dimBorder),
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

// spin advances every 3rd tick so the braille spinner stays at ~10fps even
// though the tick loop runs at 30fps for a smooth gradient border.
func (m Model) spin() string { return spinnerFrames[(m.spinner/3)%len(spinnerFrames)] }

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
	if b := m.selectedBytes(); b > 0 {
		status += dimStyle.Render("  ·  " + formatBytes(b) + " to download")
	}
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

// selectedBytes is the total download size of the current selection, across all
// backends; 0 when no sizes are known.
func (m Model) selectedBytes() int64 {
	var b int64
	for _, r := range m.rows {
		if m.selected[r.update.ID()] {
			b += r.update.SizeBytes
		}
	}
	return b
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
		nameW, curW, newW, sizeW := panelColumns(rs, innerW)

		for i := offset; i < end; i++ {
			isCur := focused && i == m.panelCursor[src]
			lines = append(lines, padRight(m.renderPanelRow(rs[i], isCur, nameW, curW, newW, sizeW), innerW))
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
		border = border.BorderForeground(lipgloss.Color(dimBorder))
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
		t := float64(perimIndex(rowt, col, w, h))/float64(perim) - phase
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

func (m Model) renderPanelRow(r row, isCursor bool, nameW, curW, newW, sizeW int) string {
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
	if sizeW > 0 {
		line += "  " + dimStyle.Render(padLeft(truncate(formatBytes(r.update.SizeBytes), sizeW), sizeW))
	}
	if r.update.Pinned {
		line += " " + pinStyle.Render("(pin)")
	}
	return line
}

// panelColumns sizes the name / current / new / size columns to fit innerW,
// sized from the data but capped so the row never overflows the panel. The size
// column is dropped when there isn't room for it alongside a readable name.
func panelColumns(rs []row, innerW int) (nameW, curW, newW, sizeW int) {
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
		if r.update.SizeBytes > 0 {
			if w := lipgloss.Width(formatBytes(r.update.SizeBytes)); w > sizeW {
				sizeW = w
			}
		}
		if r.update.Pinned {
			pinSlack = 6 // " (pin)"
		}
	}
	// Cap generously: flatpak versions now carry a disambiguating " (commit)"
	// suffix, and some refs (e.g. freedesktop-sdk-…) are long. The data-driven
	// width means short versions still stay compact.
	curW = min(curW, 24)
	newW = min(newW, 24)
	sizeW = min(sizeW, 9)

	sizeCol := 0
	if sizeW > 0 {
		sizeCol = 2 + sizeW // "  142 MB"
	}
	avail := max(innerW-overhead-pinSlack, 6)
	// Drop the size column rather than crush the name to fit it.
	if sizeW > 0 && avail-curW-newW-sizeCol < 6 {
		sizeW, sizeCol = 0, 0
	}
	avail -= sizeCol

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
	return nameW, curW, newW, sizeW
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
	var totalBytes int64
	for _, s := range m.panelSources() {
		ups := sel[s]
		n := len(ups)
		if n == 0 {
			continue
		}
		total += n
		var bytes int64
		for _, u := range ups {
			bytes += u.SizeBytes
		}
		totalBytes += bytes
		line := fmt.Sprintf("%s  %s",
			padRight(groupStyle.Render(strings.ToUpper(s)), 10),
			padRight(fmt.Sprintf("%d package%s", n, plural(n)), 14))
		if bytes > 0 {
			line += dimStyle.Render(padLeft(formatBytes(bytes), 9))
		}
		rows = append(rows, line)
	}

	body := []string{titleStyle.Render("Apply updates?") + m.dryRunBadge(), ""}
	if total == 0 {
		body = append(body, dimStyle.Render("Nothing selected."))
	} else {
		body = append(body, rows...)
		summary := fmt.Sprintf("%s across %d package manager%s",
			okStyle.Render(fmt.Sprintf("%d package%s", total, plural(total))),
			len(rows), plural(len(rows)))
		if totalBytes > 0 {
			summary += dimStyle.Render("  ·  " + formatBytes(totalBytes) + " to download")
		}
		body = append(body, "", summary)
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

// viewApplying mirrors the Selecting layout: one panel per backend being
// applied, each showing its live status, a progress bar, and its own scrolling
// output — so nothing is collapsed into a single fast-scrolling black box.
func (m Model) viewApplying() string {
	srcs := m.appliedSources()
	if len(srcs) == 0 {
		return dimStyle.Render("Nothing to apply.") + "\n\n" + helpStyle.Render("q quit")
	}

	grid := renderColumns(srcs, m.width, m.selectAvailHeight(), func(src string, w, h int) string {
		return m.renderApplyPanel(src, w, h)
	})

	var b strings.Builder
	b.WriteString(grid + "\n")

	done, total := 0, len(srcs)
	for _, s := range srcs {
		if st := m.progress[s]; st != nil && (st.finished || st.failed) {
			done++
		}
	}
	status := dimStyle.Render(fmt.Sprintf("%d of %d package managers finished", done, total))
	if m.state != stateDone {
		status = dimStyle.Render(fmt.Sprintf("%s applying  ·  %s", m.spin(),
			fmt.Sprintf("%d of %d package managers finished", done, total)))
	}
	status += m.dryRunBadge()
	b.WriteString(status + "\n")

	if m.state == stateDone {
		b.WriteString(helpStyle.Render("q/enter quit"))
	} else {
		b.WriteString(helpStyle.Render("ctrl+c cancel"))
	}
	return b.String()
}

// appliedSources is the set of backends in this apply run — those with a
// selection — in stable display order.
func (m Model) appliedSources() []string {
	sel := m.selectionByBackend()
	var out []string
	for _, s := range m.panelSources() {
		if len(sel[s]) > 0 {
			out = append(out, s)
		}
	}
	return out
}

// pkgStat is the lifecycle of one package during an apply.
type pkgStat int

const (
	statPending pkgStat = iota
	statActive
	statDone
	statFailed
)

// pkgRowStatus classifies the package at index i in the (ordered) selection,
// from whatever the backend has reported. The order of checks matters: a
// completed count (or whole-backend finish) wins over the live "active item" so
// a just-finished package doesn't flicker back to active before the next
// EventPhase arrives. The seen-set covers backends (PackageKit) that run one
// transaction and report by package name without ever emitting EventItemDone.
func pkgRowStatus(i int, name string, st *srcState) pkgStat {
	if st == nil {
		return statPending
	}
	switch {
	case st.finished && !st.failed:
		return statDone
	case i < st.done:
		return statDone
	case st.failed && name == st.item:
		return statFailed
	case st.item != "" && name == st.item:
		return statActive
	case st.seen[name]:
		return statDone
	default:
		return statPending
	}
}

// activeRow is the index the panel scrolls to keep in view: the package being
// worked on now, by name if known, else the next one by completed count.
func activeRow(pkgs []core.Update, st *srcState) int {
	if st == nil {
		return 0
	}
	if st.item != "" {
		for i, u := range pkgs {
			if u.Name == st.item {
				return i
			}
		}
	}
	if st.done < len(pkgs) {
		return st.done
	}
	return max(len(pkgs)-1, 0)
}

// renderApplyPanel draws one backend's live apply box at the given total size:
// a header with a done/total count, then every selected package listed with a
// live status icon (done/active/pending/failed), and an overall progress bar
// pinned to the bottom. When the list is short, the backend's own output tail
// fills the leftover room. The border animates (gradient) while working, turns
// green when finished and red when failed.
func (m Model) renderApplyPanel(src string, totalW, totalH int) string {
	innerW := max(totalW-2, 8)
	innerH := max(totalH-2, 1)
	st := m.progress[src]
	pkgs := m.selectionByBackend()[src]
	total := len(pkgs)

	done := 0
	if st != nil {
		done = st.done
		if st.finished && !st.failed {
			done = total // PackageKit never counts items; its final Done means all
		}
	}
	right := fmt.Sprintf(" %d/%d", done, total)
	if st != nil {
		if d := st.elapsed(); d > 0 {
			right += "  " + formatDuration(d)
		}
	}
	title := truncate(strings.ToUpper(src), max(innerW-lipgloss.Width(right), 1))
	header := padRight(groupStyle.Render(title)+dimStyle.Render(right), innerW)

	lines := make([]string, 0, innerH)
	lines = append(lines, header)
	contentH := max(innerH-1, 1)

	body := make([]string, 0, contentH)

	// Reserve the last line for the overall progress bar (or an error message).
	barH := 0
	if contentH >= 2 {
		barH = 1
	}
	listH := contentH - barH

	// The package list, scrolled so the active item stays in view.
	nameW, curW, newW := applyColumns(pkgs, innerW)
	offset := 0
	if total > listH {
		offset = clampInt(activeRow(pkgs, st)-listH/2, 0, total-listH)
	}
	end := min(offset+listH, total)
	for i := offset; i < end; i++ {
		body = append(body, padRight(m.renderApplyRow(i, pkgs[i], st, nameW, curW, newW), innerW))
	}

	// Short list: fill the gap above the bar with the backend's output tail.
	if gap := listH - (end - offset); gap > 0 {
		var logs []string
		if st != nil {
			logs = st.logs
		}
		if len(logs) > gap {
			logs = logs[len(logs)-gap:]
		}
		for _, l := range logs {
			body = append(body, padRight(dimStyle.Render(truncate(stripCR(l), innerW)), innerW))
		}
		for len(body) < listH {
			body = append(body, padRight("", innerW))
		}
	}

	if barH == 1 {
		var totalBytes int64
		for _, u := range pkgs {
			totalBytes += u.SizeBytes
		}
		body = append(body, padRight(m.applyBottomLine(st, done, total, totalBytes, innerW), innerW))
	}
	for len(body) < contentH {
		body = append(body, padRight("", innerW))
	}
	lines = append(lines, body[:contentH]...)

	// Border colour reflects state; animate the gradient while still working.
	switch {
	case st != nil && st.failed:
		return solidBoxColor(lines, "203")
	case st != nil && st.finished:
		return solidBoxColor(lines, "78")
	case m.state == stateDone:
		return solidBoxColor(lines, "240")
	default:
		return gradientBox(lines, innerW, innerH, float64(m.spinner)*0.03)
	}
}

// renderApplyRow draws one package line in an apply panel: a status icon, the
// name, and its version bump. The active row carries a spinner and its current
// phase/percentage so you can watch the work move down the list.
func (m Model) renderApplyRow(i int, u core.Update, st *srcState, nameW, curW, newW int) string {
	status := pkgRowStatus(i, u.Name, st)
	var icon string
	switch status {
	case statDone:
		icon = okStyle.Render("✓")
	case statActive:
		icon = cursorStyle.Render(m.spin())
	case statFailed:
		icon = errStyle.Render("✗")
	default:
		icon = dimStyle.Render("○")
	}

	name := padRight(truncate(u.Name, nameW), nameW)
	cur := padRight(truncate(displayVersion(u.CurrentVersion), curW), curW)
	nv := truncate(displayVersion(u.NewVersion), newW)
	line := fmt.Sprintf("%s %s  %s%s%s",
		icon, name, dimStyle.Render(cur), dimStyle.Render(" → "), nv)

	// Annotate the in-flight package with what it's doing right now.
	if status == statActive && st != nil {
		note := st.phase
		if frac := stFraction(st); frac > 0 {
			note = strings.TrimSpace(fmt.Sprintf("%s %d%%", note, int(clamp01(frac)*100)))
		}
		if note != "" {
			line += "  " + dimStyle.Render("· "+note)
		}
	}
	return line
}

// applyBottomLine is the panel's summary footer: an error when failed, a done
// note (with elapsed time) when finished, otherwise an overall progress bar with
// a downloaded/total · rate · ETA cluster on the right when sizes are known.
func (m Model) applyBottomLine(st *srcState, done, total int, totalBytes int64, w int) string {
	switch {
	case st != nil && st.failed:
		return errStyle.Render(truncate("✗ "+st.errText, max(w, 1)))
	case st != nil && st.finished:
		note := fmt.Sprintf("✓ done (%d upgraded)", done)
		if d := st.elapsed(); d > 0 {
			note += " · " + formatDuration(d)
		}
		return okStyle.Render(note)
	default:
		frac := 0.0
		if total > 0 {
			frac = (float64(done) + clamp01(stFraction(st))) / float64(total)
		}
		right := rateETA(st, frac, totalBytes)
		barW := w
		if right != "" {
			barW = max(w-lipgloss.Width(right)-2, 4)
		}
		bar := progressBar(frac, barW, st)
		if right == "" {
			return bar
		}
		return bar + "  " + dimStyle.Render(right)
	}
}

// rateETA renders a "64.0 MB/142 MB · 1.2 MB/s · ETA 0:15" cluster from the
// download size and elapsed time. Returns "" when no size is known — the live
// elapsed timer in the header carries the timing in that case. The rate is an
// estimate: progress is item-weighted, not byte-exact, for most backends.
func rateETA(st *srcState, frac float64, totalBytes int64) string {
	if st == nil || totalBytes <= 0 {
		return ""
	}
	downloaded := int64(clamp01(frac) * float64(totalBytes))
	parts := []string{formatBytes(downloaded) + "/" + formatBytes(totalBytes)}
	if el := st.elapsed().Seconds(); el > 0 && downloaded > 0 {
		rate := float64(downloaded) / el
		parts = append(parts, formatBytes(int64(rate))+"/s")
		if remain := float64(totalBytes-downloaded) / rate; remain > 0 {
			parts = append(parts, "ETA "+formatDuration(time.Duration(remain*float64(time.Second))))
		}
	}
	return strings.Join(parts, " · ")
}

// applyColumns sizes the name / current / new columns for an apply panel,
// reserving room for the leading status icon (1 col + space).
func applyColumns(pkgs []core.Update, innerW int) (nameW, curW, newW int) {
	const overhead = 1 + 1 + 2 + 3 // icon, space, gap, " → "
	for _, u := range pkgs {
		if w := lipgloss.Width(u.Name); w > nameW {
			nameW = w
		}
		if w := lipgloss.Width(displayVersion(u.CurrentVersion)); w > curW {
			curW = w
		}
		if w := lipgloss.Width(displayVersion(u.NewVersion)); w > newW {
			newW = w
		}
	}
	curW = min(curW, 10)
	newW = min(newW, 12)

	avail := max(innerW-overhead, 6)
	nameW = max(min(nameW, avail-curW-newW), 4)
	return nameW, curW, newW
}

// clampInt constrains v to [lo, hi]; if the range is empty, lo wins.
func clampInt(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	return min(max(v, lo), hi)
}

// formatBytes renders a byte count in SI units (B/kB/MB/GB/…) via go-humanize,
// matching how the package managers print download sizes. Zero or negative
// renders empty so callers can omit unknown sizes (humanize.Bytes(0) is "0 B").
func formatBytes(n int64) string {
	if n <= 0 {
		return ""
	}
	return humanize.Bytes(uint64(n))
}

// formatDuration renders a duration as m:ss (or h:mm:ss past an hour).
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Seconds() + 0.5)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, (s%3600)/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// padLeft right-aligns s to a display width of w (width-aware).
func padLeft(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}

// progressBar renders a [████░░░░] bar of the given width for fraction f.
func progressBar(f float64, w int, st *srcState) string {
	if w < 4 {
		return strings.Repeat(" ", max(w, 0))
	}
	f = clamp01(f)
	fill := int(f * float64(w))
	color := lipgloss.Color("75")
	switch {
	case st != nil && st.failed:
		color = lipgloss.Color("203")
	case st != nil && st.finished:
		color = lipgloss.Color("78")
	}
	bar := lipgloss.NewStyle().Foreground(color).Render(strings.Repeat("█", fill))
	rest := dimStyle.Render(strings.Repeat("░", w-fill))
	return bar + rest
}

func stFraction(st *srcState) float64 {
	if st == nil {
		return 0
	}
	return st.fraction
}

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// stripCR drops carriage returns so PTY progress lines don't smear the panel.
func stripCR(s string) string {
	if i := strings.LastIndexByte(s, '\r'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimRight(s, "\r\n")
}

// solidBoxColor wraps content lines in a rounded border of the given colour.
func solidBoxColor(content []string, color string) string {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(color)).
		Render(strings.Join(content, "\n"))
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
