package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/dustin/go-humanize"
	colorful "github.com/lucasb-eyer/go-colorful"
	"github.com/superstarryeyes/bit/ansifonts"

	"go.dalton.dog/spruce/internal/core"
)

// helpKeycap renders one binding as a bold-accent key plus its dim label —
// "q quit" — the key colored so it pops against the receding grey description,
// no brackets so the footer stays clean.
func helpKeycap(b key.Binding) string {
	h := b.Help()
	if h.Key == "" && h.Desc == "" {
		return ""
	}
	cap := helpKeyStyle.Render(h.Key)
	if h.Desc == "" {
		return cap
	}
	return cap + " " + dimStyle.Render(h.Desc)
}

// helpRow joins bindings into one footer line with the shared " · " separator,
// skipping disabled/empty bindings. Replaces help.ShortHelpView so the key and
// its description can be styled separately (bold-accent key, dim label); like it,
// a row wider than width is truncated to the items that fit, with a trailing
// ellipsis.
func helpRow(width int, bindings []key.Binding) string {
	sep := helpStyle.Render(sepTight)
	sepW := lipgloss.Width(sep)
	tail := " " + helpStyle.Render("…")
	tailW := lipgloss.Width(tail)

	var b strings.Builder
	total := 0
	for _, bind := range bindings {
		if !bind.Enabled() {
			continue
		}
		s := helpKeycap(bind)
		if s == "" {
			continue
		}
		w := lipgloss.Width(s)
		if total > 0 {
			s = sep + s
			w += sepW
		}
		// Once an item won't fit, stop rather than overflow the row (the layout
		// budgets on the footer fitting width). Append the ellipsis to mark the
		// truncation when there's room for it; otherwise just cut.
		if width > 0 && total+w > width {
			if total+tailW <= width {
				b.WriteString(tail)
			}
			break
		}
		total += w
		b.WriteString(s)
	}
	return b.String()
}

// helpGroups renders labeled footer rows, one per group, with the labels padded
// to a common width so the keycaps align into a column and the labels read as a
// legend down the left edge. Each row's bindings go through helpRow, so per-row
// width truncation still applies (minus the label gutter).
func helpGroups(width int, groups []helpGroup) string {
	labelW := 0
	for _, g := range groups {
		if w := lipgloss.Width(g.label); w > labelW {
			labelW = w
		}
	}
	var b strings.Builder
	for i, g := range groups {
		if i > 0 {
			b.WriteString("\n")
		}
		label := helpGroupStyle.Render(padRight(g.label, labelW))
		b.WriteString(label + "  " + helpRow(width-labelW-2, g.bindings))
	}
	return b.String()
}

// bannerLines is the "spruce" wordmark, rendered once at startup with a static
// cyan→pink gradient (the same accents in gradPalette) baked in as ANSI codes.
// nil when the font can't load, in which case the header falls back to plain text.
// bannerWidth is the widest line, used to decide when the banner fits the terminal.
var (
	bannerLines []string
	bannerWidth int
)

func init() {
	font, err := ansifonts.LoadFont(bannerFont)
	if err != nil {
		return // leave bannerLines nil → plain-title fallback
	}

	bannerLines = ansifonts.RenderTextWithOptions("spruce", font, getBannerOpts())
	for _, ln := range bannerLines {
		if w := lipgloss.Width(ln); w > bannerWidth {
			bannerWidth = w
		}
	}
}

// headerContent is the gradient banner when it fits the terminal width, otherwise
// the plain bold title (also used before the first WindowSizeMsg, when width==0).
func headerContent(width int) string {
	if len(bannerLines) == 0 || bannerWidth > width {
		return titleStyle.Render("spruce")
	}
	return strings.Join(bannerLines, "\n")
}

// gradientText renders s with a left-to-right RGB linear gradient from
// gradientPrimary to gradientSecondary — the same two stops the banner uses —
// applied per rune across the string's visible width. Short strings still get a
// smooth sweep because the factor is computed over the rune count.
func gradientText(s string) string {
	if s == "" {
		return s
	}
	start, _ := colorful.Hex(gradientPrimary)
	end, _ := colorful.Hex(gradientSecondary)
	sr, sg, sb := start.RGB255()
	er, eg, eb := end.RGB255()
	runes := []rune(s)
	n := len(runes)
	var b strings.Builder
	for i, r := range runes {
		factor := 0.0
		if n > 1 {
			factor = float64(i) / float64(n-1)
		}
		cr := int(float64(sr) + factor*float64(int(er)-int(sr)))
		cg := int(float64(sg) + factor*float64(int(eg)-int(sg)))
		cb := int(float64(sb) + factor*float64(int(eb)-int(sb)))
		b.WriteString(lipgloss.NewStyle().
			Foreground(lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", cr, cg, cb))).
			Render(string(r)))
	}
	return b.String()
}

// footerNotice returns the text to show at the bottom-right of the screen,
// opposite the help keys: the build version (always), and when a newer
// release was found, a one-line "update available" hint above it. The version
// line carries the same left-to-right gradient as the banner so it reads as a
// brand accent rather than plain dim text. Returns "" when there is nothing
// to show (no version and no update).
func (m Model) footerNotice() string {
	var lines []string
	if m.updateVer != nil && m.updateVer.Available {
		lines = append(lines, dimStyle.Render("↑ spruce "+m.updateVer.Latest+" is available"))
	}
	if m.version != "" {
		lines = append(lines, gradientText(m.version))
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// headerView positions the header in the empty space above the body: one blank
// line of top padding, with the content horizontally centered across the width.
func (m Model) headerView(width int) string {
	c := headerContent(width)
	if width > 0 {
		c = lipgloss.PlaceHorizontal(width, lipgloss.Center, c)
	}
	return "\n" + c
}

// headerHeight is the number of content lines headerView(width) produces (the
// banner or the plain title), not counting the top-padding line, so the layout
// below can reserve the right amount of vertical space.
func (m Model) headerHeight(width int) int {
	if len(bannerLines) == 0 || bannerWidth > width {
		return 1
	}
	return len(bannerLines)
}

func (m Model) View() tea.View {
	var body string
	switch m.state {
	case stateDiscovering:
		body = m.viewDiscovering()
	case stateSelecting:
		body = m.viewSelecting()
	case stateReviewing:
		body = m.viewReviewing()
	case stateConfirmInstall:
		body = m.viewConfirmInstall()
	case stateApplying, stateDone:
		body = m.viewApplying()
	}

	main := m.headerView(m.width) + "\n\n" + body

	bg := lipgloss.NewLayer(main)

	layers := []*lipgloss.Layer{bg}

	// Overlay the version / update-available notice at the bottom-right of the
	// screen, opposite the help keys. Composited as a top-most layer so it
	// floats over whatever the body rendered there.
	if notice := m.footerNotice(); notice != "" && m.width > 0 && m.height > 0 {
		nw := lipgloss.Width(notice)
		nh := lipgloss.Height(notice)
		x := max(m.width-nw, 0)
		y := max(m.height-nh, 0)
		layers = append(layers, lipgloss.NewLayer(notice).X(x).Y(y).Z(1))
	}

	rendered := lipgloss.NewCompositor(layers...).Render()

	v := tea.NewView(rendered)
	v.AltScreen = true
	v.BackgroundColor = lipgloss.Color(background)
	return v
}

func (m Model) viewDiscovering() string {
	return fmt.Sprintf("%s Looking for available package managers…", m.spinner.View())
}

func (m Model) viewSelecting() string {
	if len(m.panelSources()) == 0 {
		return dimStyle.Render("No supported package managers found.") + "\n\n" +
			helpRow(m.width, []key.Binding{m.keys.Quit})
	}

	ps := m.panels()
	focusedSrc := ""
	if m.focus >= 0 && m.focus < len(ps) {
		focusedSrc = ps[m.focus]
	}
	grid := m.renderColumns(m.selectLayout(), func(box panelBox) string {
		return m.renderPanel(box.src, box.w, box.h, box.index, box.src == focusedSrc)
	})

	var b strings.Builder
	b.WriteString(grid + "\n")
	status := dimStyle.Render(fmt.Sprintf("%d of %d selected", m.countSelected(), len(m.rows)))
	if b := m.selectedBytes(); b > 0 {
		status += dimStyle.Render(sep + formatBytes(b) + " to download")
	}
	if n := len(m.checking); n > 0 {
		status += dimStyle.Render(fmt.Sprintf("%s%s checking %d…", sep, m.spinner.View(), n))
	}
	status += m.dryRunBadge()
	b.WriteString(status + "\n")
	b.WriteString(helpGroups(m.width, m.keys.selectingHelp()))
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
// the system backend (PackageKit, the big one) first, so it sits at the top of
// the stack and gets the tallest panel. Using this fixed order rather than a
// live row-count keeps the layout stable while results stream in.
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

// selectAvailHeight is the height available to the panel grid, after the header
// block above (top-padding line + content lines + one blank separator line) and
// the count/help lines below: one status line plus the three-row selecting footer
// (4). With the plain 1-line title this is m.height-7. The apply screen's footer
// is shorter, so it just leaves a couple extra blank lines at the bottom.
func (m Model) selectAvailHeight() int {
	return max(m.height-m.headerHeight(m.width)-6, 6)
}

// minStackPanelH is the floor a panel can shrink to: a border (2) plus a header
// and one content row.
const minStackPanelH = 4

// panelGutter is the blank left margin inside every panel, so the header and rows
// don't hug the border. Content is sized to innerW-panelGutter and then prefixed
// with this many spaces, keeping each line exactly innerW wide.
const panelGutter = 1

// sep joins segments of a top-level status line (" 3 selected · 12 MB · …");
// sepTight is the compact form for dense apply rows where width is scarce.
const (
	sep      = "  ·  "
	sepTight = " · "
)

// indent prefixes the panel's left gutter to a line already sized to the content
// width, yielding a line of the full inner width.
func indent(line string) string {
	return strings.Repeat(" ", panelGutter) + line
}

// sourceColor is a backend's accent color (its Color()), or "" when it declares
// none or isn't known — callers fall back to the UI default in that case.
func (m Model) sourceColor(src string) string {
	if b := m.byName[src]; b != nil {
		return b.Color()
	}
	return ""
}

// sourceIcon is the glyph a backend shows beside its name (its Icon()), or "".
func (m Model) sourceIcon(src string) string {
	if b := m.byName[src]; b != nil {
		return b.Icon()
	}
	return ""
}

// sourceLabel is a backend's icon + upper-cased name rendered in its accent
// color — the identity chip the confirm modals show at the head of each row.
func (m Model) sourceLabel(src string) string {
	style := groupStyle
	if c := m.sourceColor(src); c != "" {
		style = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(c))
	}
	name := strings.ToUpper(src)
	if icon := m.sourceIcon(src); icon != "" {
		return style.Render(icon + " " + name)
	}
	return style.Render(name)
}

// panelHeader renders a panel's header line — the backend's icon and name in its
// accent color, with a dim right-hand note (count/spinner/timer) beside it —
// padded to the content width. Shared by the selecting and applying panels so the
// two can't drift. color/icon are the backend's; "" falls back to the UI accent
// and no icon.
func panelHeader(index int, src, right string, contentW int, icon, color string) string {
	style := groupStyle
	if color != "" {
		style = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(color))
	}
	// A bold-accent number badge ("1 ") marks each panel; it's the key you press
	// to jump to it. Reserve its width (and the icon's) so the name truncates, not
	// the badge or icon, on a narrow panel.
	badge, badgeW := "", 0
	if index > 0 {
		n := strconv.Itoa(index)
		badge = helpKeyStyle.Render(n) + " "
		badgeW = lipgloss.Width(n) + 1
	}
	iconW := 0
	if icon != "" {
		iconW = lipgloss.Width(icon) + 1
	}
	name := truncate(strings.ToUpper(src), max(contentW-lipgloss.Width(right)-iconW-badgeW, 1))
	title := name
	if icon != "" {
		title = icon + " " + name
	}
	return padRight(badge+style.Render(title)+dimStyle.Render(right), contentW)
}

// panelLayout returns the total (bordered) height of each panel, in order, given
// the content lines each one wants (its update count, or 1 for a
// checking/errored/up-to-date panel). Each panel is sized to its content —
// border + header + content — so a backend with a single update doesn't sprawl.
// When the panels' natural heights don't all fit in availH, the panel with the
// largest natural height (the system list) is drained all the way to the floor
// before any smaller panel is touched — so the big list shrinks and scrolls
// while the small backends stay whole. If everything fits, the stack is shorter
// than availH and leaves blank space below rather than padding panels out.
func panelLayout(content []int, availH int) []int {
	heights := make([]int, len(content))
	naturals := make([]int, len(content))
	floors := make([]int, len(content))
	for i, c := range content {
		naturals[i] = max(c+3, minStackPanelH) // 2 border + 1 header + content
		heights[i] = naturals[i]
		floors[i] = minStackPanelH
	}
	return shrinkToFit(heights, naturals, floors, availH)
}

// shrinkToFit drains a set of panel/band heights down to availH, reclaiming from
// the entry with the largest natural height first and draining it fully to its
// floor before touching the next-largest — so the biggest item (the system list)
// absorbs the squeeze and scrolls while smaller ones stay whole. heights is
// mutated in place and returned; floors[i] is the smallest height entry i may
// shrink to. When everything is already at its floor the total may still exceed
// availH (the caller overflows the screen), matching the old behavior.
func shrinkToFit(heights, naturals, floors []int, availH int) []int {
	total := 0
	for _, h := range heights {
		total += h
	}
	for total > availH {
		idx := -1
		for i, h := range heights {
			if h <= floors[i] {
				continue // already at the floor; can't shrink further
			}
			if idx < 0 || naturals[i] > naturals[idx] {
				idx = i
			}
		}
		if idx < 0 {
			break // all at the floor; the stack overflows the screen
		}
		take := min(heights[idx]-floors[idx], total-availH)
		heights[idx] -= take
		total -= take
	}
	return heights
}

// Column layout constants. minColW is the narrowest a column may get before we
// stop adding columns (below it, rows aren't readable), so on a narrow terminal
// the layout collapses to a single full-width stack. colGap is the blank column
// between adjacent columns.
const (
	minColW = 64
	colGap  = 1
)

// panelBox is one backend's computed placement: the total (bordered) width and
// height its box gets, and its 1-based number badge (its position in panels()
// order).
type panelBox struct {
	src   string
	w, h  int
	index int
}

// column is a vertical stack of panels; the layout is one or more columns joined
// side by side. Boxes in a column share the column's width.
type column struct {
	boxes []panelBox
}

// naturalsOf is the natural (bordered) height each panel wants: content + 2
// border + 1 header, floored at minStackPanelH.
func naturalsOf(content []int) []int {
	n := make([]int, len(content))
	for i, c := range content {
		n[i] = max(c+3, minStackPanelH)
	}
	return n
}

// columnCount chooses how many columns to split into. A panel taller than the
// screen (the system list) will scroll no matter what, so it takes a column of
// its own; the shorter panels are packed into just enough further columns that
// each column's content fits the height. The result is capped by maxCols (what
// the terminal width allows), so a narrow terminal collapses to one column and a
// short-enough set of backends stays a single readable stack.
func columnCount(naturals []int, availH, maxCols int) int {
	tall, shortSum, shortN := 0, 0, 0
	for _, n := range naturals {
		if n > availH {
			tall++
		} else {
			shortSum += n
			shortN++
		}
	}
	shortCols := 0
	if shortSum > availH { // shorts need more than one column to avoid scrolling
		shortCols = clampInt((shortSum+availH-1)/availH, 1, shortN)
	} else if shortN > 0 {
		shortCols = 1
	}
	return clampInt(tall+shortCols, 1, maxCols)
}

// columnLayout packs panels into balanced columns: each panel (in panels()
// order) drops into the currently-shortest column, so the tall system list lands
// alone in one column and the small backends stack beside it in the next — using
// the horizontal space instead of scrolling the big list under a wasted right
// margin. remeasure, when non-nil, recomputes the content-line counts once the
// column width is known (apply panels wrap their error text to the column width).
func (m Model) columnLayout(sources []string, content []int, remeasure func(colW int) []int) []column {
	availH := m.selectAvailHeight()
	fullW := m.width
	if fullW <= 0 {
		fullW = 80
	}
	maxCols := clampInt((fullW+colGap)/(minColW+colGap), 1, max(len(sources), 1))
	cols := columnCount(naturalsOf(content), availH, maxCols)
	colW := (fullW - (cols-1)*colGap) / cols
	if remeasure != nil {
		content = remeasure(colW)
	}

	// Greedy balance: place each panel into the shortest column so far.
	naturals := naturalsOf(content)
	members := make([][]int, cols)
	colHeight := make([]int, cols)
	for i := range sources {
		best := 0
		for c := 1; c < cols; c++ {
			if colHeight[c] < colHeight[best] {
				best = c
			}
		}
		members[best] = append(members[best], i)
		colHeight[best] += naturals[i]
	}

	// Fit each column's panels to the height independently, reusing the 1-D drain
	// so an over-tall column shrinks its biggest panel (the system list) first.
	columns := make([]column, cols)
	for c := range members {
		idxs := members[c]
		cont := make([]int, len(idxs))
		for k, i := range idxs {
			cont[k] = content[i]
		}
		heights := panelLayout(cont, availH)
		boxes := make([]panelBox, len(idxs))
		for k, i := range idxs {
			boxes[k] = panelBox{src: sources[i], w: colW, h: heights[k], index: i + 1}
		}
		columns[c] = column{boxes: boxes}
	}
	return columns
}

// selectLayout is the column layout for the Selecting screen.
func (m Model) selectLayout() []column {
	return m.columnLayout(m.panels(), m.panelContentLines(), nil)
}

// applyLayout is the column layout for the Applying screen. A failed backend's
// content grows to fit its word-wrapped error, measured at the column width so
// the whole reason stays legible rather than clipped.
func (m Model) applyLayout() []column {
	srcs := m.appliedSources()
	base := make([]int, len(srcs))
	for i, s := range srcs {
		base[i] = max(len(m.applying[s]), 1)
	}
	return m.columnLayout(srcs, base, func(colW int) []int {
		errW := max(colW-2-panelGutter, 4)
		content := make([]int, len(srcs))
		for i, s := range srcs {
			content[i] = base[i]
			if st := m.progress[s]; st != nil && st.failed && st.errText != "" {
				content[i] = base[i] + len(wrapLines("✗ "+st.errText, errW)) + 1
			}
		}
		return content
	})
}

// currentLayout picks the layout for whichever screen is active, so the
// width/height lookups used while syncing tables match what the view renders.
func (m Model) currentLayout() []column {
	switch m.state {
	case stateApplying, stateDone:
		return m.applyLayout()
	default:
		return m.selectLayout()
	}
}

// panelBoxFor returns src's computed box in the current layout.
func (m Model) panelBoxFor(src string) (panelBox, bool) {
	for _, col := range m.currentLayout() {
		for _, box := range col.boxes {
			if box.src == src {
				return box, true
			}
		}
	}
	return panelBox{}, false
}

// panelWidth is the total (bordered) width src's panel gets in the layout.
func (m Model) panelWidth(src string) int {
	if box, ok := m.panelBoxFor(src); ok {
		return box.w
	}
	return max(m.width, 1)
}

// panelTotalHeight is the bordered height (rows incl. border) of one panel.
func (m Model) panelTotalHeight(src string) int {
	if box, ok := m.panelBoxFor(src); ok {
		return box.h
	}
	return m.selectAvailHeight()
}

// panelContentLines is the number of content lines each panel (in panels()
// order) wants, excluding its border and header: the backend's update count, or
// a single line for a checking, errored, or up-to-date panel.
func (m Model) panelContentLines() []int {
	ps := m.panels()
	out := make([]int, len(ps))
	for i, s := range ps {
		_, errored := m.errs[s]
		switch {
		case m.checking[s] || errored:
			out[i] = 1
		default:
			out[i] = max(len(m.sourceRows(s)), 1)
		}
	}
	return out
}

// panelRows splits a panel's content area (contentH lines, header already
// excluded) into the row capacity and whether the scroll status line is shown.
// The status line is reserved only when the list overflows AND there's room for
// at least one row alongside it; in a one-line content area the row wins, so a
// tiny panel never renders past its bounds.
func panelRows(rowCount, contentH int) (rowCap int, showStatus bool) {
	if rowCount > contentH && contentH >= 2 {
		return contentH - 1, true
	}
	return contentH, false
}

// panelInnerWFor is the inner (content) width of src's panel: its assigned
// (bordered) width minus the two border columns. Mirrors the innerW computed in
// renderPanel, but reads the width from the hybrid layout so a tiled (narrow)
// panel is sized to its column, not the full terminal.
func (m Model) panelInnerWFor(src string) int {
	return max(m.panelWidth(src)-2, 8)
}

// panelContentWFor is the writable width inside src's panel: the inner width less
// the left gutter. Tables and columns are sized to this; the gutter is added back
// when the lines are assembled.
func (m Model) panelContentWFor(src string) int {
	return max(m.panelInnerWFor(src)-panelGutter, 4)
}

// panelRowsFor returns how many package rows fit in a panel's content area and
// whether the scroll status line is shown (mirrors renderPanel's accounting).
func (m Model) panelRowsFor(src string) (rowCap int, showStatus bool) {
	innerH := max(m.panelTotalHeight(src)-2, 1) // minus top/bottom border
	contentH := max(innerH-1, 1)                // minus header
	return panelRows(len(m.sourceRows(src)), contentH)
}

// focusedSource is the backend whose panel currently has focus, or "" if none.
func (m Model) focusedSource() string {
	ps := m.panels()
	if m.focus >= 0 && m.focus < len(ps) {
		return ps[m.focus]
	}
	return ""
}

// tableFor returns the persistent table.Model backing src's panel, creating it on
// first use. The table owns the panel's cursor + scroll; spruce renders each row
// itself (into a single full-width column) so the existing per-cell styling —
// checkbox, dim versions, (pin) badge, ▶ marker — carries over verbatim. Cell and
// Selected styles are no-ops because all styling lives in the rendered row.
func (m *Model) tableFor(src string) *table.Model {
	if t, ok := m.tables[src]; ok {
		return t
	}
	t := table.New(
		table.WithStyles(table.Styles{
			Header:   lipgloss.NewStyle(),
			Cell:     lipgloss.NewStyle(),
			Selected: lipgloss.NewStyle(),
		}),
	)
	m.tables[src] = &t
	return &t
}

// syncTable refreshes src's table to match the current rows, selection, focus and
// layout: it sizes the table to the panel and rebuilds every row (so the ▶ marker
// and checkboxes track state). Called whenever any of those change.
func (m *Model) syncTable(src string) {
	if src == "" {
		return
	}
	t := m.tableFor(src)
	contentW := m.panelContentWFor(src)
	rowCap, _ := m.panelRowsFor(src)

	t.SetColumns([]table.Column{{Title: "", Width: contentW}})
	t.SetWidth(contentW)
	t.SetHeight(rowCap + 1) // +1 for the table's (blank) header line

	rs := m.sourceRows(src)
	// A table synced while its backend still had 0 rows (e.g. during streaming
	// discovery, before this backend's Check returns) gets its cursor driven to
	// -1 by bubbles' SetRows underflow, and a later SetRows never lifts it back.
	// A negative cursor makes UpdateViewport render one row short, clipping the
	// last item until the user navigates. Restore it to the top once rows exist.
	if t.Cursor() < 0 && len(rs) > 0 {
		t.SetCursor(0)
	}
	focused := src == m.focusedSource()
	cur := t.Cursor()
	// Size the columns so they line up. A full-width (wide) panel sizes from every
	// backend's rows, since all wide panels share one inner width and stack down
	// the screen; a tiled (narrow) panel is its own separate box, so it sizes from
	// just its own rows at its own column width.
	colRows := m.rows
	if m.panelWidth(src) < m.width {
		colRows = m.sourceRows(src)
	}
	nameW, curW, newW, sizeW := panelColumns(colRows, contentW)
	rows := make([]table.Row, len(rs))
	for i, r := range rs {
		rows[i] = table.Row{m.renderPanelRow(r, focused && i == cur, nameW, curW, newW, sizeW)}
	}
	t.SetRows(rows)
}

// syncAllPanels clamps focus and resyncs every panel's table; used after a resize
// or when results stream in (a growing panel reflows the whole stack's heights).
func (m *Model) syncAllPanels() {
	if m.focus >= len(m.panels()) {
		m.focus = 0
	}
	for _, s := range m.panelSources() {
		m.syncTable(s)
	}
}

// renderPanel draws one backend's box at the given total size. Content lines are
// each padded to the inner width and counted exactly so the rounded border wraps
// to (totalW × totalH) precisely, keeping the grid aligned.
func (m Model) renderPanel(src string, totalW, totalH, index int, focused bool) string {
	innerW := max(totalW-2, 8)
	innerH := max(totalH-2, 1)
	contentW := max(innerW-panelGutter, 4)
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
		right = " " + m.spinner.View()
	}

	lines := make([]string, 0, innerH)
	lines = append(lines, panelHeader(index, src, right, contentW, m.sourceIcon(src), m.sourceColor(src)))

	contentH := max(innerH-1, 1)
	switch {
	case checking:
		fillCentered(&lines, contentH, contentW, dimStyle.Render(m.spinner.View()+" checking for updates…"))
	case errored:
		fillCentered(&lines, contentH, contentW, errStyle.Render(truncate("✗ "+errText, max(contentW-2, 1))))
	case len(rs) == 0:
		// Detected but nothing to upgrade: show the box with a reassuring note.
		fillCentered(&lines, contentH, contentW, okStyle.Render("Everything up-to-date!"))
	default:
		rowCap, showStatus := panelRows(len(rs), contentH)
		m.syncTable(src) // size the table + rebuild rows (cursor marker, checkboxes)
		t := m.tableFor(src)

		// table.View() is a (blank) header line + its viewport; drop the header and
		// keep exactly rowCap content lines, padding short lists as the manual
		// renderer did so the border wraps to an exact size.
		tv := strings.Split(t.View(), "\n")
		var body []string
		if len(tv) > 1 {
			body = tv[1:]
		}
		for i := range body {
			body[i] = padRight(body[i], contentW)
		}
		for len(body) < rowCap {
			body = append(body, padRight("", contentW))
		}
		lines = append(lines, body[:rowCap]...)

		if showStatus {
			// The table's viewport hides its scroll offset, so approximate the
			// hidden-above / hidden-below counts from the cursor and capacity.
			cur := t.Cursor()
			maxOff := max(len(rs)-rowCap, 0)
			above := min(max(cur-(rowCap-1), 0), maxOff)
			below := max(len(rs)-rowCap-above, 0)
			status := fmt.Sprintf("↑ %d   ↓ %d", above, below)
			lines = append(lines, padRight(dimStyle.Render(status), contentW))
		}
	}

	// Add the left gutter back to every content line so each is exactly innerW.
	for i := range lines {
		lines[i] = indent(lines[i])
	}

	// While checking, draw a gradient border whose phase rotates each tick;
	// otherwise a solid border in the backend's own color, brightening to the
	// accent when focused.
	if checking {
		return gradientBox(lines, innerW, innerH, float64(m.tick)*0.03)
	}
	return solidBox(lines, focused, m.sourceColor(src))
}

// solidBox wraps content lines in a border that keeps the backend's own color in
// every state, using two cues to mark the focused panel: the focused panel keeps
// its color at full strength and gains a heavier thick border, while unfocused
// panels dim their color (blended toward the background) and keep the lighter
// rounded border — so the selected panel reads clearly without losing its hue.
// A backend that declares no color falls back to the accent (focused) or the dim
// border (unfocused).
func solidBox(content []string, focused bool, color string) string {
	style := lipgloss.NewStyle()
	switch {
	case focused && color != "":
		style = style.Border(lipgloss.ThickBorder()).BorderForeground(lipgloss.Color(color))
	case focused:
		style = style.Border(lipgloss.ThickBorder()).BorderForeground(lipgloss.Color(colAccent))
	case color != "":
		style = style.Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(dimColor(color)))
	default:
		style = style.Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(dimBorder))
	}
	return style.Render(strings.Join(content, "\n"))
}

// dimColor mutes a hex color for an unfocused panel's border: it mostly drops
// saturation (so the color still reads as itself, just quieter) and only lightly
// darkens, keeping the hue identifiable rather than blending it into a muddy
// near-background shade. The focused panel — full color + thick border — stays
// the clear standout. A color that won't parse is returned unchanged.
func dimColor(hex string) string {
	c, err := colorful.Hex(hex)
	if err != nil {
		return hex
	}
	h, s, l := c.Hsl()
	return colorful.Hsl(h, s*0.5, l*0.85).Clamped().Hex()
}

// renderColumns draws the column layout: each column is a vertical stack of
// panels, and the columns are joined side by side (top-aligned) with a blank gap
// column between them. A shorter column is padded below by the horizontal join,
// so the app canvas shows through beneath it.
func (m Model) renderColumns(cols []column, render func(box panelBox) string) string {
	gapCol := strings.Repeat(" ", colGap)
	parts := make([]string, 0, len(cols)*2-1)
	for ci, col := range cols {
		if ci > 0 {
			parts = append(parts, gapCol)
		}
		boxes := make([]string, len(col.boxes))
		for j, b := range col.boxes {
			boxes[j] = render(b)
		}
		parts = append(parts, lipgloss.JoinVertical(lipgloss.Left, boxes...))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// gradientBox wraps content (exactly innerH lines, each innerW wide) in a
// rounded border whose color sweeps the palette around the perimeter. phase
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
	// Pad nv to the column width too: an unpadded new-version shifts the size
	// column and (pin) badge left on rows with shorter versions.
	nv := padRight(truncate(displayVersion(r.update.NewVersion), newW), newW)

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
	// Cap version columns: flatpak versions can carry a " (commit)" suffix, but
	// the name is what identifies the package, so we keep these bounded and let
	// the shrink loop below trim them before the name. The data-driven width
	// means short versions still stay compact.
	curW = min(curW, 18)
	newW = min(newW, 18)
	sizeW = min(sizeW, 9)

	sizeCol := func() int {
		if sizeW > 0 {
			return 2 + sizeW // "  142 MB"
		}
		return 0
	}
	avail := max(innerW-overhead-pinSlack, 6)

	// Shrink to fit, dropping the least-missed information first: the size
	// column, then the version columns down to a usable floor, and only then
	// the name. This keeps package names readable on narrow panels.
	for nameW+curW+newW+sizeCol() > avail {
		switch {
		case sizeW > 0:
			sizeW = 0
		case curW > 6:
			curW--
		case newW > 6:
			newW--
		case nameW > 12:
			nameW--
		case curW > 3:
			curW--
		case newW > 3:
			newW--
		case nameW > 4:
			nameW--
		default:
			return nameW, curW, newW, sizeW // can't shrink further
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

// viewConfirmInstall floats the single-package install confirmation (the (i)
// flow) over the Selecting grid, reusing the review-modal layering.
func (m Model) viewConfirmInstall() string {
	backdrop := m.viewSelecting()
	modal := m.installModal()

	x := max((m.width-lipgloss.Width(modal))/2, 0)
	y := max((lipgloss.Height(backdrop)-lipgloss.Height(modal))/2, 0)

	bg := lipgloss.NewLayer(backdrop)
	fg := lipgloss.NewLayer(modal).X(x).Y(y).Z(1)
	return lipgloss.NewCompositor(bg, fg).Render()
}

// installModal is the floating confirmation box for installing just the hovered
// package: backend, name, version transition, and download size.
func (m Model) installModal() string {
	body := []string{titleStyle.Render("Install this package?") + m.dryRunBadge(), ""}
	if m.installTarget == nil {
		body = append(body, dimStyle.Render("Nothing selected."))
	} else {
		t := m.installTarget
		u := t.update
		line := fmt.Sprintf("%s  %s",
			padRight(m.sourceLabel(t.source), 12),
			u.Name)
		body = append(body, line)
		ver := fmt.Sprintf("%s → %s", u.CurrentVersion, u.NewVersion)
		if u.SizeBytes > 0 {
			ver += dimStyle.Render(sep + formatBytes(u.SizeBytes) + " to download")
		}
		body = append(body, dimStyle.Render(ver))
	}
	body = append(body, m.planLines()...)
	body = append(body, "", helpRow(m.width, m.keys.confirmInstallHelp()))

	content := lipgloss.JoinVertical(lipgloss.Left, body...)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colAccent)).
		Padding(1, 3).
		Render(content)
}

// planLines renders the resolved-Plan section shared by both confirm modals: a
// spinner while plans resolve, then any backend Notes (e.g. brew's pulled-in
// dependents). Returns nil when there's nothing to show, so callers can append
// it unconditionally. The leading blank keeps it spaced from the summary above.
func (m Model) planLines() []string {
	if m.planning {
		return []string{"", dimStyle.Render(m.spinner.View() + " resolving dependencies…")}
	}
	var notes []string
	for _, s := range m.panelSources() {
		if p, ok := m.plans[s]; ok {
			notes = append(notes, p.Notes...)
		}
	}
	if len(notes) == 0 {
		return nil
	}
	out := []string{""}
	for _, n := range notes {
		out = append(out, dimStyle.Render(n))
	}
	return out
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
			padRight(m.sourceLabel(s), 12),
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
			summary += dimStyle.Render(sep + formatBytes(totalBytes) + " to download")
		}
		body = append(body, "", summary)
	}
	body = append(body, m.planLines()...)
	body = append(body, "", helpRow(m.width, m.keys.reviewingHelp()))

	content := lipgloss.JoinVertical(lipgloss.Left, body...)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colAccent)).
		Padding(1, 3).
		Render(content)
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
		return dimStyle.Render("Nothing to apply.") + "\n\n" + helpRow(m.width, []key.Binding{m.keys.QuitDone})
	}

	// Each apply panel is sized to the number of packages it's applying, plus
	// room for the full (wrapped) error text when that backend has failed — a
	// failed backend is forced full-width (applyLayout) so the reason isn't buried.
	grid := m.renderColumns(m.applyLayout(), func(box panelBox) string {
		return m.renderApplyPanel(box.src, box.w, box.h, box.index)
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
		status = dimStyle.Render(fmt.Sprintf("%s applying%s%s", m.spinner.View(), sep,
			fmt.Sprintf("%d of %d package managers finished", done, total)))
	}
	status += m.dryRunBadge()
	b.WriteString(status + "\n")

	if m.state == stateDone {
		b.WriteString(helpRow(m.width, m.keys.doneHelp()))
	} else {
		b.WriteString(helpRow(m.width, m.keys.applyingHelp()))
	}
	return b.String()
}

// appliedSources is the set of backends in this apply run — those with a
// selection — in stable display order.
func (m Model) appliedSources() []string {
	sel := m.applying
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
func (m Model) renderApplyPanel(src string, totalW, totalH, index int) string {
	innerW := max(totalW-2, 8)
	innerH := max(totalH-2, 1)
	contentW := max(innerW-panelGutter, 4)
	st := m.progress[src]
	pkgs := m.applying[src]
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

	lines := make([]string, 0, innerH)
	lines = append(lines, panelHeader(index, src, right, contentW, m.sourceIcon(src), m.sourceColor(src)))
	contentH := max(innerH-1, 1)

	body := make([]string, 0, contentH)

	// Failed apply: show the package list, then the full word-wrapped error
	// beneath it. The panel was sized (viewApplying) to fit the wrapped text, so
	// the whole reason is legible instead of clipped to a single bottom line.
	if st != nil && st.failed && st.errText != "" {
		barW := applyBarWidth(contentW)
		nameW, curW, newW, sizeW := m.applyColWidthsFor(src)
		for i := 0; i < total && len(body) < contentH; i++ {
			body = append(body, padRight(m.renderApplyRow(i, pkgs[i], st, nameW, curW, newW, sizeW, barW, contentW), contentW))
		}
		if len(body) < contentH {
			body = append(body, padRight("", contentW)) // spacer above the error
		}
		for _, l := range wrapLines("✗ "+st.errText, contentW) {
			if len(body) >= contentH {
				break
			}
			body = append(body, padRight(errStyle.Render(l), contentW))
		}
		for len(body) < contentH {
			body = append(body, padRight("", contentW))
		}
		lines = append(lines, body[:contentH]...)
		for i := range lines {
			lines[i] = indent(lines[i])
		}
		return solidBoxColor(lines, colErr)
	}

	// Reserve the last line for the overall progress bar (or an error message),
	// plus a blank spacer above it so the bar doesn't crowd the table.
	barH := 0
	if contentH >= 2 {
		barH = 1
	}
	spacerH := 0
	if contentH >= 3 {
		spacerH = 1
	}
	listH := contentH - barH - spacerH

	// The package list, scrolled so the active item stays in view.
	// Reserve a per-row bar column, dropping it on narrow panels so the columns
	// keep today's behaviour when there's no room. The column widths are sized
	// once across every backend's packages so they line up down the whole stack.
	barW := applyBarWidth(contentW)
	nameW, curW, newW, sizeW := m.applyColWidthsFor(src)
	offset := 0
	if total > listH {
		offset = clampInt(activeRow(pkgs, st)-listH/2, 0, total-listH)
	}
	end := min(offset+listH, total)
	for i := offset; i < end; i++ {
		body = append(body, padRight(m.renderApplyRow(i, pkgs[i], st, nameW, curW, newW, sizeW, barW, contentW), contentW))
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
			body = append(body, padRight(dimStyle.Render(truncate(stripCR(l), contentW)), contentW))
		}
		for len(body) < listH {
			body = append(body, padRight("", contentW))
		}
	}

	if spacerH == 1 {
		body = append(body, padRight("", contentW))
	}
	if barH == 1 {
		var totalBytes int64
		for _, u := range pkgs {
			totalBytes += u.SizeBytes
		}
		frac := applyOverallFraction(pkgs, st)
		body = append(body, padRight(m.applyBottomLine(st, done, frac, totalBytes, contentW), contentW))
	}
	for len(body) < contentH {
		body = append(body, padRight("", contentW))
	}
	lines = append(lines, body[:contentH]...)

	// Add the left gutter back to every content line so each is exactly innerW.
	for i := range lines {
		lines[i] = indent(lines[i])
	}

	// Border color reflects state; animate the gradient while still working.
	switch {
	case st != nil && st.failed:
		return solidBoxColor(lines, colErr)
	case st != nil && st.finished:
		return solidBoxColor(lines, colOk)
	case m.state == stateDone:
		return solidBoxColor(lines, colHelp)
	default:
		return gradientBox(lines, innerW, innerH, float64(m.tick)*0.03)
	}
}

// renderApplyRow draws one package line in an apply panel: a status icon, the
// name, its version bump, and its download size. The active row carries a spinner
// and a live note (phase + downloaded/size + percent) so you can watch the work
// move down the list. The whole row is bounded to innerW so the note can't spill
// past the border.
func (m Model) renderApplyRow(i int, u core.Update, st *srcState, nameW, curW, newW, sizeW, barW, innerW int) string {
	status := pkgRowStatus(i, u.Name, st)
	// Before any package goes active, the transaction may already be working
	// (dnf5's silent depsolve/download); show that status on the row next in line
	// so even a one-row panel shows life instead of a bare ○ for tens of seconds.
	prepActive := st != nil && !st.finished && !st.failed && st.status != "" &&
		st.item == "" && i == st.done

	var icon string
	switch {
	case status == statDone:
		icon = okStyle.Render("✓")
	case status == statActive || prepActive:
		icon = cursorStyle.Render(m.spinner.View())
	case status == statFailed:
		icon = errStyle.Render("✗")
	default:
		icon = dimStyle.Render("○")
	}

	name := padRight(truncate(u.Name, nameW), nameW)
	cur := padRight(truncate(displayVersion(u.CurrentVersion), curW), curW)
	// Pad nv so the size column and per-row bar don't shift on shorter versions.
	nv := padRight(truncate(displayVersion(u.NewVersion), newW), newW)
	line := fmt.Sprintf("%s %s  %s%s%s",
		icon, name, dimStyle.Render(cur), dimStyle.Render(" → "), nv)
	if sizeW > 0 {
		line += "  " + dimStyle.Render(padLeft(truncate(formatBytes(u.SizeBytes), sizeW), sizeW))
	}

	// A per-row download bar that fills as the package downloads and stays full
	// once it's done, so the list reads as a wall of progress filling top-to-bottom.
	if barW > 0 {
		line += "  " + rowProgressBar(rowFraction(status, u.Name, st), barW, status)
	}

	// Annotate the in-flight package with what it's doing right now, truncated to
	// the width left in the panel so the styled note never overflows the border.
	note := ""
	switch {
	case status == statActive && st != nil:
		note = applyActiveNote(u, st)
	case prepActive:
		note = st.status
	}
	if note != "" {
		// "  · " is 4 visible cols of overhead before the (truncatable) note.
		if avail := innerW - lipgloss.Width(line) - 4; avail > 0 {
			line += "  " + dimStyle.Render("· "+truncate(note, avail))
		}
	}
	return line
}

// applyActiveNote describes what the active package is doing: its phase, and —
// when the backend reports numeric progress and the size is known — the live
// downloaded/size and percent (downloaded ≈ fraction × size). With no numeric
// progress (brew/flatpak report phase only) it falls back to just the phase, so
// we never show a misleading 0%.
func applyActiveNote(u core.Update, st *srcState) string {
	note := st.phase
	frac := clamp01(stFraction(st))
	if frac <= 0 {
		return note
	}
	pct := fmt.Sprintf("%d%%", int(frac*100))
	if u.SizeBytes > 0 {
		downloaded := int64(frac * float64(u.SizeBytes))
		return strings.TrimSpace(fmt.Sprintf("%s %s/%s %s",
			note, formatBytes(downloaded), formatBytes(u.SizeBytes), pct))
	}
	return strings.TrimSpace(note + " " + pct)
}

// applyBottomLine is the panel's summary footer: an error when failed, a done
// note (with elapsed time) when finished, otherwise an overall progress bar with
// a downloaded/total · rate · ETA cluster on the right when sizes are known.
func (m Model) applyBottomLine(st *srcState, done int, frac float64, totalBytes int64, w int) string {
	switch {
	case st != nil && st.failed:
		return errStyle.Render(truncate("✗ "+st.errText, max(w, 1)))
	case st != nil && st.finished:
		note := fmt.Sprintf("✓ done (%d upgraded)", done)
		if d := st.elapsed(); d > 0 {
			note += sepTight + formatDuration(d)
		}
		return okStyle.Render(note)
	default:
		// Silent prep phase: the backend has started but emitted no progress yet
		// (dnf5 spends seconds on metadata/depsolve before its first signal). Show
		// that it's working rather than a frozen 0% bar — the header timer is live.
		if st != nil && !st.finished && st.item == "" && done == 0 && frac == 0 {
			label := st.status
			if label == "" {
				label = "preparing…"
			}
			return dimStyle.Render(m.spinner.View() + " " + label)
		}
		frac = clamp01(frac)
		right := rateETA(st, frac, totalBytes)
		// In a narrow panel there's no room for both; keep the full-width bar and
		// drop the cluster (the header still carries the elapsed timer).
		const minBar = 8
		if right != "" && lipgloss.Width(right)+2+minBar > w {
			right = ""
		}
		barW := w
		if right != "" {
			barW = max(w-lipgloss.Width(right)-2, minBar)
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
	return strings.Join(parts, sepTight)
}

// applyBarWidth is the per-row progress-bar column width for an apply panel of
// the given content width; 0 on panels too narrow to spare the room. "+2" (used
// by callers) is the leading gap before the bar.
func applyBarWidth(contentW int) int {
	if contentW >= 48 {
		return clampInt(contentW/5, 10, 18)
	}
	return 0
}

// applyColWidthsFor sizes src's apply-panel name / current / new / size columns.
// A full-width (wide) panel sizes across every backend's packages so the columns
// line up down the stack; a tiled (narrow) panel — its own separate box — sizes
// from just its own packages at its own column width.
func (m Model) applyColWidthsFor(src string) (nameW, curW, newW, sizeW int) {
	contentW := m.panelContentWFor(src)
	colW := contentW
	if barW := applyBarWidth(contentW); barW > 0 {
		colW = contentW - barW - 2
	}
	sel := m.selectionByBackend()
	var pkgs []core.Update
	if m.panelWidth(src) < m.width {
		pkgs = sel[src]
	} else {
		for _, s := range m.appliedSources() {
			pkgs = append(pkgs, sel[s]...)
		}
	}
	return applyColumns(pkgs, colW)
}

// applyColumns sizes the name / current / new / size columns for an apply panel,
// reserving room for the leading status icon (1 col + space). The size column is
// dropped when there isn't room for it alongside a readable name.
func applyColumns(pkgs []core.Update, innerW int) (nameW, curW, newW, sizeW int) {
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
		if u.SizeBytes > 0 {
			if w := lipgloss.Width(formatBytes(u.SizeBytes)); w > sizeW {
				sizeW = w
			}
		}
	}
	curW = min(curW, 18)
	newW = min(newW, 18)
	sizeW = min(sizeW, 9)

	sizeCol := func() int {
		if sizeW > 0 {
			return 2 + sizeW // "  24 MB"
		}
		return 0
	}
	avail := max(innerW-overhead, 6)

	// Same priority as panelColumns: trim size, then versions, then the name.
	for nameW+curW+newW+sizeCol() > avail {
		switch {
		case sizeW > 0:
			sizeW = 0
		case curW > 6:
			curW--
		case newW > 6:
			newW--
		case nameW > 12:
			nameW--
		case curW > 3:
			curW--
		case newW > 3:
			newW--
		case nameW > 4:
			nameW--
		default:
			return nameW, curW, newW, sizeW
		}
	}
	return nameW, curW, newW, sizeW
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

// progressBar renders a [████░░░░] bar of the given width for fraction f, using
// the bubbles progress component. The fill color reflects state (accent active,
// green finished, red failed) and the empty track stays dim, matching the look
// the hand-rolled bar had.
func progressBar(f float64, w int, st *srcState) string {
	fill := colAccent
	switch {
	case st != nil && st.failed:
		fill = colErr
	case st != nil && st.finished:
		fill = colOk
	}
	return coloredBar(f, w, fill)
}

// rowProgressBar draws one apply row's bar, colored by that row's status —
// dim/idle while pending, accent while downloading, green once done, red on
// failure — so completed rows read as a full green wall as the run progresses.
// The bar is wrapped in dim brackets so the per-row bars read as distinct
// units rather than clumping into one block down the column.
func rowProgressBar(f float64, w int, status pkgStat) string {
	fill := colDim // pending: dim, reads as an empty/idle track
	switch status {
	case statActive:
		fill = colAccent
	case statDone:
		fill = colOk
	case statFailed:
		fill = colErr
	}
	if w < 6 { // no room for brackets + a meaningful bar
		return coloredBar(f, w, fill)
	}
	return dimStyle.Render("[") + coloredBar(f, w-2, fill) + dimStyle.Render("]")
}

// coloredBar renders a [████░░░░] bar of the given width and fill color using
// the bubbles progress component; the empty track stays dim, matching dimStyle.
func coloredBar(f float64, w int, fill string) string {
	if w < 4 {
		return strings.Repeat(" ", max(w, 0))
	}
	bar := progress.New(
		progress.WithoutPercentage(),
		progress.WithFillCharacters('█', '░'),
		progress.WithWidth(w),
	)
	bar.FullColor = lipgloss.Color(fill)
	bar.EmptyColor = lipgloss.Color(colDim)
	return bar.ViewAs(clamp01(f))
}

// rowFraction derives an apply row's bar fill from its status: a finished/seen
// package reads full and stays full, the active one shows its persisted download
// progress, and pending rows read empty. Backends with no per-package data report
// 0 here, so their rows simply fill on completion.
func rowFraction(status pkgStat, name string, st *srcState) float64 {
	switch status {
	case statDone:
		return 1.0
	case statActive, statFailed:
		if st != nil {
			return clamp01(st.pkgFrac[name])
		}
	}
	return 0
}

// applyOverallFraction is the panel's overall progress: the mean of the
// per-row fractions across every package. Because each row's fraction is
// monotonic (a done/seen row stays at 1.0 and the active row reports its
// persisted max via pkgFrac), the overall bar only ever advances — unlike a
// naive (done + current-item fraction)/total, which snaps backwards when the
// active item's fraction resets on each new package (e.g. PackageKit, which
// never emits a per-item EventItemDone).
func applyOverallFraction(pkgs []core.Update, st *srcState) float64 {
	if len(pkgs) == 0 {
		return 0
	}
	var sum float64
	for i, u := range pkgs {
		sum += rowFraction(pkgRowStatus(i, u.Name, st), u.Name, st)
	}
	return sum / float64(len(pkgs))
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

// solidBoxColor wraps content lines in a rounded border of the given color.
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

// wrapLines word-wraps s to width w and returns the resulting lines. Used so an
// apply panel can show a backend's full error instead of truncating it to one
// line; sizing and rendering share this so their line counts agree.
func wrapLines(s string, w int) []string {
	if w < 1 {
		w = 1
	}
	wrapped := lipgloss.NewStyle().Width(w).Render(s)
	return strings.Split(wrapped, "\n")
}
