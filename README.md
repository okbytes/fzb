# fzb

fzb is a minimal re-cut of [fzf](https://github.com/junegunn/fzf) for picking
one line from a short list, such as git branches. It reads lines from stdin,
shows them below the prompt, and prints the one you pick. There is no query,
fuzzy matching, or options: it looks and behaves like one fzf setup, with
`--no-input --reverse --height ~100% --pointer '*'` and green for the current
line.

## Install

    go install github.com/okbytes/fzb@latest

or, from a checkout, `go install .`

## Keys

| Key | Action |
| --- | --- |
| `j`, Down, alt-Down, ctrl-j, ctrl-n | next line |
| `k`, Up, alt-Up, ctrl-k, ctrl-p | previous line |
| PgDn, PgUp | move a page |
| Enter | print the line and exit 0 |
| Esc, ctrl-c, ctrl-g, ctrl-q, ctrl-d | exit 130 without printing |
| ctrl-l | redraw |

Other keys do nothing. fzb exits 1 on empty input and 2 on errors.

## Git branch picker

In `~/.gitconfig`, `git co` with no arguments picks a branch, most recent
first:

    [alias]
        co = "!f() { \
            if [ $# -gt 0 ]; then git switch \"$@\"; return $?; fi; \
            b=$(git branch --sort=-committerdate --format='%(refname:short)' | fzb); \
            if [ -n \"$b\" ]; then git switch \"$b\"; fi; \
          }; f"
