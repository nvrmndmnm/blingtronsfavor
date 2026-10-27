package service

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/installer"
	"github.com/itp5b5/blessforge/internal/packager"
	"github.com/itp5b5/blessforge/internal/resolver"
	"github.com/itp5b5/blessforge/internal/state"
)

// InstallPlan installs one resolved plan into the client's addon directory
// and records it on the addon's state entry: a "release" plan downloads and
// unpacks its release asset, a "source-pkgmeta" plan builds the repository
// at the planned ref through the packager pipeline (shallow working copy,
// .pkgmeta externals, keyword substitution, debug stripping, TOC flavor
// split). Both land through the atomic installer, so a failure or a
// cancellation rolls back to the previous install.
func InstallPlan(ctx context.Context, res *resolver.Resolver, flavor client.Flavor, rec state.AddonState, plan resolver.Plan, force bool) error {
	dir, err := os.MkdirTemp("", "bfctl-install-")
	if err != nil {
		return fmt.Errorf("create fetch directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	var root string
	if plan.Strategy == state.StrategyRelease {
		root, err = res.Fetch(ctx, plan, dir)
		if err != nil {
			return err
		}
	} else {
		ref, err := sourceRepo(&rec)
		if err != nil {
			return err
		}
		if err := buildSourcePlan(ctx, plan, ref, flavor, dir); err != nil {
			return err
		}
		root = dir
	}

	rec.Strategy = plan.Strategy
	rec.InstalledVersion = versionOr(plan.Version, plan.Ref)
	rec.InstalledCommit = ""
	rec.ETag = plan.Version
	rec.UpdatedAt = time.Now().UTC()
	in, err := installerFor()
	if err != nil {
		return err
	}
	return in.Install(ctx, installer.Request{Flavor: flavor, Addon: rec, SourceDir: root, Force: force})
}

// buildSourcePlan composes a source-pkgmeta plan into dest through the
// packager pipeline, targeted at the given client flavor: the working copy
// is built at the plan's ref with its .pkgmeta externals and keyword
// substitution, and the staged folders carry the flavor's interface version
// in their .toc files.
func buildSourcePlan(ctx context.Context, plan resolver.Plan, ref resolver.RepoRef, flavor client.Flavor, dest string) error {
	iface, err := clientInterface(flavor)
	if err != nil {
		return err
	}
	_, err = packager.Build(ctx, packager.Spec{
		Owner: ref.Owner,
		Repo:  ref.Repo,
		URL:   plan.URL,
		Ref:   plan.Ref,
	}, packager.Options{OutDir: dest, Flavor: flavor, Interface: iface})
	return err
}

// clientInterface resolves the recorded interface version of a client
// flavor — the number the packager writes into the staged .toc files.
func clientInterface(flavor client.Flavor) (int, error) {
	st, err := LoadState()
	if err != nil {
		return 0, err
	}
	cs := st.Client(flavor)
	if cs == nil {
		return 0, fmt.Errorf("no client configured for flavor %s: run \"bfctl init\" first", flavor)
	}
	if cs.Interface == 0 {
		return 0, fmt.Errorf("client %s reports no interface version: run \"bfctl init\" first", flavor)
	}
	return cs.Interface, nil
}
