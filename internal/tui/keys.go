package tui

import "charm.land/bubbles/v2/key"

// keyMap is the single source of truth for keybindings: the Update handlers match
// against these, and the footer help (help.Model) is rendered from the same
// bindings, so the two can't drift apart. Bindings that aren't shown in any footer
// carry no WithHelp; they exist only for matching.
type keyMap struct {
	Up       key.Binding
	Down     key.Binding
	PageUp   key.Binding
	PageDown key.Binding
	Home     key.Binding
	End      key.Binding
	Left     key.Binding
	Right    key.Binding
	Tab      key.Binding
	ShiftTab key.Binding
	Toggle   key.Binding
	All      key.Binding
	None     key.Binding
	DryRun   key.Binding
	Install  key.Binding
	Review   key.Binding
	Apply    key.Binding
	Back     key.Binding
	Quit     key.Binding
	Cancel   key.Binding
	QuitDone key.Binding
	More     key.Binding
}

// defaultKeys mirrors the bindings (and footer wording) the TUI used when these
// were hand-written switch statements and literal help strings.
func defaultKeys() keyMap {
	return keyMap{
		// Up carries the "move" label for the footer; Down is match-only.
		Up:   key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/↓", "move")),
		Down: key.NewBinding(key.WithKeys("down", "j")),
		// Page/Home/End move within the focused panel; match-only (no footer help).
		PageUp:   key.NewBinding(key.WithKeys("pgup", "ctrl+u")),
		PageDown: key.NewBinding(key.WithKeys("pgdown", "ctrl+d")),
		Home:     key.NewBinding(key.WithKeys("home", "g")),
		End:      key.NewBinding(key.WithKeys("end", "G")),
		Left:     key.NewBinding(key.WithKeys("left", "h")),
		// Right carries the "panel" label for the footer; Left/Tab/ShiftTab match-only.
		Right:    key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("←/→/tab", "panel")),
		Tab:      key.NewBinding(key.WithKeys("tab")),
		ShiftTab: key.NewBinding(key.WithKeys("shift+tab")),
		Toggle:   key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "toggle")),
		All:      key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "all")),
		None:     key.NewBinding(key.WithKeys("N"), key.WithHelp("N", "none")),
		DryRun:   key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "dry-run")),
		Install:  key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "install one")),
		Review:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "review")),
		Apply:    key.NewBinding(key.WithKeys("y", "enter"), key.WithHelp("enter/y", "apply")),
		Back:     key.NewBinding(key.WithKeys("esc", "b", "n"), key.WithHelp("esc", "cancel")),
		Quit:     key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		Cancel:   key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "cancel")),
		QuitDone: key.NewBinding(key.WithKeys("q", "esc"), key.WithHelp("q", "quit")),
		More:     key.NewBinding(key.WithKeys("enter", "r"), key.WithHelp("enter", "more updates")),
	}
}

// Footer help, one slice per state — only the bindings that should appear, in
// display order. help.Model renders each as "<key> <desc>" joined by " · ".

// selectingHelp omits None (the N key still works) to keep the footer within a
// 100-col terminal once Install is advertised.
func (k keyMap) selectingHelp() []key.Binding {
	return []key.Binding{k.Up, k.Right, k.Toggle, k.All, k.DryRun, k.Install, k.Review, k.Quit}
}

func (k keyMap) reviewingHelp() []key.Binding {
	return []key.Binding{k.Apply, k.DryRun, k.Back}
}

func (k keyMap) confirmInstallHelp() []key.Binding {
	return []key.Binding{k.Apply, k.DryRun, k.Back}
}

func (k keyMap) applyingHelp() []key.Binding {
	return []key.Binding{k.Cancel}
}

func (k keyMap) doneHelp() []key.Binding {
	return []key.Binding{k.More, k.QuitDone}
}
