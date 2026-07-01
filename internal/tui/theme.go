package tui

import (
	"charm.land/lipgloss/v2"
	colorful "github.com/lucasb-eyer/go-colorful"
)

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

// palette is every colour the UI uses, in one place. These are base-16 ANSI
// codes (0–15) rather than xterm-256 shades, so the UI rides the user's own
// terminal theme — the colours they've already customised — instead of pinning
// specific hues. The one deliberate exception is the animated "checking" border,
// which lives in gradPalette as hex because colorful.Hex requires it.
const (
	colAccent = "13" // bright magenta — the single primary accent: titles, panel
	//                    headers, the ▶ cursor, focused borders, active progress.
	colDim  = "8"  // bright black (grey) — secondary text and the empty progress track.
	colHelp = "8"  // bright black (grey) — the help footer and the done-state border.
	colOk   = "10" // bright green — selected, done, finished.
	colErr  = "9"  // bright red — errors and failed state.
	colPin  = "11" // bright yellow — the (pin) badge and the DRY RUN tag.
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colAccent))
	groupStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colAccent))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colDim))
	pinStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colPin))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colOk))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colErr))
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colAccent)).Bold(true)
	helpStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colHelp))
	// helpKeyStyle styles the footer keycaps: bold accent so the actionable keys
	// pop, while the action labels (dimStyle) and separators (helpStyle) recede.
	helpKeyStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colAccent))
)
