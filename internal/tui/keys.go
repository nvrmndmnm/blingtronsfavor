package tui

import (
	"charm.land/bubbles/v2/key"
)

// keyMap is the TUI key map, exactly the bindings the UX contract fixes:
//
//	u update all · a add · U update one · c check · r refresh ·
//	s toggle release/branch · d delete (confirm) ·
//	enter detail pane · / filter · q quit
//
// It also satisfies bubbles' help.KeyMap so the bottom bar can render it.
type keyMap struct {
	Up        key.Binding
	Down      key.Binding
	PgUp      key.Binding
	PgDn      key.Binding
	Detail    key.Binding
	Filter    key.Binding
	Update    key.Binding
	Add       key.Binding
	UpdateOne key.Binding
	Check     key.Binding
	Refresh   key.Binding
	Track     key.Binding
	Delete    key.Binding
	Confirm   key.Binding
	Cancel    key.Binding
	Quit      key.Binding
}

func newKeyMap(g glyphs) keyMap {
	return keyMap{
		Up:        key.NewBinding(key.WithKeys("up", "k"), key.WithHelp(g.Up+"/k", "up")),
		Down:      key.NewBinding(key.WithKeys("down", "j"), key.WithHelp(g.Down+"/j", "down")),
		PgUp:      key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up")),
		PgDn:      key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdown", "page down")),
		Detail:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "detail")),
		Filter:    key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Update:    key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "update all")),
		Add:       key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")),
		UpdateOne: key.NewBinding(key.WithKeys("U"), key.WithHelp("U", "update one")),
		Check:     key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "check")),
		Refresh:   key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Track:     key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "release/branch")),
		Delete:    key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete")),
		Confirm:   key.NewBinding(key.WithKeys("y", "enter"), key.WithHelp("y", "confirm")),
		Cancel:    key.NewBinding(key.WithKeys("n", "esc"), key.WithHelp("esc", "cancel")),
		Quit:      key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

// ShortHelp renders the compact bottom help bar.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Update, k.Add, k.UpdateOne, k.Check, k.Refresh, k.Track, k.Delete, k.Detail, k.Filter, k.Quit}
}

// FullHelp renders the two-row help page: navigation plus the detail
// pane's scroll keys.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.PgUp, k.PgDn, k.Detail, k.Filter},
		{k.Update, k.Add, k.UpdateOne, k.Check, k.Refresh, k.Track, k.Delete, k.Quit},
	}
}
