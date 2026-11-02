package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nvrmndmnm/blingtronsfavor/internal/client"
	"github.com/nvrmndmnm/blingtronsfavor/internal/paths"
	"github.com/nvrmndmnm/blingtronsfavor/internal/service"
	"github.com/nvrmndmnm/blingtronsfavor/internal/state"
)

// The shared --flavor contract, pinned on single- and multi-client
// configurations: one alias table, one error language, and the same
// scoping semantics on every command — so "bfctl <cmd> --flavor <name>"
// targets exactly the client the TUI's flavor switch would.

// testFlavorState points bfctl's config and cache at fresh temp directories
// and records one client per given flavor (interface 16001, no addons), so
// the flavor resolution paths see a deterministic configuration.
func testFlavorState(t *testing.T, flavors ...client.Flavor) {
	t.Helper()
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
		st.Clients[flavor] = &state.ClientState{
			Flavor:    flavor,
			Root:      filepath.Join(t.TempDir(), "_client_"),
			Interface: 16001,
			Addons:    map[string]*state.AddonState{},
		}
	}
	if err := state.Open(path).Save(st); err != nil {
		t.Fatal(err)
	}
}

// TestParseFlavorAcceptsEveryAlias pins the shared alias table on the CLI:
// every accepted spelling lands in its canonical flavor — camelot folds to
// forever, and the separator-tolerant spellings parse too — and anything
// else is rejected with the message naming every accepted flavor.
func TestParseFlavorAcceptsEveryAlias(t *testing.T) {
	aliases := map[string]client.Flavor{
		"mainline":          client.FlavorMainline,
		"retail":            client.FlavorMainline,
		"forever":           client.FlavorForever,
		"camelot":           client.FlavorForever,
		"wow-forever":       client.FlavorForever,
		"classic":           client.FlavorClassic,
		"vanilla":           client.FlavorClassic,
		"Classic Era":       client.FlavorClassic,
		"mists":             client.FlavorMists,
		"mop":               client.FlavorMists,
		"mists_of_pandaria": client.FlavorMists,
	}
	for spelling, want := range aliases {
		got, err := parseFlavor(spelling)
		if err != nil {
			t.Errorf("parseFlavor(%q) failed: %v", spelling, err)
			continue
		}
		if got != want {
			t.Errorf("parseFlavor(%q) = %q, want %q", spelling, got, want)
		}
	}
	_, err := parseFlavor("bogus")
	if err == nil {
		t.Fatal("parseFlavor(\"bogus\") succeeded, want a rejection")
	}
	for _, want := range []string{"unknown flavor", "mainline, forever, classic or mists", "camelot"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("parseFlavor error %q is missing %q", err, want)
		}
	}
}

// TestTargetFlavorSingleClient pins the single-client default: without the
// flag the one configured flavor is used, and a flag spelling resolves to
// the same flavor through the alias table.
func TestTargetFlavorSingleClient(t *testing.T) {
	testFlavorState(t, client.FlavorForever)
	got, err := targetFlavor("")
	if err != nil {
		t.Fatalf("targetFlavor(\"\"): %v", err)
	}
	if got != client.FlavorForever {
		t.Fatalf("targetFlavor(\"\") = %q, want forever", got)
	}
	got, err = targetFlavor("camelot")
	if err != nil {
		t.Fatalf("targetFlavor(\"camelot\"): %v", err)
	}
	if got != client.FlavorForever {
		t.Fatalf("targetFlavor(\"camelot\") = %q, want forever (the alias folds)", got)
	}
}

// TestTargetFlavorMultiClientRequiresTheFlag pins the multi-client
// contract: without the flag the error lists the configured flavors and
// names the fix; with it the named flavor wins.
func TestTargetFlavorMultiClientRequiresTheFlag(t *testing.T) {
	testFlavorState(t, client.FlavorForever, client.FlavorClassic)
	_, err := targetFlavor("")
	if err == nil {
		t.Fatal("targetFlavor(\"\") with several clients succeeded, want an error")
	}
	for _, want := range []string{"several clients configured (classic, forever)", "pass --flavor"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("targetFlavor error %q is missing %q", err, want)
		}
	}
	got, err := targetFlavor("classic")
	if err != nil {
		t.Fatalf("targetFlavor(\"classic\"): %v", err)
	}
	if got != client.FlavorClassic {
		t.Fatalf("targetFlavor(\"classic\") = %q, want classic", got)
	}
}

// TestTargetFlavorRejectsUnknownAndUnconfigured pins the two rejection
// paths: an unknown spelling lists the accepted flavors, and a known but
// unconfigured flavor names "bfctl init" as the fix.
func TestTargetFlavorRejectsUnknownAndUnconfigured(t *testing.T) {
	testFlavorState(t, client.FlavorForever)
	if _, err := targetFlavor("bogus"); err == nil || !strings.Contains(err.Error(), "unknown flavor") {
		t.Fatalf("targetFlavor(\"bogus\") = %v, want the unknown-flavor error", err)
	}
	_, err := targetFlavor("mists")
	if err == nil {
		t.Fatal("targetFlavor(\"mists\") with no mists client succeeded, want an error")
	}
	for _, want := range []string{"no client configured for flavor mists", "bfctl init"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("targetFlavor error %q is missing %q", err, want)
		}
	}
}

// TestTargetFlavorWithoutClient pins the empty-configuration error: it
// points straight at "bfctl init".
func TestTargetFlavorWithoutClient(t *testing.T) {
	testFlavorState(t)
	_, err := targetFlavor("")
	if err == nil {
		t.Fatal("targetFlavor(\"\") with no client succeeded, want an error")
	}
	if !strings.Contains(err.Error(), `no client configured: run "bfctl init" first`) {
		t.Errorf("targetFlavor error %q is missing the bfctl init step", err)
	}
}

// TestScopedFlavorIsOptional pins the sync/status side of the contract:
// no flag means every configured flavor (an empty scope, no error, even
// with several clients), and a flag goes through the same validation as
// the one-client commands.
func TestScopedFlavorIsOptional(t *testing.T) {
	testFlavorState(t, client.FlavorForever, client.FlavorClassic)
	got, err := scopedFlavor("")
	if err != nil {
		t.Fatalf("scopedFlavor(\"\"): %v", err)
	}
	if got != "" {
		t.Fatalf("scopedFlavor(\"\") = %q, want the empty all-flavors scope", got)
	}
	got, err = scopedFlavor("forever")
	if err != nil {
		t.Fatalf("scopedFlavor(\"forever\"): %v", err)
	}
	if got != client.FlavorForever {
		t.Fatalf("scopedFlavor(\"forever\") = %q, want forever", got)
	}
	if _, err := scopedFlavor("bogus"); err == nil || !strings.Contains(err.Error(), "unknown flavor") {
		t.Fatalf("scopedFlavor(\"bogus\") = %v, want the unknown-flavor error", err)
	}
	if _, err := scopedFlavor("mists"); err == nil || !strings.Contains(err.Error(), "no client configured for flavor mists") {
		t.Fatalf("scopedFlavor(\"mists\") = %v, want the unconfigured-flavor error", err)
	}
}

// testMultiSyncState records two clients — forever and classic — carrying
// one manual addon each, so a flavor-scoped sync run can be told apart per
// client without any network access.
func testMultiSyncState(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	if err := paths.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	path, err := paths.StateFile()
	if err != nil {
		t.Fatal(err)
	}
	manual := func(id string) *state.AddonState {
		return &state.AddonState{ID: id, Source: state.SourceRef{Kind: state.SourceManual, Slug: id}, Strategy: state.StrategyManual}
	}
	st := state.New()
	st.Clients[client.FlavorForever] = &state.ClientState{
		Flavor: client.FlavorForever, Root: filepath.Join(t.TempDir(), "_forever_"), Interface: 16001,
		Addons: map[string]*state.AddonState{"ForeverAddOn": manual("ForeverAddOn")},
	}
	st.Clients[client.FlavorClassic] = &state.ClientState{
		Flavor: client.FlavorClassic, Root: filepath.Join(t.TempDir(), "_classic_"), Interface: 11507,
		Addons: map[string]*state.AddonState{"ClassicAddOn": manual("ClassicAddOn")},
	}
	if err := state.Open(path).Save(st); err != nil {
		t.Fatal(err)
	}
}

// TestSyncFlavorFlagScopesTheRun pins "bfctl sync --flavor" end to end:
// the run works on the named flavor's addons only, the aliases fold, and
// unknown or unconfigured flavors fail as usage errors that teach the fix.
func TestSyncFlavorFlagScopesTheRun(t *testing.T) {
	testMultiSyncState(t)

	stdout, _, code := runSyncCaptured(t, "--flavor", "forever", "--json")
	if code != ExitOK {
		t.Fatalf("sync --flavor forever exit = %d, want %d", code, ExitOK)
	}
	var rep syncJSON
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("sync --json output is not one JSON document: %v\n%s", err, stdout)
	}
	if len(rep.Results) != 1 || rep.Results[0].Addon != "ForeverAddOn" || rep.Results[0].Flavor != "forever" {
		t.Fatalf("sync --flavor forever results = %+v, want only ForeverAddOn/forever", rep.Results)
	}

	// "camelot" is the same client as "forever".
	stdout, _, code = runSyncCaptured(t, "--flavor", "camelot", "--json")
	if code != ExitOK {
		t.Fatalf("sync --flavor camelot exit = %d, want %d", code, ExitOK)
	}
	rep = syncJSON{}
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("sync --json output is not one JSON document: %v\n%s", err, stdout)
	}
	if len(rep.Results) != 1 || rep.Results[0].Flavor != "forever" {
		t.Fatalf("sync --flavor camelot results = %+v, want only the forever client's addon", rep.Results)
	}

	// No flag keeps every configured flavor in scope.
	stdout, _, code = runSyncCaptured(t, "--json")
	if code != ExitOK {
		t.Fatalf("sync exit = %d, want %d", code, ExitOK)
	}
	rep = syncJSON{}
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("sync --json output is not one JSON document: %v\n%s", err, stdout)
	}
	if len(rep.Results) != 2 {
		t.Fatalf("unscoped sync results = %+v, want both clients' addons", rep.Results)
	}
}

// TestSyncFlavorFlagRejectsBadFlavors pins the usage errors: an unknown
// spelling and a known-but-unconfigured flavor both exit 2 and name the
// fix in stderr.
func TestSyncFlavorFlagRejectsBadFlavors(t *testing.T) {
	testMultiSyncState(t)
	for _, tc := range []struct {
		flavor string
		want   string
	}{
		{"bogus", "unknown flavor"},
		{"mists", "no client configured for flavor mists"},
	} {
		_, stderr, code := runSyncCaptured(t, "--flavor", tc.flavor)
		if code != ExitUsage {
			t.Errorf("sync --flavor %s exit = %d, want %d", tc.flavor, code, ExitUsage)
		}
		if !strings.Contains(stderr, tc.want) {
			t.Errorf("sync --flavor %s stderr = %q, want it to contain %q", tc.flavor, stderr, tc.want)
		}
	}
}

// TestFilterStatusScopesTheReport pins the status side: --flavor keeps the
// named client's section and addons in every output shape and drops the
// others; no flavor leaves the report untouched.
func TestFilterStatusScopesTheReport(t *testing.T) {
	st := service.Status{
		Clients: []service.ClientRow{
			{Flavor: "classic", FlavorName: "Classic Era", Addons: []service.AddonRow{{ID: "ClassicAddOn", Flavor: "classic"}}},
			{Flavor: "forever", FlavorName: "Forever", Addons: []service.AddonRow{{ID: "ForeverAddOn", Flavor: "forever"}}},
		},
		Addons: []service.AddonRow{
			{ID: "ClassicAddOn", Flavor: "classic"},
			{ID: "ForeverAddOn", Flavor: "forever"},
		},
	}
	got := filterStatus(st, client.FlavorForever)
	if len(got.Clients) != 1 || got.Clients[0].Flavor != "forever" {
		t.Fatalf("filterStatus clients = %+v, want only the forever client", got.Clients)
	}
	if len(got.Addons) != 1 || got.Addons[0].ID != "ForeverAddOn" {
		t.Fatalf("filterStatus addons = %+v, want only ForeverAddOn", got.Addons)
	}
	if all := filterStatus(st, ""); len(all.Clients) != 2 || len(all.Addons) != 2 {
		t.Fatalf("filterStatus(\"\") = %+v, want the whole report", all)
	}
}

// TestStatusFlavorFlagScopesTheJSONReport drives "bfctl status --flavor
// --json" end to end on the two-client fixture: one client section and one
// addon survive the scope.
func TestStatusFlavorFlagScopesTheJSONReport(t *testing.T) {
	testMultiSyncState(t)
	var code int
	stdout := captureStdout(t, func() { code = runStatus([]string{"--flavor", "classic", "--json"}, BuildInfo{}) })
	if code != ExitOK {
		t.Fatalf("status --flavor classic --json exit = %d, want %d", code, ExitOK)
	}
	var st service.Status
	if err := json.Unmarshal([]byte(stdout), &st); err != nil {
		t.Fatalf("status --json output is not one JSON document: %v\n%s", err, stdout)
	}
	if len(st.Clients) != 1 || st.Clients[0].Flavor != "classic" {
		t.Fatalf("scoped status clients = %+v, want only the classic client", st.Clients)
	}
	if len(st.Addons) != 1 || st.Addons[0].ID != "ClassicAddOn" {
		t.Fatalf("scoped status addons = %+v, want only ClassicAddOn", st.Addons)
	}
}

// TestScanTargetFlavorSelection pins the scan's target-client rules:
// --flavor/--interface pin it, --flavor alone re-detects the configured
// client, several clients without --flavor are an error that lists them,
// and --interface without --flavor is rejected.
func TestScanTargetFlavorSelection(t *testing.T) {
	testFlavorState(t, client.FlavorForever, client.FlavorClassic)

	if _, err := scanTarget("", 16001); err == nil || !strings.Contains(err.Error(), "--interface requires --flavor") {
		t.Fatalf("scanTarget(\"\", 16001) = %v, want the pairing error", err)
	}
	if _, err := scanTarget("bogus", 16001); err == nil || !strings.Contains(err.Error(), "unknown flavor") {
		t.Fatalf("scanTarget(\"bogus\", 16001) = %v, want the unknown-flavor error", err)
	}

	target, err := scanTarget("camelot", 12345)
	if err != nil {
		t.Fatalf("scanTarget(\"camelot\", 12345): %v", err)
	}
	if target.Flavor != client.FlavorForever || target.Interface != 12345 {
		t.Fatalf("scanTarget(\"camelot\", 12345) = %+v, want the pinned forever/12345 target", target)
	}

	// --flavor alone re-detects the configured client, falling back to its
	// recorded interface when the root does not detect.
	target, err = scanTarget("classic", 0)
	if err != nil {
		t.Fatalf("scanTarget(\"classic\", 0): %v", err)
	}
	if target.Flavor != client.FlavorClassic || target.Interface != 16001 {
		t.Fatalf("scanTarget(\"classic\", 0) = %+v, want the configured classic client", target)
	}

	if _, err := scanTarget("mists", 0); err == nil || !strings.Contains(err.Error(), "no client configured for flavor mists") {
		t.Fatalf("scanTarget(\"mists\", 0) = %v, want the unconfigured-flavor error", err)
	}

	_, err = scanTarget("", 0)
	if err == nil {
		t.Fatal("scanTarget(\"\", 0) with several clients succeeded, want an error listing them")
	}
	for _, want := range []string{"several clients configured (classic, forever)", "pass --flavor"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("scanTarget error %q is missing %q", err, want)
		}
	}
}
