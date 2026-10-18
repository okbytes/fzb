// Command fzb picks one line of its standard input on the terminal and prints
// it. It is a minimal re-cut of fzf that hardcodes what one configuration of
//
//	fzf --no-input --bind j:down,k:up --height ~100% --reverse --pointer '*' ...
//
// draws and does: no query, no fuzzy matching, no options.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rivo/uniseg"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const (
	escDelay   = 100 * time.Millisecond // how long ESC waits to become part of a key
	cprTimeout = 500 * time.Millisecond // how long to wait for the cursor position
	tabStop    = 8
)

// The user's fzf colors, written the way fzf writes them.
const (
	sgrReset   = "\x1b[0m"
	sgrPointer = "\x1b[;1;32m" // pointer:green, bold because fzf's --bold is on
	sgrCurrent = "\x1b[;32m"   // fg+:green:regular
	sgrNormal  = "\x1b[;37m"   // fg:7
)

func main() {
	if len(os.Args) > 1 || term.IsTerminal(0) {
		fmt.Fprintln(os.Stderr, "usage: <command> | fzb")
		os.Exit(2)
	}
	os.Exit(run())
}

func run() int {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fail(err)
	}
	if len(data) == 0 {
		return 1
	}
	// Like fzf on Unix, split on '\n' only: a '\r' stays part of its line.
	items := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	choice, code, err := pick(items)
	if err != nil {
		return fail(err)
	}
	if code == 0 {
		// fzf prints the line as it decoded it, with U+FFFD for invalid UTF-8
		fmt.Println(string([]rune(choice)))
	}
	return code
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "fzb:", err)
	return 2
}

// pick lets the user choose an item on /dev/tty. It returns the item and exit
// code 0, or exit code 130 when aborted, after restoring the terminal.
func pick(items []string) (string, int, error) {
	// The tty stays open until exit: on macOS, closing it would wait for the
	// read that readKeys is blocked in.
	t, err := openTTY()
	if err != nil {
		return "", 0, err
	}
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, unix.SIGINT, unix.SIGTERM, unix.SIGHUP, unix.SIGQUIT, unix.SIGWINCH)
	if err := t.makeRaw(); err != nil {
		return "", 0, err
	}
	rows, cols := t.size()
	h := min(len(items), rows)
	p := &picker{tty: t, items: items, h0: h, h: h, width: cols}
	defer p.close() // also when panicking

	col, typed := t.cursorColumn()
	p.start(col)
	keys := make(chan input)
	go t.readKeys(typed, keys)
	for {
		select {
		case sig := <-sigs:
			if sig != unix.SIGWINCH {
				return "", 130, nil
			}
			p.redraw()
		case in := <-keys:
			if in.err != nil {
				return "", 0, in.err
			}
			for b := in.keys; len(b) > 0; {
				a, n := nextKey(b)
				b = b[n:]
				switch a {
				case accept:
					return items[p.cy], 0, nil
				case abort:
					return "", 130, nil
				case redraw:
					p.redraw()
				default:
					p.move(a)
				}
			}
			p.draw(false)
			p.flush()
		}
	}
}

// picker shows the items in a region of h rows starting on the line the
// cursor was on. Like fzf, it addresses rows only relative to the terminal
// cursor, whose row it tracks.
type picker struct {
	tty     *tty
	items   []string
	cy, off int // the current item, and the item on the region's first row
	h0, h   int // region height at start and now; a resize can only lower it
	width   int // terminal columns
	col     int // column the cursor started in; if > 0 the region starts a line below
	y       int // terminal cursor row relative to the region's first row
	started bool
	drawn   []row           // what each region row shows
	buf     strings.Builder // the frame being built
}

type row struct {
	item    int
	current bool
}

// start makes room for the region below the cursor, scrolling the screen if
// needed, and draws it.
func (p *picker) start(col int) {
	p.col = col
	if col > 0 {
		p.buf.WriteString("\n\x1b[G") // leave the line the cursor is on alone
	}
	p.buf.WriteString("\x1b[J")
	for range p.h - 1 {
		p.buf.WriteString("\n\x1b[G")
	}
	// fzf does this even when h is 1, but CUU 0 moves up a row, so with a
	// single item fzf draws over the line above and erases it.
	if p.h > 1 {
		fmt.Fprintf(&p.buf, "\x1b[%dA", p.h-1)
	}
	p.started = true
	p.draw(true)
	p.flush()
}

// redraw clears the region and draws it again at the terminal's current
// size, after a resize or ctrl-l.
func (p *picker) redraw() {
	rows, cols := p.tty.size()
	p.moveTo(0)
	p.buf.WriteString("\x1b[J")
	p.h, p.width = min(rows, p.h0), cols
	// Like fzf, go down to the region's last row and back. CUD stops at the
	// bottom of the screen, so a region that a shrink left running past it
	// moves up to end on the last row.
	if p.h > 1 {
		fmt.Fprintf(&p.buf, "\x1b[%dB\x1b[%dA", p.h-1, p.h-1)
	}
	p.draw(true)
	p.flush()
}

// close erases the region, puts the cursor back where fzb started, and
// restores the terminal.
func (p *picker) close() {
	if p.started {
		p.moveTo(0)
		p.buf.WriteString("\x1b[J")
		if p.col > 0 {
			fmt.Fprintf(&p.buf, "\x1b[A\x1b[%dG", p.col+1)
		}
	}
	p.buf.WriteString("\x1b[?25h")
	p.flush()
	p.tty.restore()
}

func (p *picker) move(a action) {
	page := max(1, p.h-1)
	switch a {
	case up:
		p.cy--
	case down:
		p.cy++
	case pageUp:
		p.cy -= page
	case pageDown:
		p.cy += page
	}
	p.cy = clamp(p.cy, 0, len(p.items)-1)
}

// scroll keeps the cursor in view like fzf: it gives the cursor up to 3 rows
// of context above, then below, so below wins when both cannot have it.
func (p *picker) scroll() {
	n, h := len(p.items), p.h
	minOff := max(p.cy-h+1, 0)
	maxOff := max(min(n-h, p.cy), 0)
	p.off = clamp(p.off, minOff, maxOff)
	context := min(h/2, 3)
	above := func() int { return p.cy - p.off }
	below := func() int { return h - above() - 1 }
	for p.off > minOff && above() < context && below() >= context {
		p.off--
	}
	for p.off < maxOff && below() < context && above() >= context {
		p.off++
	}
}

// draw updates the region, rewriting only the rows that change unless all is
// set. Like fzf, it leaves the cursor on the last row it wrote, which decides
// what a terminal pushes into its scrollback when it shrinks.
func (p *picker) draw(all bool) {
	p.scroll()
	if len(p.drawn) != p.h {
		p.drawn = make([]row, p.h)
		all = true
	}
	for r := range p.h {
		want := row{p.off + r, p.off+r == p.cy}
		if all || p.drawn[r] != want {
			p.drawn[r] = want
			p.moveTo(r)
			p.writeRow(want)
		}
	}
}

func (p *picker) moveTo(r int) {
	if r > p.y {
		fmt.Fprintf(&p.buf, "\x1b[%dB", r-p.y)
	} else if r < p.y {
		fmt.Fprintf(&p.buf, "\x1b[%dA", p.y-r)
	}
	p.buf.WriteByte('\r')
	p.y = r
}

// writeRow writes an item's row: the pointer or a blank gutter, a blank
// marker column (fzf keeps it without --multi too), then the text.
func (p *picker) writeRow(r row) {
	sgr := sgrNormal
	if r.current {
		sgr = sgrCurrent
		p.buf.WriteString(sgrPointer + "*" + sgrNormal + " " + sgr)
	} else {
		p.buf.WriteString(sgrReset + "  " + sgr)
	}
	limit := p.width - 2
	text, width := fit(p.items[r.item], limit)
	writeText(&p.buf, text, sgr)
	p.buf.WriteString(sgrReset)
	// At the last column the cursor stays on the text, which EL would erase
	if width < limit {
		p.buf.WriteString("\x1b[K")
	}
}

// flush writes the frame in one go, with autowrap off so that the last column
// can be written, and as a synchronized update.
func (p *picker) flush() {
	if p.buf.Len() > 0 {
		p.tty.write("\x1b[?2026h\x1b[?7l\x1b[?25l" + p.buf.String() + "\x1b[?7h\x1b[?2026l")
		p.buf.Reset()
	}
}

// fit lays out s for a column of the given width like fzf: tabs become spaces
// up to the next multiple of 8, and a line that does not fit is cut after the
// last whole grapheme cluster that leaves room for a ".." ellipsis. It
// returns the text and its width.
func fit(s string, width int) (string, int) {
	ew := clamp(width, 0, 2)
	var b strings.Builder
	w, cut, cutWidth := 0, -1, 0
	state := -1
	for s != "" {
		var g string
		var gw int
		g, s, gw, state = uniseg.FirstGraphemeClusterInString(s, state)
		if g == "\t" {
			gw = tabStop - w%tabStop
			g = strings.Repeat(" ", gw)
		} else {
			gw += strings.Count(g, "\r") // shown as '␍'
		}
		if cut < 0 && w+gw > width-ew {
			cut, cutWidth = b.Len(), w
		}
		if w += gw; w > width {
			return b.String()[:cut] + strings.Repeat("·", ew), cutWidth + ew
		}
		b.WriteString(g)
	}
	return b.String(), w
}

// writeText writes s in the given color the way fzf's renderer filters text:
// control characters are dropped, ESC included, so an escape sequence shows
// as text; '\r' shows as a dim '␍'; and U+FFFD, which also stands in for
// invalid UTF-8, is dropped.
func writeText(b *strings.Builder, s, sgr string) {
	for _, r := range s {
		switch {
		case r == '\r':
			b.WriteString("\x1b[2m␍" + sgr)
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f, r == utf8.RuneError:
			// dropped
		default:
			b.WriteRune(r)
		}
	}
}

func clamp(v, lo, hi int) int {
	return max(min(v, hi), lo)
}

type action int

const (
	none action = iota
	up
	down
	pageUp
	pageDown
	accept
	abort
	redraw
)

// The escape sequences fzb binds, by what follows ESC [ or ESC O, which fzf
// treats alike. Modifier 1 is none, and 3, 9 and 11 are alt, which fzf binds
// to the same action.
var escKeys = map[string]action{
	"A": up, "1;1A": up, "1;3A": up, "1;9A": up, "1;11A": up,
	"B": down, "1;1B": down, "1;3B": down, "1;9B": down, "1;11B": down,
	"5~": pageUp, "6~": pageDown,
}

// nextKey returns what the key at the start of b does, following fzf's
// default bindings plus j and k, and how many bytes it takes.
func nextKey(b []byte) (action, int) {
	switch b[0] {
	case 0x1b:
		return escKey(b)
	case 'j', 'j' & 0x1f, 'n' & 0x1f: // j, ctrl-j, ctrl-n
		return down, 1
	case 'k', 'k' & 0x1f, 'p' & 0x1f: // k, ctrl-k, ctrl-p
		return up, 1
	case '\r': // enter, ctrl-m
		return accept, 1
	case 'c' & 0x1f, 'g' & 0x1f, 'q' & 0x1f, 'd' & 0x1f:
		return abort, 1
	case 'l' & 0x1f:
		return redraw, 1
	}
	_, n := utf8.DecodeRune(b)
	return none, n
}

// escKey handles a key that starts with ESC. ESC alone, or doubled, aborts.
// Otherwise it starts an alt key or a CSI or SS3 sequence. Sequences fzb does
// not bind are dropped whole, except that, as in fzf, one of 4 bytes or fewer
// could be alt-[ or alt-O followed by typed keys.
func escKey(b []byte) (action, int) {
	alt := 0
	if len(b) > 2 && b[1] == 0x1b {
		b, alt = b[1:], 1 // ESC ESC x is alt + ESC x
	}
	switch {
	case len(b) == 1:
		return abort, 1
	case b[1] == 0x1b:
		return abort, alt + 2
	case b[1] != '[' && b[1] != 'O':
		_, n := utf8.DecodeRune(b[1:])
		return none, alt + 1 + n
	}
	end := seqEnd(b)
	if end <= 0 {
		return none, alt + 2 // malformed, or cut short
	}
	if a, ok := escKeys[string(b[2:end])]; ok {
		return a, alt + end
	}
	if end <= 4 {
		return none, alt + 2
	}
	return none, alt + end
}

// seqEnd returns the length of the CSI or SS3 sequence at the start of b, 0
// if its final byte has not arrived, or -1 if it is malformed.
func seqEnd(b []byte) int {
	digits := true
	for i := 2; i < len(b); i++ {
		c := b[i]
		// rxvt ends keys with '$' after digits, as in \e[7$ for shift-home
		if c >= 0x40 && c <= 0x7e || c == '$' && digits && i > 2 {
			return i + 1
		}
		if c < 0x20 || c > 0x3f {
			return -1
		}
		digits = digits && c >= '0' && c <= '9'
	}
	return 0
}

// incomplete reports whether b ends in ESC or in a CSI or SS3 sequence still
// missing its final byte.
func incomplete(b []byte) bool {
	i := bytes.LastIndexByte(b, 0x1b)
	if i < 0 {
		return false
	}
	return i == len(b)-1 || (b[i+1] == '[' || b[i+1] == 'O') && seqEnd(b[i:]) == 0
}

type tty struct {
	fd   int
	orig *term.State
}

type input struct {
	keys []byte
	err  error
}

func openTTY() (*tty, error) {
	fd, err := unix.Open("/dev/tty", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/tty: %w", err)
	}
	return &tty{fd: fd}, nil
}

// makeRaw puts the terminal in raw mode like cfmakeraw, at once and without
// flushing, so that keys typed while stdin was being read still count.
func (t *tty) makeRaw() error {
	orig, err := term.MakeRaw(t.fd)
	if err != nil {
		return fmt.Errorf("/dev/tty: %w", err)
	}
	t.orig = orig
	return nil
}

func (t *tty) restore() {
	if t.orig != nil {
		term.Restore(t.fd, t.orig)
	}
}

func (t *tty) size() (rows, cols int) {
	cols, rows, err := term.GetSize(t.fd)
	if err != nil || rows == 0 || cols == 0 {
		return 24, 80
	}
	return rows, cols
}

func (t *tty) write(s string) {
	b := []byte(s)
	for len(b) > 0 {
		n, err := unix.Write(t.fd, b)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return
		}
		b = b[n:]
	}
}

func (t *tty) read(b []byte) ([]byte, error) {
	var buf [1024]byte
	for {
		n, err := unix.Read(t.fd, buf[:])
		switch {
		case err == unix.EINTR:
			continue
		case err != nil:
			return b, fmt.Errorf("read /dev/tty: %w", err)
		case n == 0:
			return b, errors.New("read /dev/tty: end of input")
		}
		return append(b, buf[:n]...), nil
	}
}

// readable waits up to d for input. select works on terminals on macOS,
// where poll does not. Like fzf, it gives up at once on an fd too high for
// select.
func (t *tty) readable(d time.Duration) bool {
	if t.fd >= len(unix.FdSet{}.Bits)*unix.NFDBITS {
		return false
	}
	deadline := time.Now().Add(d)
	for {
		tv := unix.NsecToTimeval(max(time.Until(deadline), 0).Nanoseconds())
		var fds unix.FdSet
		fds.Set(t.fd)
		n, err := unix.Select(t.fd+1, &fds, nil, nil, &tv)
		if err != unix.EINTR {
			return err == nil && n > 0
		}
	}
}

var cursorReport = regexp.MustCompile(`\x1b\[\d+;(\d+)R`)

// cursorColumn hides the cursor and asks where it is. It returns the 0-based
// column, or 0 without a reply, and whatever keys were typed meanwhile.
func (t *tty) cursorColumn() (int, []byte) {
	t.write("\x1b[?25l\x1b[6n")
	deadline := time.Now().Add(cprTimeout)
	var buf []byte
	for t.readable(time.Until(deadline)) {
		var err error
		if buf, err = t.read(buf); err != nil {
			break
		}
		if m := cursorReport.FindSubmatchIndex(buf); m != nil {
			col, _ := strconv.Atoi(string(buf[m[2]:m[3]]))
			return col - 1, append(buf[:m[0]:m[0]], buf[m[1]:]...)
		}
	}
	return 0, buf
}

// readKeys sends keys as they arrive, starting with typed ones. A chunk that
// ends in ESC or in an unfinished escape sequence waits up to escDelay for
// the rest, so that ESC alone can be told from the start of a key.
func (t *tty) readKeys(typed []byte, out chan<- input) {
	for b := typed; ; b = nil {
		var err error
		if len(b) == 0 {
			b, err = t.read(nil)
		}
		for err == nil && incomplete(b) && t.readable(escDelay) {
			b, err = t.read(b)
		}
		out <- input{b, err}
		if err != nil {
			return
		}
	}
}
