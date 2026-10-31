//go:build linux

package cli

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// ptyPair is one script(1)-style pseudo terminal. The renderer writes to
// the slave exactly as production writes to os.Stderr (and os.Stdout) on
// a terminal — line discipline, ONLCR and window size included — and the
// test reads the display stream from the master: the same bytes a
// terminal emulator would receive. The window size is set like a real
// terminal's, so the live block's width handling is exercised for real.
type ptyPair struct {
	master int      // raw fd; drain reads the display stream
	slave  *os.File // the renderer's terminal stream (like os.Stderr)
	out    *os.File // a second handle on the same terminal (like os.Stdout)
}

// openPTY opens a PTY pair sized cols x rows. The handles close with the
// test.
func openPTY(t *testing.T, cols, rows int) *ptyPair {
	t.Helper()
	mfd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open /dev/ptmx: %v", err)
	}
	p := &ptyPair{master: mfd}
	t.Cleanup(func() { _ = unix.Close(mfd) })
	if err := unix.IoctlSetInt(mfd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatalf("unlock pty: %v", err)
	}
	n, err := unix.IoctlGetInt(mfd, unix.TIOCGPTN)
	if err != nil {
		t.Fatalf("pty number: %v", err)
	}
	slavePath := fmt.Sprintf("/dev/pts/%d", n)
	slave, err := os.OpenFile(slavePath, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", slavePath, err)
	}
	p.slave = slave
	t.Cleanup(func() { _ = slave.Close() })
	out, err := os.OpenFile(slavePath, os.O_WRONLY|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open %s for output: %v", slavePath, err)
	}
	p.out = out
	t.Cleanup(func() { _ = out.Close() })
	ws := &unix.Winsize{Col: uint16(cols), Row: uint16(rows)}
	if err := unix.IoctlSetWinsize(mfd, unix.TIOCSWINSZ, ws); err != nil {
		t.Fatalf("set window size: %v", err)
	}
	if err := unix.SetNonblock(mfd, true); err != nil {
		t.Fatalf("set master non-blocking: %v", err)
	}
	return p
}

// drain reads the display bytes written so far. The renderer's writes are
// synchronous, so draining after one step yields exactly that step's
// output — no sleeps, no timing in the assertions.
func (p *ptyPair) drain(t *testing.T) []byte {
	t.Helper()
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, err := unix.Read(p.master, buf)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if err != nil {
			if errors.Is(err, unix.EAGAIN) {
				return out
			}
			t.Fatalf("read pty master: %v", err)
		}
		if n == 0 {
			return out
		}
	}
}
