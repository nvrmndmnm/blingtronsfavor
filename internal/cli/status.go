package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"

	"github.com/itp5b5/blessforge/internal/paths"
)

// addonState is the subset of state.json the status skeleton needs. The full
// state model lands with the state management milestone.
type addonState struct {
	Addons map[string]json.RawMessage `json:"addons"`
}

func runStatus(args []string, _ BuildInfo) int {
	fs := subcommandFlagSet("status")
	if code, ok := parseSubcommand(fs, args); !ok {
		return code
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "bfctl status: unexpected argument %q\n", fs.Arg(0))
		subcommandUsage(fs)
		return ExitUsage
	}
	names, err := configuredAddons()
	if err != nil {
		slog.Error("read state", "error", err)
		return ExitError
	}
	if len(names) == 0 {
		fmt.Println("no addons configured")
		return ExitOK
	}
	fmt.Printf("%d addon(s) configured:\n", len(names))
	for _, name := range names {
		fmt.Printf("  %s\n", name)
	}
	return ExitOK
}

// configuredAddons returns the sorted addon names from state.json. An absent
// or empty state yields no names.
func configuredAddons() ([]string, error) {
	statePath, err := paths.StateFile()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st addonState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse %s: %w", statePath, err)
	}
	names := make([]string, 0, len(st.Addons))
	for name := range st.Addons {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
