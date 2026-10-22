package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/github"
	"github.com/itp5b5/blessforge/internal/installer"
	"github.com/itp5b5/blessforge/internal/paths"
	"github.com/itp5b5/blessforge/internal/resolver"
	"github.com/itp5b5/blessforge/internal/state"
)

// stateLockTimeout is how long add waits for the state lock when it only
// records a manifest entry (mirrors the installer's own timeout).
const stateLockTimeout = 5 * time.Second

// runAdd implements "bfctl add <github-repo-or-url>": it parses the
// repository reference, validates that the repository resolves, resolves an
// install Plan, downloads and unpacks the matching release asset through the
// atomic installer and records state + upstream tag. A Plan that needs a
// source build is recorded in the manifest without installing anything and
// is skipped by sync until source builds land (M4).
func runAdd(args []string, bi BuildInfo) int {
	fs := subcommandFlagSet("add")
	branchFlag := fs.String("branch", "", "branch to track instead of stable releases")
	flavorFlag := fs.String("flavor", "", "client flavor to install for: mainline, forever, classic or mists (required when several are configured)")
	forceFlag := fs.Bool("force", false, "take over addon directories owned by another addon")
	explainFlag := fs.Bool("explain", false, "show why the plan was selected")
	if code, ok := parseSubcommand(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "bfctl add: expected exactly one repository reference")
		subcommandUsage(fs)
		return ExitUsage
	}
	ref, err := resolver.ParseRepo(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "bfctl add: %v\n", err)
		return ExitUsage
	}
	flavor, err := targetFlavor(*flavorFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bfctl add: %v\n", err)
		return ExitUsage
	}
	tracking := "stable"
	if *branchFlag != "" {
		tracking = *branchFlag
	}
	res, err := newResolver(bi)
	if err != nil {
		slog.Error("create github client", "error", err)
		return ExitError
	}
	ctx := context.Background()

	// Validate the repository resolves before planning anything.
	repo, err := res.GH.Repository(ctx, ref.Owner, ref.Repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bfctl add: cannot resolve %s: %v\n", ref.Slug(), err)
		return ExitError
	}
	plan, err := res.Resolve(ctx, resolver.Request{
		Owner:         ref.Owner,
		Repo:          ref.Repo,
		Flavor:        flavor,
		Tracking:      tracking,
		DefaultBranch: repo.DefaultBranch,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "bfctl add: cannot resolve %s: %v\n", ref.Slug(), err)
		return ExitError
	}
	if *explainFlag {
		fmt.Printf("plan: %s\n", plan.Reason)
	}

	rec := state.AddonState{
		ID:          ref.Repo,
		DisplayName: ref.Repo,
		Source:      state.SourceRef{Kind: state.SourceGitHub, URL: ref.URL(), Slug: ref.Slug()},
		Strategy:    plan.Strategy,
		Tracking:    tracking,
	}
	switch plan.Strategy {
	case state.StrategyRelease:
		rec.InstalledVersion = plan.Version
		rec.ETag = plan.Version
		if err := installPlan(ctx, res, flavor, rec, plan, *forceFlag); err != nil {
			slog.Error("install", "addon", rec.ID, "error", err)
			return ExitError
		}
		fmt.Printf("added %s %s (%s)\n", rec.ID, plan.Version, plan.Strategy)
	default:
		// A source build plan is recorded but not installed: sync reports
		// "source build required" for it and skips it cleanly until M4.
		if err := recordAddon(flavor, &rec); err != nil {
			slog.Error("record manifest entry", "addon", rec.ID, "error", err)
			return ExitError
		}
		fmt.Printf("added %s to the manifest (not installed)\n", rec.ID)
		fmt.Printf("note: %s — installing from source lands with M4; \"bfctl sync\" skips it until then\n", plan.Reason)
	}
	return ExitOK
}

// installPlan downloads and unpacks the plan's release asset into a
// temporary directory and installs it atomically via the installer.
func installPlan(ctx context.Context, res *resolver.Resolver, flavor client.Flavor, rec state.AddonState, plan resolver.Plan, force bool) error {
	dir, err := os.MkdirTemp("", "bfctl-add-")
	if err != nil {
		return fmt.Errorf("create fetch directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	root, err := res.Fetch(ctx, plan, dir)
	if err != nil {
		return err
	}
	statePath, err := paths.StateFile()
	if err != nil {
		return err
	}
	in := &installer.Installer{Store: state.Open(statePath)}
	return in.Install(installer.Request{Flavor: flavor, Addon: rec, SourceDir: root, Force: force})
}

// recordAddon writes one manifest entry under the state lock, for plans that
// do not install anything yet (source builds land with M4).
func recordAddon(flavor client.Flavor, rec *state.AddonState) error {
	statePath, err := paths.StateFile()
	if err != nil {
		return err
	}
	store := state.Open(statePath)
	lock, err := store.Lock(stateLockTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	st, err := store.Load()
	if err != nil {
		return err
	}
	cs := st.Client(flavor)
	if cs == nil {
		return fmt.Errorf("no client configured for flavor %s: run \"bfctl init\" first", flavor)
	}
	if cs.Addons == nil {
		cs.Addons = make(map[string]*state.AddonState)
	}
	rec.UpdatedAt = time.Now().UTC()
	cs.Addons[rec.ID] = rec
	return store.Save(st)
}

// targetFlavor picks the client flavor "bfctl add" installs for: --flavor
// when it is passed, otherwise the single configured client flavor.
func targetFlavor(flavorFlag string) (client.Flavor, error) {
	st, err := loadState()
	if err != nil {
		return "", fmt.Errorf("read state: %w", err)
	}
	if flavorFlag != "" {
		flavor := client.Flavor(flavorFlag)
		switch flavor {
		case client.FlavorMainline, client.FlavorForever, client.FlavorClassic, client.FlavorMists:
		default:
			return "", fmt.Errorf("unknown flavor %q (want mainline, forever, classic or mists)", flavorFlag)
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

// clientList renders the recorded flavors for error messages.
func clientList(clients []*state.ClientState) string {
	flavors := make([]string, 0, len(clients))
	for _, cs := range clients {
		flavors = append(flavors, string(cs.Flavor))
	}
	return strings.Join(flavors, ", ")
}

// newResolver builds the GitHub-backed resolver for a command run.
func newResolver(bi BuildInfo) (*resolver.Resolver, error) {
	gh, err := github.New(github.Options{Version: bi.Version})
	if err != nil {
		return nil, err
	}
	return &resolver.Resolver{GH: gh}, nil
}
