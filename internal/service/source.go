package service

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nvrmndmnm/blingtronsfavor/internal/client"
	"github.com/nvrmndmnm/blingtronsfavor/internal/installer"
	"github.com/nvrmndmnm/blingtronsfavor/internal/packager"
	"github.com/nvrmndmnm/blingtronsfavor/internal/paths"
	"github.com/nvrmndmnm/blingtronsfavor/internal/provider"
	"github.com/nvrmndmnm/blingtronsfavor/internal/resolver"
	"github.com/nvrmndmnm/blingtronsfavor/internal/state"
)

// PlanAdd resolves the install plan for one GitHub repository add: the
// repository must resolve before anything is planned, and the plan decides
// the install strategy. Both failures surface as "cannot resolve <slug>"
// errors. Progress reports the resolve phase and, on failure, the terminal
// failure — a successful resolve continues in InstallPlan, which reports
// the rest of the unit.
func PlanAdd(ctx context.Context, res *resolver.Resolver, flavor client.Flavor, ref resolver.RepoRef, tracking string, onProgress ProgressFunc) (resolver.Plan, error) {
	slot := progressSlot{p: newProgressSink(onProgress), unit: Unit{Flavor: flavor, AddonID: ref.Repo}, index: 1, total: 1}
	slot.emit(PhaseResolving, Detail{})
	repo, err := res.GH.Repository(ctx, ref.Owner, ref.Repo)
	if err != nil {
		err = fmt.Errorf("cannot resolve %s: %w", ref.Slug(), err)
		slot.rateLimited(PhaseResolving, err)
		slot.emit(PhaseFailed, Detail{Err: err})
		return resolver.Plan{}, err
	}
	plan, err := res.Resolve(ctx, resolver.Request{
		Owner:         ref.Owner,
		Repo:          ref.Repo,
		Flavor:        flavor,
		Tracking:      tracking,
		DefaultBranch: repo.DefaultBranch,
	})
	if err != nil {
		err = fmt.Errorf("cannot resolve %s: %w", ref.Slug(), err)
		slot.rateLimited(PhaseResolving, err)
		slot.emit(PhaseFailed, Detail{Err: err})
		return resolver.Plan{}, err
	}
	return plan, nil
}

// InstallPlan installs one resolved plan into the client's addon directory
// and records it on the addon's state entry: a "release" plan downloads and
// unpacks its release asset, a "source-pkgmeta" plan builds the repository
// at the planned ref through the packager pipeline (shallow working copy,
// .pkgmeta externals, keyword substitution, debug stripping, TOC flavor
// split). Both land through the atomic installer, so a failure or a
// cancellation rolls back to the previous install. The unit's phases are
// reported through onProgress — download, install and the terminal
// completion — with a single-unit workload counter (1 of 1).
func InstallPlan(ctx context.Context, res *resolver.Resolver, flavor client.Flavor, rec state.AddonState, plan resolver.Plan, force bool, onProgress ProgressFunc) error {
	slot := progressSlot{p: newProgressSink(onProgress), unit: Unit{Flavor: flavor, AddonID: rec.ID}, index: 1, total: 1}
	dir, err := os.MkdirTemp("", "bfctl-install-")
	if err != nil {
		err = fmt.Errorf("create fetch directory: %w", err)
		slot.emit(PhaseFailed, Detail{Err: err})
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	slot.emit(PhaseDownloading, Detail{Version: plan.Version, Ref: plan.Ref})
	var root string
	if plan.Strategy == state.StrategyRelease {
		root, err = res.Fetch(ctx, plan, dir)
		if err != nil {
			slot.rateLimited(PhaseDownloading, err)
			slot.emit(PhaseFailed, Detail{Err: err})
			return err
		}
	} else {
		ref, err := sourceRepo(&rec)
		if err != nil {
			slot.emit(PhaseFailed, Detail{Err: err})
			return err
		}
		if err := buildSourcePlan(ctx, plan, ref, flavor, dir); err != nil {
			slot.emit(PhaseFailed, Detail{Err: err})
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
		slot.emit(PhaseFailed, Detail{Err: err})
		return err
	}
	slot.emit(PhaseInstalling, Detail{Version: plan.Version, Ref: plan.Ref})
	if err := in.Install(ctx, installer.Request{Flavor: flavor, Addon: rec, SourceDir: root, Force: force}); err != nil {
		slot.emit(PhaseFailed, Detail{Err: err})
		return err
	}
	slot.emit(PhaseDone, Detail{Version: rec.InstalledVersion})
	return nil
}

// InstallProvider installs one addon through a provider-backed source kind
// (wago, cfwidget, git-direct): Check resolves the upstream version and
// changelog, Fetch materializes the payload into a staging directory and the
// atomic installer commits it under the state lock. id is the manifest key
// and name labels the source in errors (never a signed download link — the
// providers redact those themselves). The resolved upstream is returned so
// callers can render their own confirmation. The unit's phases — resolve,
// download, install and the terminal completion — are reported through
// onProgress with a single-unit workload counter (1 of 1).
func InstallProvider(ctx context.Context, prov provider.Provider, ref state.SourceRef, id, name string, flavor client.Flavor, force bool, onProgress ProgressFunc) (provider.Upstream, error) {
	slot := progressSlot{p: newProgressSink(onProgress), unit: Unit{Flavor: flavor, AddonID: id}, index: 1, total: 1}
	slot.emit(PhaseResolving, Detail{})
	up, err := prov.Check(ctx, ref)
	if err != nil {
		err = fmt.Errorf("cannot resolve %s: %w", name, err)
		slot.rateLimited(PhaseResolving, err)
		slot.emit(PhaseFailed, Detail{Err: err})
		return up, err
	}
	slot.emit(PhaseDownloading, Detail{Version: up.Version})
	dir, err := os.MkdirTemp("", "bfctl-add-")
	if err != nil {
		err = fmt.Errorf("create fetch directory: %w", err)
		slot.emit(PhaseFailed, Detail{Err: err})
		return up, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := prov.Fetch(ctx, ref, dir); err != nil {
		err = fmt.Errorf("cannot fetch %s: %w", name, err)
		slot.rateLimited(PhaseDownloading, err)
		slot.emit(PhaseFailed, Detail{Err: err})
		return up, err
	}
	rec := state.AddonState{
		ID:               id,
		DisplayName:      id,
		Source:           ref,
		Strategy:         up.Strategy,
		Tracking:         "stable",
		InstalledVersion: up.Version,
		ETag:             up.Version,
		Changelog:        up.Changelog,
	}
	statePath, err := paths.StateFile()
	if err != nil {
		err = fmt.Errorf("resolve state file: %w", err)
		slot.emit(PhaseFailed, Detail{Err: err})
		return up, err
	}
	slot.emit(PhaseInstalling, Detail{Version: up.Version})
	in := &installer.Installer{Store: state.Open(statePath)}
	if err := in.Install(ctx, installer.Request{Flavor: flavor, Addon: rec, SourceDir: dir, Force: force}); err != nil {
		err = fmt.Errorf("install %s: %w", id, err)
		slot.emit(PhaseFailed, Detail{Err: err})
		return up, err
	}
	slot.emit(PhaseDone, Detail{Version: up.Version})
	return up, nil
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
