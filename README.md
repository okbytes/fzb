# fzb

fzb picks one line from a list. Pipe lines into it, move to the one you want,
and press Enter: fzb prints that line. It is made for short lists such as git
branches.

There is no search and there are no options. The list opens under your prompt,
as tall as it needs to be up to the height of the terminal, marks the current
line with a green `*`, and disappears when you are done.

## Install

On Linux or macOS, Intel or ARM, this installs the latest release to
`/usr/local/bin`, asking for sudo if it needs to. Run it again to update.

```sh
curl -fsSL https://raw.githubusercontent.com/okbytes/fzb/main/install.sh | sh
```

To install somewhere else, such as `~/.local/bin`, set `FZB_DIR`:

```sh
curl -fsSL https://raw.githubusercontent.com/okbytes/fzb/main/install.sh | FZB_DIR=~/.local/bin sh
```

With Go, `go install github.com/okbytes/fzb@latest` also works.

## Use

```sh
git branch --format='%(refname:short)' | fzb
```

| Key | Action |
| --- | --- |
| `j`, Down, alt-Down, ctrl-j, ctrl-n | next line |
| `k`, Up, alt-Up, ctrl-k, ctrl-p | previous line |
| PgDn, PgUp | move a page |
| Enter | print the line and exit 0 |
| Esc, ctrl-c, ctrl-g, ctrl-q, ctrl-d | exit 130 without printing |
| ctrl-l | redraw |

Other keys do nothing. fzb exits 1 on empty input, and 2 on errors or when it
is given arguments or no piped input.

## Git branch picker

In `~/.gitconfig`, `git co` with no arguments picks a branch, most recent
first:

```gitconfig
[alias]
    co = "!f() { \
        if [ $# -gt 0 ]; then git switch \"$@\"; return $?; fi; \
        b=$(git branch --sort=-committerdate --format='%(refname:short)' | fzb); \
        if [ -n \"$b\" ]; then git switch \"$b\"; fi; \
      }; f"
```

## Releasing

Pushing a tag such as `v0.1.1` builds the binaries and publishes the release.

## Credits

fzb started as a cut-down copy of [fzf](https://github.com/junegunn/fzf) by
Junegunn Choi. MIT licensed; see [LICENSE](LICENSE).
