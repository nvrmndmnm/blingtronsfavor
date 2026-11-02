package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/bubbletea/v2"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/paths"
	"github.com/itp5b5/blessforge/internal/service"
	"github.com/itp5b5/blessforge/internal/state"
)

// Screen-level acceptance tests for the multi-client flavor switch (P2-1):
// tab / [ ] cycle the configured clients, the table reloads for the active
// one and the header names it, single-client configurations see no new
// keys, and a running sync refuses the switch (sync is per-client).

// testModelFlavors builds a model with one client per given flavor, each
// carrying one addon named "<flavor>Addon", for the flavor-switch tests.
func testModelFlavors(t *testing.T, flavors ...client.Flavor) Model {
	t.Helper()
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	if err := paths.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	path, err := paths.StateFile()
	if err != nil {
		t.Fatal(err)
	}
	st := state.New()
	for _, flavor := range flavors {
		id := string(flavor) + "Addon"
		st.Clients[flavor] = &state.ClientState{
			Flavor: flavor,
			Root:   filepath.Join(t.TempDir(), "_client_"),
			Addons: map[string]*state.AddonState{
				id: {ID: id, Source: state.SourceRef{Kind: state.SourceGitHub, Slug: "owner/" + id}, Tracking: "stable", InstalledVersion: "1.0.0"},
			},
		}
	}
	if err := state.Open(path).Save(st); err != nil {
		t.Fatal(err)
	}
	return NewModel(context.Background(), Options{}, &runtime{})
}

// tabKey builds the tab press.
func tabKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyTab}
}

func TestClientSwitchCyclesConfiguredClients(t *testing.T) {
	m := testModelFlavors(t, client.FlavorClassic, client.FlavorForever)
	if m.client.Flavor != string(client.FlavorClassic) {
		t.Fatalf("active flavor = %q, want classic (the first recorded client)", m.client.Flavor)
	}

	// A stale outcome from the shown client must not follow the table to
	// the next client ("reloads the table for that client").
	m.results["foreverAddon"] = service.Result{AddonID: "foreverAddon", Kind: service.ResultFailed, Err: errors.New("boom")}

	m = press(t, m, tabKey())
	if m.client.Flavor != string(client.FlavorForever) {
		t.Fatalf("tab: active flavor = %q, want forever", m.client.Flavor)
	}
	if len(m.rows) != 1 || m.rows[0].ID != "foreverAddon" {
		t.Fatalf("tab: rows = %+v, want the forever client's addon", m.rows)
	}
	if got := rowText(t, m, "foreverAddon"); strings.HasPrefix(got, "failed") {
		t.Fatalf("the new client's row shows the old client's outcome %q", got)
	}
	if !strings.Contains(m.headerBar(80), "Forever") {
		t.Fatalf("header does not name the active flavor:\n%s", m.headerBar(80))
	}

	// tab wraps around the list, [ steps back, ] steps forward.
	m = press(t, m, tabKey())
	if m.client.Flavor != string(client.FlavorClassic) {
		t.Fatalf("tab wrap: active flavor = %q, want classic", m.client.Flavor)
	}
	m = press(t, m, keyMsg('['))
	if m.client.Flavor != string(client.FlavorForever) {
		t.Fatalf("[: active flavor = %q, want forever", m.client.Flavor)
	}
	m = press(t, m, keyMsg(']'))
	if m.client.Flavor != string(client.FlavorClassic) {
		t.Fatalf("]: active flavor = %q, want classic", m.client.Flavor)
	}
}

func TestSingleClientIgnoresSwitchKeys(t *testing.T) {
	m := testModel(t)
	if m.keys.Client.Enabled() {
		t.Fatal("the switch keys are armed with a single client")
	}
	before := m.client.Flavor
	statusBefore := m.statusLine
	m = press(t, m, tabKey())
	m = press(t, m, keyMsg('['))
	m = press(t, m, keyMsg(']'))
	if m.client.Flavor != before {
		t.Fatalf("switch keys moved the active flavor to %q with a single client", m.client.Flavor)
	}
	if m.statusLine != statusBefore {
		t.Fatalf("switch keys changed the status line to %q with a single client", m.statusLine)
	}
}

// TestActiveFlavorSurvivesSnapshotReload pins the switch's consistency
// across a snapshot reload: the flavor the user switched to keeps its
// table rows and its header badge when a finished sync or refresh lands a
// fresh status report.
func TestActiveFlavorSurvivesSnapshotReload(t *testing.T) {
	m := testModelFlavors(t, client.FlavorClassic, client.FlavorForever)
	m = press(t, m, tabKey())
	if m.client.Flavor != string(client.FlavorForever) {
		t.Fatalf("tab: active flavor = %q, want forever", m.client.Flavor)
	}
	// A fresh snapshot lands — the loadStatus command every finished run
	// and refresh returns.
	next, _ := m.Update(loadStatus()())
	m = next.(Model)
	if m.client.Flavor != string(client.FlavorForever) {
		t.Fatalf("snapshot reload moved the active flavor to %q", m.client.Flavor)
	}
	if len(m.rows) != 1 || m.rows[0].ID != "foreverAddon" {
		t.Fatalf("rows after reload = %+v, want the forever client's addon", m.rows)
	}
	if !strings.Contains(m.headerBar(80), "Forever") {
		t.Fatalf("header after reload does not name the active flavor:\n%s", m.headerBar(80))
	}
}

func TestClientSwitchRefusedMidSync(t *testing.T) {
	m := testModelFlavors(t, client.FlavorClassic, client.FlavorForever)
	m.syncing = true
	m = press(t, m, tabKey())
	if m.client.Flavor != string(client.FlavorClassic) {
		t.Fatalf("switching mid-sync moved the active flavor to %q", m.client.Flavor)
	}
	if !strings.Contains(m.statusLine, "can't switch client") || !strings.Contains(m.statusLine, "bound to") {
		t.Fatalf("statusLine = %q, want the per-client refusal", m.statusLine)
	}
}

func TestClientSwitchRefusedMidAdd(t *testing.T) {
	m := testModelFlavors(t, client.FlavorClassic, client.FlavorForever)
	m.adding = true
	m = press(t, m, tabKey())
	if m.client.Flavor != string(client.FlavorClassic) {
		t.Fatalf("switching mid-add moved the active flavor to %q", m.client.Flavor)
	}
	if !strings.Contains(m.statusLine, "can't switch client") {
		t.Fatalf("statusLine = %q, want the refusal", m.statusLine)
	}
}

func TestUpdateAllScopesTheRunToTheActiveClient(t *testing.T) {
	for _, tc := range []struct {
		name        string
		switchFirst bool
		want        client.Flavor
	}{
		{"the shown client", false, client.FlavorClassic},
		{"the client switched to", true, client.FlavorForever},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testModelFlavors(t, client.FlavorClassic, client.FlavorForever)
			got := make(chan service.SyncOptions, 1)
			m.syncFn = fakeSync(got)
			if tc.switchFirst {
				m = press(t, m, tabKey())
			}
			m = press(t, m, keyMsg('u'))
			opts := awaitOpts(t, got)
			if opts.Flavor != tc.want {
				t.Fatalf("u scoped the run to flavor %q, want %q", opts.Flavor, tc.want)
			}
			if opts.CheckOnly || opts.Only != nil {
				t.Fatalf("u started %+v, want the update-all run for one client", opts)
			}
		})
	}
}

func TestHelpOverlayListsSwitchKeysOnlyWithSeveralClients(t *testing.T) {
	m := testModelFlavors(t, client.FlavorClassic, client.FlavorForever)
	m.width, m.height = 100, 28
	m.helpOpen = true
	content := m.View().Content
	if !strings.Contains(content, "tab/[ ]") || !strings.Contains(content, "switch client") {
		t.Fatalf("multi-client help overlay is missing the switch keys:\n%s", content)
	}

	single := testModel(t)
	single.width, single.height = 100, 28
	single.helpOpen = true
	if strings.Contains(single.View().Content, "switch client") {
		t.Fatalf("single-client help overlay advertises switch keys that do nothing:\n%s", single.View().Content)
	}
}
