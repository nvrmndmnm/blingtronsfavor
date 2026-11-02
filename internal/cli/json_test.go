package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/nvrmndmnm/blingtronsfavor/internal/paths"
	"github.com/nvrmndmnm/blingtronsfavor/internal/service"
	"github.com/nvrmndmnm/blingtronsfavor/internal/state"
)

// captureStdout collects everything fn prints on stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestStatusJSONRoundTrip keeps the "bfctl status --json" field contract:
// the output must parse as JSON with the documented top-level keys.
func TestStatusJSONRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	if err := paths.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	path, err := paths.StateFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Open(path).Save(state.New()); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if code := runStatus([]string{"--json"}, BuildInfo{Version: "test"}); code != ExitOK {
			t.Errorf("runStatus exit = %d, want %d", code, ExitOK)
		}
	})
	var report map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	for _, key := range []string{"clients", "addons"} {
		if _, ok := report[key]; !ok {
			t.Fatalf("status JSON is missing %q:\n%s", key, out)
		}
	}
}

// TestSyncJSONRoundTrip keeps the "bfctl sync --json" field contract.
func TestSyncJSONRoundTrip(t *testing.T) {
	rep := service.Report{
		Results: []service.Result{
			{Flavor: "forever", AddonID: "DemoAddon", Kind: service.ResultUpdated, From: "1.0.0", To: "2.0.0"},
			{Flavor: "forever", AddonID: "OtherAddon", Kind: service.ResultFailed, Err: errTest},
		},
		Updated: 1,
		Failed:  1,
	}
	out := captureStdout(t, func() {
		if err := printSyncJSON(rep); err != nil {
			t.Errorf("printSyncJSON: %v", err)
		}
	})
	var report struct {
		Results []struct {
			Addon  string `json:"addon"`
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"results"`
		Summary struct {
			Updated int `json:"updated"`
			Failed  int `json:"failed"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if len(report.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(report.Results))
	}
	if report.Results[1].Error != errTest.Error() {
		t.Fatalf("error field = %q, want %q", report.Results[1].Error, errTest.Error())
	}
	if report.Summary.Updated != 1 || report.Summary.Failed != 1 {
		t.Fatalf("summary = %+v, want updated=1 failed=1", report.Summary)
	}
}

// errTest is the sentinel failure used in sync JSON tests.
var errTest = errors.New("boom")
