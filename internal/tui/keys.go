package tui

import (
	"charm.land/bubbles/v2/key"
)

// keyMap is the TUI key map, exactly the bindings the UX contract fixes:
//
//	u update all addons · a add · U update selected ·
//	c check (dry run) · r reload from disk ·
//	s toggle release/branch · d delete (confirm) ·
//	enter detail pane · / filter (esc clears) · o sort ·
//	tab / [ ] switch client · ? help · q quit
//
// q and ctrl+c are deliberately split (R2): q quits — asking first while a
// run is in flight, never silently cancelling — while ctrl+c cancels the
// running sync and then offers quit.
//
// The client switch is armed only when several clients are configured
// (P2-1): disabled it matches nothing and bubbles' help drops it, so a
// single-client configuration sees exactly the keys and the help it has
// always seen.
//
// It also satisfies bubbles' help.KeyMap: ShortHelp renders the compact
// bottom bar, FullHelp groups the bindings, and helpSections gives the
// full help overlay (?) its context grouping.
type keyMap struct {
	Up        key.Binding
	Down      key.Binding
	PgUp      key.Binding
	PgDn      key.Binding
	Detail    key.Binding
	Filter    key.Binding
	Sort      key.Binding
	Client    key.Binding
	Update    key.Binding
	Add       key.Binding
	UpdateOne key.Binding
	Check     key.Binding
	Refresh   key.Binding
	Track     key.Binding
	Delete    key.Binding
	Confirm   key.Binding
	Cancel    key.Binding
	Help      key.Binding
	Quit      key.Binding
	Interrupt key.Binding
}

func newKeyMap(g glyphs) keyMap {
	return keyMap{
		Up:        key.NewBinding(key.WithKeys("up", "k"), key.WithHelp(g.Up+"/k", "up")),
		Down:      key.NewBinding(key.WithKeys("down", "j"), key.WithHelp(g.Down+"/j", "down")),
		PgUp:      key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up")),
		PgDn:      key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdown", "page down")),
		Detail:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "detail")),
		Filter:    key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Sort:      key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "sort")),
		Client:    key.NewBinding(key.WithKeys("tab", "[", "]"), key.WithHelp("tab/[ ]", "switch client"), key.WithDisabled()),
		Update:    key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "update all addons")),
		Add:       key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")),
		UpdateOne: key.NewBinding(key.WithKeys("U"), key.WithHelp("U", "update selected")),
		Check:     key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "check (dry run)")),
		Refresh:   key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reload from disk")),
		Track:     key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "toggle release/branch")),
		Delete:    key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete")),
		Confirm:   key.NewBinding(key.WithKeys("y", "enter"), key.WithHelp("y", "confirm")),
		Cancel:    key.NewBinding(key.WithKeys("n", "esc"), key.WithHelp("esc", "cancel")),
		Help:      key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:      key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		Interrupt: key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "cancel & quit")),
	}
}

// ShortHelp renders the compact bottom help bar. The client switch sits
// just before the pinned help and quit keys: when the bar cannot hold
// every entry it drops first — the ? overlay always lists it.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Update, k.Add, k.UpdateOne, k.Check, k.Refresh, k.Track, k.Client, k.Delete, k.Detail, k.Filter, k.Sort, k.Help, k.Quit}
}

// FullHelp groups the bindings for the full help: navigation (with the
// detail pane's scroll keys and the client switch), the actions, and the
// help/quit pair.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.PgUp, k.PgDn, k.Detail, k.Filter, k.Sort, k.Client},
		{k.Update, k.Add, k.UpdateOne, k.Check, k.Refresh, k.Track, k.Delete},
		{k.Help, k.Quit, k.Interrupt},
	}
}

// helpRow is one key/description pair of the full help overlay.
type helpRow struct{ key, desc string }

// helpSection is one context group of the full help overlay: the title
// naming the context and the keys active in it.
type helpSection struct {
	title string
	rows  []helpRow
}

// bindingRow renders one binding as a help row, so the overlay cannot
// drift apart from the bindings in wording.
func bindingRow(kb key.Binding) helpRow {
	h := kb.Help()
	return helpRow{key: h.Key, desc: h.Desc}
}

// actionRow renders one binding with the overlay's full behavior note, so
// the ? help explains what the key actually does (a dry run, a disk
// reload) instead of repeating its terse bottom-bar label.
func actionRow(kb key.Binding, desc string) helpRow {
	return helpRow{key: kb.Help().Key, desc: desc}
}

// helpSections is the full help overlay's content: every keybinding
// grouped by the context it is active in — the list, the two text inputs,
// the target-flavor prompt, the confirmations, and the keys that act on a
// running sync. The action keys carry their concrete behavior here while
// the bindings stay terse for the compact bottom bar; the inputs list
// only their actionable controls (apply/cancel) — narrating typing and
// backspace wastes the space the keys need.
func (k keyMap) helpSections() []helpSection {
	list := []helpRow{
		bindingRow(k.Up), bindingRow(k.Down), bindingRow(k.PgUp), bindingRow(k.PgDn),
		bindingRow(k.Detail), bindingRow(k.Filter), bindingRow(k.Sort),
	}
	// The client switch is listed only where it exists: several clients
	// configured (its binding is disabled otherwise).
	if k.Client.Enabled() {
		list = append(list, bindingRow(k.Client))
	}
	list = append(list,
		bindingRow(k.Update), bindingRow(k.Add),
		actionRow(k.UpdateOne, "update the selected addon only"),
		actionRow(k.Check, "check for updates without modifying disk (dry run)"),
		actionRow(k.Refresh, "reload status from disk, update the checked stamp"),
		actionRow(k.Track, "toggle release/branch tracking"),
		bindingRow(k.Delete),
		bindingRow(k.Help), bindingRow(k.Quit), bindingRow(k.Interrupt),
	)
	return []helpSection{
		{title: "the list", rows: list},
		{title: "filter input", rows: []helpRow{
			{"enter", "apply"},
			{"esc", "clear"},
		}},
		{title: "add input", rows: []helpRow{
			{"paste", "inserts a reference"},
			{"left/right, alt+b/alt+f, home/end, ctrl+a/ctrl+e", "move the cursor"},
			{"ctrl+w", "delete word back"},
			{"ctrl+u", "clear to start"},
			{"enter", "continue"},
			{"esc", "cancel"},
		}},
		{title: "target flavor", rows: []helpRow{
			{"1-9", "pick"},
			{k.Up.Help().Key + " " + k.Down.Help().Key, "pick"},
			{"enter", "add"},
			{"esc", "cancel"},
		}},
		{title: "confirmations", rows: []helpRow{
			{"y/enter", "confirm"},
			{"n/esc", "cancel"},
		}},
		{title: "during a run", rows: []helpRow{
			{"esc", "cancel the add"},
			{"ctrl+c", "cancel the sync"},
			{"q", "ask to quit"},
		}},
	}
}
