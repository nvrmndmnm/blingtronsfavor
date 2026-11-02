package cli

import (
	"flag"
	"fmt"
	"strings"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/state"
)

// The shared --flavor contract: every command that acts on a client flavor
// registers the same flag, resolves its spellings through the same alias
// table and fails with the same actionable errors — so "bfctl sync --flavor
// camelot" and the TUI's flavor switch target the same client, one to one.
// The two wordings differ only in when the flag is required: the one-client
// commands (add, adopt, remove) must name a flavor when the choice is
// ambiguous, while the scoped commands (sync, status) fall back to every
// configured flavor.

// requiredFlavorUsage is the --flavor wording of the commands that act on
// one client: with several candidates configured the flag is required.
const requiredFlavorUsage = "client flavor to target: mainline, forever (a.k.a. camelot), classic or mists (required when several are configured)"

// scopedFlavorUsage is the --flavor wording of the commands that may scope
// their work to one flavor: without the flag every configured flavor is in
// scope.
const scopedFlavorUsage = "client flavor to target: mainline, forever (a.k.a. camelot), classic or mists (default: every configured flavor)"

// registerFlavorFlag adds the shared --flavor flag to fs and returns its
// value slot. One registration helper keeps the flag's spelling and shape
// identical across every command that offers it.
func registerFlavorFlag(fs *flag.FlagSet, usage string) *string {
	return fs.String("flavor", "", usage)
}

// parseFlavor resolves one --flavor spelling through the shared alias
// table ("camelot" folds to "forever", "retail" to "mainline", and the
// separator-tolerant spellings like "Classic Era" all parse) and rejects
// everything else with the one error message every command repeats.
func parseFlavor(name string) (client.Flavor, error) {
	flavor, ok := client.FlavorForKey(name)
	if !ok {
		return "", fmt.Errorf("unknown flavor %q (want mainline, forever, classic or mists; \"camelot\" is an accepted spelling of forever)", name)
	}
	return flavor, nil
}

// targetFlavor picks the client flavor a one-client command (add, adopt)
// acts on: --flavor when it is passed, otherwise the single configured
// client flavor. With several configured and no flag the error lists them
// and names the fix; with none configured it points at "bfctl init".
func targetFlavor(flavorFlag string) (client.Flavor, error) {
	st, err := loadState()
	if err != nil {
		return "", fmt.Errorf("read state: %w", err)
	}
	if flavorFlag != "" {
		flavor, err := parseFlavor(flavorFlag)
		if err != nil {
			return "", err
		}
		if st.Client(flavor) == nil {
			return "", fmt.Errorf("no client configured for flavor %s: run \"bfctl init\" first", flavor)
		}
		return flavor, nil
	}
	clients := sortedClients(st)
	switch len(clients) {
	case 0:
		return "", fmt.Errorf("no client configured: run \"bfctl init\" first")
	case 1:
		return clients[0].Flavor, nil
	default:
		return "", fmt.Errorf("several clients configured (%s): pass --flavor", clientList(clients))
	}
}

// scopedFlavor resolves the optional --flavor scope of sync and status:
// an empty flag leaves the scope empty — every configured flavor — while a
// name must be a known alias of a configured flavor, rejected with the same
// messages targetFlavor uses.
func scopedFlavor(flavorFlag string) (client.Flavor, error) {
	if flavorFlag == "" {
		return "", nil
	}
	return targetFlavor(flavorFlag)
}

// clientList renders the recorded flavors for error messages.
func clientList(clients []*state.ClientState) string {
	flavors := make([]string, 0, len(clients))
	for _, cs := range clients {
		flavors = append(flavors, string(cs.Flavor))
	}
	return strings.Join(flavors, ", ")
}
