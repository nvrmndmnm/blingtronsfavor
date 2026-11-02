package tui

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nvrmndmnm/blingtronsfavor/internal/client"
	"github.com/nvrmndmnm/blingtronsfavor/internal/service"
)

// fakeSync replaces the service call: it reports the run options the key
// press started with and returns an empty report, so key routing can be
// tested without any service work or network.
func fakeSync(got chan<- service.SyncOptions) func(context.Context, service.SyncOptions, func(service.Result), service.ProgressFunc) (service.Report, error) {
	return func(_ context.Context, opts service.SyncOptions, _ func(service.Result), _ service.ProgressFunc) (service.Report, error) {
		got <- opts
		return service.Report{CheckOnly: opts.CheckOnly}, nil
	}
}

// awaitOpts waits for the run a key press started.
func awaitOpts(t *testing.T, got <-chan service.SyncOptions) service.SyncOptions {
	t.Helper()
	select {
	case opts := <-got:
		return opts
	case <-time.After(5 * time.Second):
		t.Fatal("the key press never started a run")
		return service.SyncOptions{}
	}
}

func TestCheckKeyRunsCheckOnlyPreview(t *testing.T) {
	m := testModel(t)
	got := make(chan service.SyncOptions, 1)
	m.syncFn = fakeSync(got)
	m = press(t, m, keyMsg('c'))
	opts := awaitOpts(t, got)
	if !opts.CheckOnly {
		t.Fatalf("c started %+v, want a check-only run", opts)
	}
	if opts.Only != nil {
		t.Fatalf("c scoped the run to %+v, want every addon", *opts.Only)
	}
	if !m.checkOnly {
		t.Fatal("the running frame does not know it is a check-only run")
	}
}

func TestUpdateOneKeySyncsOnlySelectedAddon(t *testing.T) {
	m := testModel(t)
	got := make(chan service.SyncOptions, 1)
	m.syncFn = fakeSync(got)
	m = press(t, m, keyMsg('U'))
	opts := awaitOpts(t, got)
	if opts.CheckOnly {
		t.Fatal("U started a check-only run, want a real update")
	}
	want := service.Unit{Flavor: client.FlavorForever, AddonID: "DemoAddon"}
	if opts.Only == nil || *opts.Only != want {
		t.Fatalf("U scoped the run to %+v, want %+v", opts.Only, want)
	}
}

func TestUpdateAllKeyStillSyncsEverything(t *testing.T) {
	m := testModel(t)
	got := make(chan service.SyncOptions, 1)
	m.syncFn = fakeSync(got)
	m = press(t, m, keyMsg('u'))
	opts := awaitOpts(t, got)
	if opts.CheckOnly || opts.Only != nil {
		t.Fatalf("u started %+v, want the unchanged update-all run", opts)
	}
}

func TestRefreshKeyReloadsSnapshotAndStamp(t *testing.T) {
	m := testModel(t)
	stale := time.Now().Add(-time.Hour)
	m.checkedAt = stale

	next, cmd := m.Update(keyMsg('r'))
	m = next.(Model)
	if cmd == nil {
		t.Fatal("r did not return a refresh command")
	}
	next, _ = m.Update(cmd())
	m = next.(Model)

	if !m.checkedAt.After(stale) {
		t.Fatalf("checked stamp = %v, want it updated", m.checkedAt)
	}
	if !strings.Contains(m.statusLine, "refreshed") {
		t.Fatalf("statusLine = %q, want a refreshed message", m.statusLine)
	}
	if !regexp.MustCompile(`checked \d{2}:\d{2}`).MatchString(m.View().Content) {
		t.Fatalf("view is missing the checked stamp:\n%s", m.View().Content)
	}
}

// TestCheckPreviewUsesTheSharedResultLabels pins the check-only preview
// on the shared result vocabulary (the UX contract in docs/bfctl.1): the
// available update settles as "update available" ("available to install"
// with nothing installed), and what would change stays in the row's
// Installed/Upstream columns and the frozen result line.
func TestCheckPreviewUsesTheSharedResultLabels(t *testing.T) {
	m := testModel(t)
	m.results["DemoAddon"] = service.Result{Kind: service.ResultAvailable, From: "12.1.17", To: "12.1.18"}
	m.refreshRows()
	if got := rowText(t, m, "DemoAddon"); got != "update available" {
		t.Fatalf("status = %q, want the update-available label", got)
	}

	m.results["DemoAddon"] = service.Result{Kind: service.ResultAvailable, From: "-", To: "2.0.0"}
	m.refreshRows()
	if got := rowText(t, m, "DemoAddon"); got != "available to install" {
		t.Fatalf("status = %q, want the available-to-install label", got)
	}
}

func TestCheckRunFrameSaysChecking(t *testing.T) {
	m := beginSync(testModel(t))
	m.checkOnly = true
	m.live.total = 2
	if got := m.runFrame(); !strings.HasPrefix(got, "checking") {
		t.Fatalf("runFrame() = %q, want a checking frame", got)
	}
	m.checkOnly = false
	if got := m.runFrame(); !strings.HasPrefix(got, "syncing") {
		t.Fatalf("runFrame() = %q, want the unchanged syncing frame", got)
	}
}

func TestActionKeysWaitWhileSyncing(t *testing.T) {
	m := testModel(t)
	got := make(chan service.SyncOptions, 4)
	m.syncFn = fakeSync(got)
	m.syncing = true
	for _, code := range []rune{'c', 'U', 'u'} {
		m = press(t, m, keyMsg(code))
		if !strings.Contains(m.statusLine, "already running") {
			t.Fatalf("pressing %c while syncing left statusLine %q", code, m.statusLine)
		}
	}
	if len(got) != 0 {
		t.Fatal("a run started while another was already running")
	}
}
