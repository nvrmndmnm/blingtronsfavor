package service

import (
	"context"
	"fmt"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/installer"
	"github.com/itp5b5/blessforge/internal/state"
)

// ReleaseTrackings are the tracking values that mean "follow releases":
// unset (the default), the explicit "stable" mode and the "release" pin.
var releaseTrackings = map[string]bool{"": true, "stable": true, "release": true}

// ToggleTracking flips one addon between release tracking ("stable") and
// branch tracking — following the repository's development branch — and
// returns the new tracking value. Branch tracking needs a GitHub source to
// learn the default branch from; other sources are refused rather than
// guessed at. The tracking mode is what the resolver reads when planning the
// next sync, so the change takes effect on the following update.
func ToggleTracking(ctx context.Context, flavor client.Flavor, id string) (string, error) {
	store, err := openStore()
	if err != nil {
		return "", err
	}
	lock, err := store.Lock(lockTimeout)
	if err != nil {
		return "", err
	}
	defer func() { _ = lock.Unlock() }()

	st, err := store.Load()
	if err != nil {
		return "", err
	}
	cs := st.Client(flavor)
	if cs == nil {
		return "", fmt.Errorf("toggle tracking: no client configured for flavor %s", flavor)
	}
	a := cs.Addons[id]
	if a == nil {
		return "", fmt.Errorf("toggle tracking: addon %q is not managed by bfctl for flavor %s (see \"bfctl status\")", id, flavor)
	}

	tracking := "stable"
	if releaseTrackings[a.Tracking] {
		branch, err := defaultBranch(ctx, a)
		if err != nil {
			return "", err
		}
		tracking = branch
	}
	a.Tracking = tracking
	if err := store.Save(st); err != nil {
		return "", err
	}
	return tracking, nil
}

// defaultBranch resolves the branch an addon follows in branch-tracking
// mode: the GitHub repository's default branch.
func defaultBranch(ctx context.Context, a *state.AddonState) (string, error) {
	if a.Source.Kind != state.SourceGitHub {
		return "", fmt.Errorf("branch tracking needs a GitHub source (addon %q has source %q)", a.ID, a.Source.Kind)
	}
	ref, err := sourceRepo(a)
	if err != nil {
		return "", err
	}
	res, err := newResolver()
	if err != nil {
		return "", err
	}
	repo, err := res.GH.Repository(ctx, ref.Owner, ref.Repo)
	if err != nil {
		return "", fmt.Errorf("resolve default branch of %s: %w", ref.Slug(), err)
	}
	if repo.DefaultBranch == "" {
		return "", fmt.Errorf("resolve default branch of %s: repository reports none", ref.Slug())
	}
	return repo.DefaultBranch, nil
}

// RemoveAddon deletes the addon's owned directories and drops its state
// entry, atomically and with the installer's rollback guarantees. A
// cancelled context aborts through the same rollback.
func RemoveAddon(ctx context.Context, flavor client.Flavor, id string) error {
	in, err := installerFor()
	if err != nil {
		return err
	}
	return in.Remove(ctx, flavor, id)
}
