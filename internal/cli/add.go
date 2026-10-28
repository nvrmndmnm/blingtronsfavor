package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/github"
	"github.com/itp5b5/blessforge/internal/provider"
	"github.com/itp5b5/blessforge/internal/resolver"
	"github.com/itp5b5/blessforge/internal/service"
	"github.com/itp5b5/blessforge/internal/state"
)

// runAdd implements "bfctl add <source>": GitHub "owner/repo" references
// and github.com URLs parse into a repository reference, validate that the
// repository resolves, resolve an install Plan, download and unpack the
// matching release asset through the atomic installer and record state +
// upstream tag. Wago Addons references (the addon page URL or "wago:<key>")
// install through the Wago provider and stay bound to it for updates;
// CurseForge references (the addon page URL or "cfwidget:<key>") install
// through the cfwidget CurseForge mirror provider and stay bound to it;
// any other git remote installs through the git-direct provider. A GitHub
// Plan that needs a source build is composed locally through the packager
// pipeline (.pkgmeta externals, keyword substitution, TOC flavor split) and
// installed the same way. While the work runs, progress reports stream to
// stderr through the shared renderer (deliverable D4); stdout keeps the
// frozen result lines.
func runAdd(args []string, bi BuildInfo) int {
	fs := subcommandFlagSet("add")
	branchFlag := fs.String("branch", "", "branch to track instead of stable releases")
	strategyFlag := fs.String("strategy", "", "install strategy pin: \"release\" pins to GitHub releases and silences the staleness detector")
	flavorFlag := fs.String("flavor", "", "client flavor to install for: mainline, forever (a.k.a. camelot), classic or mists (required when several are configured)")
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
	// One renderer per run: progress on stderr, stdout untouched. "bfctl
	// add" has no graceful SIGINT path, so the guard only restores the
	// terminal before the process dies exactly as it otherwise would.
	prog := newProgressRenderer(os.Stderr, progressOptions{Quiet: logQuiet, TTY: stderrIsTTY()})
	defer prog.Stop()
	defer interruptGuard(prog)()
	// Wago Addons references (the addon page URL or "wago:<key>") install
	// through the Wago provider; the manifest entry records the wago source
	// so sync keeps maintaining the addon through Wago, even when the
	// addon's page lists a GitHub repository that no longer resolves.
	if key, ok := parseWagoRef(fs.Arg(0)); ok {
		if *branchFlag != "" || *strategyFlag != "" {
			fmt.Fprintln(os.Stderr, "bfctl add: --branch and --strategy only apply to GitHub repositories")
			return ExitUsage
		}
		flavor, ferr := targetFlavor(*flavorFlag)
		if ferr != nil {
			fmt.Fprintf(os.Stderr, "bfctl add: %v\n", ferr)
			return ExitUsage
		}
		return addWago(key, flavor, *forceFlag, prog)
	}
	// CurseForge references (the addon page URL or "cfwidget:<key>") install
	// through the cfwidget mirror provider; the manifest entry records the
	// cfwidget source so sync keeps maintaining the addon through the
	// mirror. A curseforge.com page is not a git remote and must never reach
	// the git-direct path.
	if key, ok := parseCfRef(fs.Arg(0)); ok {
		if *branchFlag != "" || *strategyFlag != "" {
			fmt.Fprintln(os.Stderr, "bfctl add: --branch and --strategy only apply to GitHub repositories")
			return ExitUsage
		}
		flavor, ferr := targetFlavor(*flavorFlag)
		if ferr != nil {
			fmt.Fprintf(os.Stderr, "bfctl add: %v\n", ferr)
			return ExitUsage
		}
		return addCfwidget(key, flavor, *forceFlag, prog)
	}
	ref, err := resolver.ParseRepo(fs.Arg(0))
	if err != nil {
		// Non-GitHub git remotes install through the git-direct provider:
		// shallow clone plus the full packager pipeline ("source-git").
		if isSourceGitURL(fs.Arg(0)) {
			flavor, ferr := targetFlavor(*flavorFlag)
			if ferr != nil {
				fmt.Fprintf(os.Stderr, "bfctl add: %v\n", ferr)
				return ExitUsage
			}
			if *branchFlag != "" || *strategyFlag != "" {
				fmt.Fprintln(os.Stderr, "bfctl add: --branch and --strategy only apply to GitHub repositories")
				return ExitUsage
			}
			return addSourceGit(fs.Arg(0), flavor, *forceFlag, prog)
		}
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
	switch *strategyFlag {
	case "":
	case "release":
		if *branchFlag != "" {
			fmt.Fprintln(os.Stderr, "bfctl add: --strategy release and --branch are mutually exclusive")
			return ExitUsage
		}
		tracking = "release"
	default:
		fmt.Fprintf(os.Stderr, "bfctl add: unsupported --strategy %q (only \"release\" is a pin)\n", *strategyFlag)
		return ExitUsage
	}
	res, err := newResolver(bi)
	if err != nil {
		slog.Error("create github client", "error", err)
		return ExitError
	}
	ctx := context.Background()

	// Validate the repository resolves before planning anything.
	plan, err := service.PlanAdd(ctx, res, flavor, ref, tracking, prog.Handle)
	if err != nil {
		prog.Stop()
		fmt.Fprintf(os.Stderr, "bfctl add: %v\n", err)
		return ExitError
	}
	if *explainFlag {
		// The status line is still live (a successful plan keeps reporting
		// through InstallPlan), so the plan note goes through Suspend like
		// every mid-run stdout line.
		prog.Suspend(func() { fmt.Printf("plan: %s\n", plan.Reason) })
	}

	rec := state.AddonState{
		ID:          ref.Repo,
		DisplayName: ref.Repo,
		Source:      state.SourceRef{Kind: state.SourceGitHub, URL: ref.URL(), Slug: ref.Slug()},
		Strategy:    plan.Strategy,
		Tracking:    tracking,
	}
	if err := service.InstallPlan(ctx, res, flavor, rec, plan, *forceFlag, prog.Handle); err != nil {
		prog.Stop()
		slog.Error("install", "addon", rec.ID, "error", err)
		return ExitError
	}
	prog.Stop()
	ver := plan.Version
	if ver == "" {
		ver = plan.Ref
	}
	fmt.Printf("added %s %s (%s)\n", rec.ID, ver, plan.Strategy)
	return ExitOK
}

// targetFlavor picks the client flavor "bfctl add" installs for: --flavor
// when it is passed, otherwise the single configured client flavor.
func targetFlavor(flavorFlag string) (client.Flavor, error) {
	st, err := loadState()
	if err != nil {
		return "", fmt.Errorf("read state: %w", err)
	}
	if flavorFlag != "" {
		flavor, ok := client.FlavorForKey(flavorFlag)
		if !ok {
			return "", fmt.Errorf("unknown flavor %q (want mainline, forever, classic or mists; \"camelot\" is an accepted spelling of forever)", flavorFlag)
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

// parseWagoRef recognizes Wago Addons references and returns the provider
// key: the addon page URL "https://addons.wago.io/addons/<key>" (query
// strings, fragments and deep links like "/versions" are ignored) and the
// direct short form "wago:<key>". The key is the page slug, the TOC
// "## X-Wago-ID" short ID or the legacy numeric ID — the Wago provider
// resolves all three.
func parseWagoRef(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if key, ok := strings.CutPrefix(raw, "wago:"); ok {
		key = strings.Trim(strings.TrimSpace(key), "/")
		return key, key != ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	switch strings.ToLower(u.Hostname()) {
	case "addons.wago.io", "www.addons.wago.io":
	default:
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || !strings.EqualFold(parts[0], "addons") {
		return "", false
	}
	return parts[1], parts[1] != ""
}

// parseCfRef recognizes CurseForge references and returns the cfwidget
// mirror key: the addon page URL "https://www.curseforge.com/wow/addons/<slug>"
// (query strings, fragments and deep links like "/files/123" are ignored)
// and the direct short form "cfwidget:<key>". The key is the project slug
// or the numeric project ID (the TOC "## X-Curse-Project-ID" value) — the
// cfwidget provider resolves both. Non-WoW CurseForge games are rejected.
func parseCfRef(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if key, ok := strings.CutPrefix(raw, "cfwidget:"); ok {
		key = strings.Trim(strings.TrimSpace(key), "/")
		return key, key != ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	switch strings.ToLower(u.Hostname()) {
	case "curseforge.com", "www.curseforge.com":
	default:
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 || !strings.EqualFold(parts[0], "wow") || !strings.EqualFold(parts[1], "addons") {
		return "", false
	}
	return parts[2], parts[2] != ""
}

// isSourceGitURL reports whether raw is a git remote URL for any forge or
// host, as opposed to an "owner/repo" GitHub reference (which the resolver
// handles).
func isSourceGitURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "http", "https", "git", "ssh":
		return u.Host != ""
	default:
		return false
	}
}

// addSourceGit installs an arbitrary git repository through the git-direct
// provider: "git ls-remote" picks the version, a shallow clone feeds the
// full packager pipeline (TOC splitting, .pkgmeta externals) and the atomic
// installer commits the result. Progress for the unit reports through prog.
func addSourceGit(raw string, flavor client.Flavor, force bool, prog progressRenderer) int {
	prov, err := providerFor(state.SourceGit, flavor)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bfctl add: %v\n", err)
		return ExitError
	}
	id := gitRepoName(raw)
	ref := state.SourceRef{Kind: state.SourceGit, URL: raw}
	up, err := service.InstallProvider(context.Background(), prov, ref, id, raw, flavor, force, prog.Handle)
	if err != nil {
		prog.Stop()
		fmt.Fprintf(os.Stderr, "bfctl add: %v\n", err)
		return ExitError
	}
	prog.Stop()
	fmt.Printf("added %s %s (%s, source-git)\n", id, up.Version, up.Strategy)
	return ExitOK
}

// addWago installs a Wago Addons package through the Wago provider. The
// manifest entry records the wago source, so sync keeps maintaining the
// addon through Wago — the chosen source of truth stays authoritative even
// when the addon's page lists a GitHub repository that 404s. Progress for
// the unit reports through prog.
func addWago(key string, flavor client.Flavor, force bool, prog progressRenderer) int {
	prov, err := providerFor(state.SourceWago, flavor)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bfctl add: %v\n", err)
		return ExitError
	}
	ref := state.SourceRef{Kind: state.SourceWago, Slug: key}
	up, err := service.InstallProvider(context.Background(), prov, ref, key, "wago:"+key, flavor, force, prog.Handle)
	if err != nil {
		prog.Stop()
		fmt.Fprintf(os.Stderr, "bfctl add: %v\n", err)
		return ExitError
	}
	prog.Stop()
	fmt.Printf("added %s %s (%s, wago)\n", key, up.Version, up.Strategy)
	return ExitOK
}

// addCfwidget installs a CurseForge addon through the cfwidget mirror
// provider. The manifest entry records the cfwidget source, so sync keeps
// maintaining the addon through the mirror: the mirror resolves the
// project's release files and the packages come from CurseForge's open
// CDN — the addon page itself is never scraped and never cloned. Progress
// for the unit reports through prog.
func addCfwidget(key string, flavor client.Flavor, force bool, prog progressRenderer) int {
	prov, err := providerFor(state.SourceCfwidget, flavor)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bfctl add: %v\n", err)
		return ExitError
	}
	ref := state.SourceRef{Kind: state.SourceCfwidget, Slug: key}
	up, err := service.InstallProvider(context.Background(), prov, ref, key, "cfwidget:"+key, flavor, force, prog.Handle)
	if err != nil {
		prog.Stop()
		fmt.Fprintf(os.Stderr, "bfctl add: %v\n", err)
		return ExitError
	}
	prog.Stop()
	fmt.Printf("added %s %s (%s, cfwidget)\n", key, up.Version, up.Strategy)
	return ExitOK
}

// providerFor builds the provider for one source kind with the flavor's
// client context (the interface version the packager pipeline writes into
// staged .toc files).
func providerFor(kind state.SourceKind, flavor client.Flavor) (provider.Provider, error) {
	st, err := loadState()
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	cs := st.Client(flavor)
	return provider.For(kind, provider.Options{Flavor: flavor, Interface: cs.Interface})
}

// gitRepoName renders the repository name of a git URL as the addon id,
// e.g. "https://gitlab.com/group/addon.git" -> "addon".
func gitRepoName(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	name := filepath.Base(strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/"))
	if name == "." || name == "/" || name == "" {
		return raw
	}
	return name
}

// newResolver builds the GitHub-backed resolver for a command run.
func newResolver(bi BuildInfo) (*resolver.Resolver, error) {
	gh, err := github.New(github.Options{Version: bi.Version, Token: github.TokenFromEnv()})
	if err != nil {
		return nil, err
	}
	return &resolver.Resolver{GH: gh}, nil
}
