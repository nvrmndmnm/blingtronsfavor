// Package gitops shells out to the git CLI for the two repository
// operations bfctl needs: listing remote refs and creating shallow working
// copies of a repository at one ref. The git binary is resolved before any
// work starts so a missing installation fails with a friendly error instead
// of a cryptic exec failure.
package gitops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ErrGitMissing is wrapped into errors returned when no git executable is
// found on the PATH.
var ErrGitMissing = errors.New("git executable not found in PATH")

// gitBin resolves the git executable or reports ErrGitMissing.
func gitBin() (string, error) {
	path, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("%w; install git and retry", ErrGitMissing)
	}
	return path, nil
}

// runGit runs one git invocation and returns its stdout. stderr is folded
// into the error; prompts are disabled so a misconfigured remote fails
// instead of blocking on input.
func runGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	bin, err := gitBin()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

// LsRemote lists the tag and branch refs of the remote repository at
// url. It returns the short ref names (e.g. "v1.2.3", "main") mapped to the
// commit SHAs they point at; peeled tag entries are omitted.
func LsRemote(ctx context.Context, url string) (tags, heads map[string]string, err error) {
	out, err := runGit(ctx, "", "ls-remote", "--refs", "--tags", "--heads", "--", url)
	if err != nil {
		return nil, nil, fmt.Errorf("list remote refs of %s: %w", url, err)
	}
	tags = make(map[string]string)
	heads = make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		sha, ref, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		switch {
		case strings.HasPrefix(ref, "refs/tags/"):
			tags[strings.TrimPrefix(ref, "refs/tags/")] = sha
		case strings.HasPrefix(ref, "refs/heads/"):
			heads[strings.TrimPrefix(ref, "refs/heads/")] = sha
		}
	}
	return tags, heads, nil
}

// ShallowClone clones url at ref into dest with depth 1. Branch and tag
// names use the clone fast path; commit SHAs are fetched explicitly because
// git clone cannot check out an arbitrary SHA directly. dest must not exist
// yet.
func ShallowClone(ctx context.Context, url, ref, dest string) error {
	if ref == "" {
		return errors.New("shallow clone: empty ref")
	}
	if isSHA(ref) {
		return shallowFetch(ctx, url, ref, dest)
	}
	if _, err := runGit(ctx, "", "clone", "--depth", "1", "--branch", ref, "--", url, dest); err != nil {
		return fmt.Errorf("shallow clone of %s at %s: %w", url, ref, err)
	}
	return nil
}

// shallowFetch materializes a single commit into dest via init + fetch of
// that exact SHA. Used for refs git clone --branch cannot address.
func shallowFetch(ctx context.Context, url, ref, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dest, err)
	}
	steps := [][]string{
		{"init", "--quiet", dest},
		{"-C", dest, "remote", "add", "origin", url},
		{"-C", dest, "fetch", "--quiet", "--depth", "1", "origin", ref},
		{"-C", dest, "checkout", "--quiet", "--detach", "FETCH_HEAD"},
	}
	for _, args := range steps {
		if _, err := runGit(ctx, "", args...); err != nil {
			return fmt.Errorf("shallow fetch of %s at %s: %w", url, ref, err)
		}
	}
	return nil
}

// isSHA reports whether ref looks like a hexadecimal commit SHA rather than
// a branch or tag name.
func isSHA(ref string) bool {
	if len(ref) < 7 {
		return false
	}
	for _, r := range ref {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}
