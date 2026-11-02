# Blingtron's Favor

Blingtron's Favor (`bfctl`) is a command-line addon manager. It keeps its
configuration in `~/.config/bfctl` and its cache in `~/.cache/bfctl` (both
honour the usual `XDG_CONFIG_HOME` / `XDG_CACHE_HOME` overrides and are
created lazily with mode `0700`).

## Disclaimer – Questionable Quality Care

> [!WARNING]
> **There must always be a maintainer (and it isn't me).**

I wrote `bfctl` so I could update my addons without installing a bloated Electron adware. This repository is public strictly as a showcase, not as an offer/product.

The tool is provided strictly "as-is." To bypass launcher walls, it scrapes public web pages and resolves download links directly, which is somewhat gray area ToS-wise. If you run it, you assume full responsibility for your actions.

It works on my machine until an upstream website renames a CSS class, at which point it will remain broken until my next sesh night forces me to fix it.

## Build

Requires Go 1.27.2+ and [go-task](https://taskfile.dev) v3.

`task lint` runs the golangci-lint release pinned in `Taskfile.yml` via `go run`, so no pre-installed
linter binary is needed.

```sh
task build          # builds bin/bfctl with version metadata
task verify         # go vet + golangci-lint + go test
task release        # bin/bfctl -> dist/bfctl-<version>-<os>-<arch>.tar.gz
task --list         # show all tasks
```

## Quickstart – Arch Linux

```sh
# install (AUR-style: build and place on PATH)
git clone https://github.com/nvrmndmnm/blingtronsfavor && cd blingtronsfavor
task build            # or: go build -o bin/bfctl ./cmd/bfctl
sudo install -m755 bin/bfctl /usr/local/bin/bfctl    # sudo for path access only

# point bfctl at your World of Warcraft flavor directory
bfctl init --path "$HOME/Games/World of Warcraft/_forever_"

# manage a stable-release addon (GitHub owner/repo)
bfctl add owner/repo

# or add from Wago Addons (e.g. when the listed GitHub source 404s)
# an addon added via Wago stays maintained through Wago
bfctl add https://addons.wago.io/addons/bling-bling

# or add from CurseForge – installed through the mirror and CurseForge's 
# open CDN (an addon added this way stays maintained that way;
# the most stable channel serving your flavor is taken: release, then beta, then alpha)
bfctl add https://www.curseforge.com/wow/addons/bling-bling
bfctl sync --check-only          # what would change, without downloading
bfctl sync                       # install updates atomically
bfctl status                     # Forever 1.60.1 – addon table
bfctl tui                        # interactive manager (or just: bfctl)
```

`bfctl sync` never prompts and exits 0 (clean), 1 (interrupted / failed to
start), 2 (usage) or 4 (partial failure), so it is safe to call from the
launch script that starts the game. SIGINT rolls back the in-flight install.

### GitHub API rate limits

bfctl talks to the public GitHub API – unauthenticated that is 60 requests
per hour, which runs out quickly. Export a personal access token first and
every request is made authenticated (5000/hour): `GH_TOKEN`or `GITHUB_TOKEN`
(any classic token is enough for public repos, no scopes needed;
fine-grained tokens need read access to repos).

```sh
export GH_TOKEN="$(gh auth token)"   # if you use the gh CLI
# or: export GH_TOKEN=ghp_yourtoken
bfctl sync
```

The token is held in memory only: it is never logged and never written to
the cache or `state.json`.

## Usage

```
bfctl [global flags] <command> [flags]

Commands:
  init      create the bfctl configuration and state
  adopt     adopt an existing addon into bfctl management
  add       add an addon to the manifest
  sync      sync installed addons with the manifest
  status    show configured addons
  remove    remove an addon from the manifest
  tui       open the interactive addon manager
  version   print version information

Global flags:
  --verbose  enable debug logging
  --quiet    only log errors
```

`bfctl init` creates the XDG layout and an empty `state.json`; `bfctl status`
reports the configured addons (or "no addons configured"). `status` and
`sync` accept `--json` for machine-readable output (see `docs/bfctl.1`), and
`bfctl sync` accepts `--check-only` and `--explain`. Sync works its
addons concurrently and reports the results in report order. An update
is the matching release asset when one serves the client, and a local source
build (`.pkgmeta` externals, keyword substitution, TOC flavor split) when no
asset does or the releases are stale. A bare `bfctl` on a terminal opens the TUI; elsewhere it prints the usage
text and exits 2.

## Interactive TUI

`bfctl tui` opens a full-screen addon manager
(bubbletea/bubbles/lipgloss):

- styled header bar: flavor badge – e.g. `Forever 1.60.1` – game version,
  root and live counts (`12 addons · 3 updates available`) right,
- responsive table `Addon | Installed | Upstream | Strategy | Status` –
  columns sized from the terminal, truncated cells show their full value
  in the detail pane,
- status words with meaning: green `up to date`, yellow `update available`,
  red `failed`/`Missing`, dim `manual`/`-`; each status also carries a glyph
  (`✓ ⚠ ✗ …`) and color never carries meaning alone,
- detail pane: `enter` toggle overlay below the table,
- separate status and help bars at the bottom,
- `u` update all (spinner + live per-addon results), `a` add (inline ref
  input, validated live; asks the target flavor when several clients are
  configured), `s` toggle release/branch tracking, `d` delete (confirm),
  `/` filter, `?` full help, `q` quit.

Every action goes through the same service functions the CLI uses. `?`
opens a full help overlay listing every key by context (`?` or `esc`
closes it). Quitting mid-run never happens silently: `q` while a sync or
add is in flight asks first, stating that quitting rolls back the
in-flight install, and Ctrl-C cancels the running sync through the
installer's rollback and then offers quit; `esc` cancels an add flow. On
non-UTF-8 terminals every glyph degrades to plain ASCII (`+ ! x *`);
`bfctl tui --ascii` forces that mode, and `NO_COLOR` is honored.

## Shell completion

Completion scripts for bash and zsh live in `completions/`:

```sh
sudo install -m644 completions/bfctl.bash /etc/bash_completion.d/bfctl   # bash
sudo install -m644 completions/_bfctl /usr/share/zsh/site-functions/_bfctl  # zsh
```
a fish completion can be derived from `bfctl --help`.

## JSON output

`bfctl status --json` and `bfctl sync --json` emit stable, jq-friendly
reports (see `docs/bfctl.1` for the field contract):

```sh
bfctl status --json | jq -r '.addons[] | [.name, .status] | @tsv'
bfctl sync --json   | jq -r '.summary | "\(.updated) updated, \(.failed) failed"'
```
## License & Acknowledgements

This project is entirely free and unencumbered software released into the public domain. 

If you are a rights holder or platform representative and have questions about this repo, reach out to resolve them without drama.