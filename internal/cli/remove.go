package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/nvrmndmnm/blingtronsfavor/internal/client"
	"github.com/nvrmndmnm/blingtronsfavor/internal/service"
	"github.com/nvrmndmnm/blingtronsfavor/internal/state"
)

// errNotManaged marks "this addon is not in the state file": a runtime
// condition rather than a usage mistake.
var errNotManaged = errors.New("not managed by bfctl")

// runRemove implements "bfctl remove <id>": it trashes the addon's owned
// directories through the atomic installer and drops its state entry.
func runRemove(args []string, _ BuildInfo) int {
	fs := subcommandFlagSet("remove")
	flavorFlag := registerFlavorFlag(fs, requiredFlavorUsage)
	if code, ok := parseSubcommand(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "bfctl remove: expected exactly one addon id — usage: bfctl remove <id>")
		subcommandUsage(fs)
		return ExitUsage
	}
	id := fs.Arg(0)

	st, err := loadState()
	if err != nil {
		return fail("remove", err, ExitError)
	}
	flavor, dirs, err := resolveRemoveFlavor(st, id, *flavorFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bfctl remove: %v\n", err)
		if errors.Is(err, errNotManaged) {
			return ExitError
		}
		return ExitUsage
	}

	if err := service.RemoveAddon(context.Background(), flavor, id); err != nil {
		return fail("remove", err, ExitError)
	}
	word := "directories"
	if len(dirs) == 1 {
		word = "directory"
	}
	fmt.Printf("removed %s (%d %s)\n", id, len(dirs), word)
	return ExitOK
}

// resolveRemoveFlavor picks the client flavor "bfctl remove" operates on:
// --flavor when it is passed, otherwise the single flavor the addon is
// installed for. It also returns the addon's recorded owned directories for
// the summary line.
func resolveRemoveFlavor(st *state.State, id, flavorFlag string) (client.Flavor, []string, error) {
	if flavorFlag != "" {
		flavor, err := parseFlavor(flavorFlag)
		if err != nil {
			return "", nil, err
		}
		cs := st.Client(flavor)
		if cs == nil || cs.Addons[id] == nil {
			return "", nil, fmt.Errorf("%w: no addon %q for flavor %s (see \"bfctl status\")", errNotManaged, id, flavor)
		}
		return flavor, cs.Addons[id].OwnedDirs, nil
	}
	var (
		found client.Flavor
		dirs  []string
		flavs []string
	)
	for _, cs := range sortedClients(st) {
		if a := cs.Addons[id]; a != nil {
			found, dirs = cs.Flavor, a.OwnedDirs
			flavs = append(flavs, string(cs.Flavor))
		}
	}
	switch len(flavs) {
	case 0:
		return "", nil, fmt.Errorf("%w: no addon %q (see \"bfctl status\")", errNotManaged, id)
	case 1:
		return found, dirs, nil
	default:
		return "", nil, fmt.Errorf("addon %q is installed for several flavors (%s): pass --flavor", id, strings.Join(flavs, ", "))
	}
}
