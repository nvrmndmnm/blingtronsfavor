package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/provider"
	"github.com/itp5b5/blessforge/internal/resolver"
	"github.com/itp5b5/blessforge/internal/state"
)

// fakeProvider is a provider.Provider stand-in: Check is unused here, Fetch
// unpacks one addon folder into dest the way the real providers do.
type fakeProvider struct {
	changelog string
}

func (f *fakeProvider) Check(context.Context, state.SourceRef) (provider.Upstream, error) {
	return provider.Upstream{Version: "2.0.0", Changelog: f.changelog, Strategy: state.StrategyDirect}, nil
}

func (f *fakeProvider) Fetch(_ context.Context, _ state.SourceRef, dest string) error {
	dir := filepath.Join(dest, "WagoAddOn")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "WagoAddOn.toc"), []byte("## Interface: 16001\n"), 0o644)
}

// TestInstallUpdateProvider drives the provider install path: Fetch unpacks
// into the staging directory and the state entry keeps its identity while
// gaining the new version, ETag and the provider-reported changelog.
func TestInstallUpdateProvider(t *testing.T) {
	addon := &state.AddonState{
		ID:               "WagoAddOn",
		DisplayName:      "Wago Addon",
		Source:           state.SourceRef{Kind: state.SourceWago, Slug: "VBNBxKx5"},
		Strategy:         state.StrategyDirect,
		Tracking:         "stable",
		InstalledVersion: "1.9.0",
		ETag:             "1.9.0",
	}
	testState(t, addon)

	prov := &fakeProvider{changelog: "## 2.0.0\n\n- fixed a glitch"}
	task := syncTask{flavor: client.FlavorForever, iface: 16001, addon: addon}
	outcome := resolveResult{
		prov: prov,
		plan: resolver.Plan{Strategy: state.StrategyDirect, Version: "2.0.0", Reason: "resolved by the wago provider"},
		upstream: provider.Upstream{
			Version:   "2.0.0",
			Changelog: "## 2.0.0\n\n- fixed a glitch",
			Strategy:  state.StrategyDirect,
		},
	}
	if err := installUpdate(context.Background(), nil, task, outcome); err != nil {
		t.Fatalf("installUpdate: %v", err)
	}

	st, err := LoadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	got := st.Client(client.FlavorForever).Addons["WagoAddOn"]
	if got == nil {
		t.Fatal("state lost the addon entry")
	}
	if got.InstalledVersion != "2.0.0" || got.ETag != "2.0.0" {
		t.Errorf("version/etag = %q/%q, want 2.0.0/2.0.0", got.InstalledVersion, got.ETag)
	}
	if got.Changelog != "## 2.0.0\n\n- fixed a glitch" {
		t.Errorf("changelog = %q, want the provider-reported changelog", got.Changelog)
	}
	if got.Source.Slug != "VBNBxKx5" || got.Strategy != state.StrategyDirect {
		t.Errorf("source/strategy = %+v %q, want the wago source kept", got.Source, got.Strategy)
	}
	root := filepath.Join(st.Client(client.FlavorForever).Root, "Interface", "AddOns", "WagoAddOn", "WagoAddOn.toc")
	if _, err := os.Stat(root); err != nil {
		t.Errorf("addon not installed at %s: %v", root, err)
	}
}

// TestResolveAllFailSoft pins the routing contract: per-task problems are
// results, never run failures — manual entries skip with their reason and a
// source kind no provider handles fails that task alone.
func TestResolveAllFailSoft(t *testing.T) {
	manual := &state.AddonState{
		ID: "ManualAddOn", Strategy: state.StrategyManual,
		Source: state.SourceRef{Kind: state.SourceManual, Slug: "ManualAddOn"},
	}
	orphan := &state.AddonState{
		ID:     "CurseAddOn",
		Source: state.SourceRef{Kind: state.SourceKind("curse"), Slug: "8315"},
	}
	results := resolveAll(context.Background(), nil, []syncTask{
		{flavor: client.FlavorForever, addon: manual},
		{flavor: client.FlavorForever, addon: orphan},
	}, 2, state.New())
	if results[0].skip == "" || results[0].err != nil {
		t.Errorf("manual entry = %+v, want a skip", results[0])
	}
	if results[1].err == nil || !strings.Contains(results[1].err.Error(), "curse") {
		t.Errorf("orphan = %+v, want a per-addon unsupported-kind error", results[1])
	}
}
