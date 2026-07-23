// Package ptyrun runs a child process under a pseudo-terminal and streams its
// output.
//
// This is the crux of wrapping tools that have no API (brew, and any future
// CLI-wrapped backend). Many CLIs detect whether stdout is a TTY and *change
// their behavior*: with a real terminal they emit progress bars and color;
// with a pipe they go quiet. Allocating a PTY makes the tool believe it's
// interactive, so we get the rich output stream to parse.
//
// It also lets us notice the tool going idle — a heuristic that it is blocked
// on an interactive prompt — so the caller can surface it instead of hanging.
package ptyrun

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// newIdleTimer returns a reset func and a channel that fires after ms
// milliseconds of inactivity. reset must only be called from a single
// goroutine (the Stream coordinator), so draining the timer channel here is
// race-free against the coordinator's select.
func newIdleTimer(ms int) (func(), <-chan time.Time) {
	d := time.Duration(ms) * time.Millisecond
	t := time.NewTimer(d)
	reset := func() {
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
		t.Reset(d)
	}
	return reset, t.C
}

// Chunk is a piece of child output, or an idle tick.
type Chunk struct {
	Data string
	// Idle is true when no output arrived within IdleTimeout and the child is
	// still running — a likely interactive prompt.
	Idle bool
}

// Options configures a Stream run.
type Options struct {
	Env []string // full environment for the child (defaults to os.Environ)
	Dir string   // working directory, optional
	// IdleTimeout: if >0, emit an Idle chunk after this many milliseconds of
	// silence while the child is still alive.
	IdleTimeoutMS int
}

// Stream spawns argv under a PTY and returns a channel of output chunks plus an
// error channel that yields the final result (process exit) exactly once after
// the chunk channel closes.
//
// The child is killed if ctx is cancelled.
func Stream(ctx context.Context, argv []string, opts Options) (<-chan Chunk, <-chan error) {
	chunks := make(chan Chunk, 32)
	done := make(chan error, 1)

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if opts.Env != nil {
		cmd.Env = opts.Env
	} else {
		cmd.Env = os.Environ()
	}
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}

	// Stdin stays off the PTY (pinned to /dev/null below) so TTY-gated CLIs
	// like Homebrew Cask fail non-interactively instead of prompting. Setctty
	// defaults to fd 0, so the slave goes through ExtraFiles at fd 3 instead.
	ptmx, tty, err := pty.Open()
	if err != nil {
		close(chunks)
		done <- err
		return chunks, done
	}

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		_ = tty.Close()
		_ = ptmx.Close()
		close(chunks)
		done <- err
		return chunks, done
	}

	cmd.Stdin = devNull
	cmd.Stdout = tty
	cmd.Stderr = tty
	cmd.ExtraFiles = []*os.File{tty}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 3}

	err = cmd.Start()
	_ = tty.Close() // the child has its own copy; the parent only needs ptmx
	if err != nil {
		_ = devNull.Close()
		_ = ptmx.Close()
		close(chunks)
		done <- err
		return chunks, done
	}

	// Reader goroutine: pump raw PTY output onto an internal channel so the
	// coordinator can multiplex it against an idle timer and ctx.
	raw := make(chan string, 32)
	go func() {
		defer close(raw)
		buf := make([]byte, 64*1024)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				raw <- string(buf[:n])
			}
			if rerr != nil {
				return // EOF / EIO when the child exits and closes the PTY
			}
		}
	}()

	go func() {
		defer func() {
			_ = ptmx.Close()
			close(chunks)
			done <- cmd.Wait()
			_ = devNull.Close()
		}()

		var idle <-chan time.Time
		resetIdle := func() {}
		if opts.IdleTimeoutMS > 0 {
			resetIdle, idle = newIdleTimer(opts.IdleTimeoutMS)
			resetIdle()
		}

		for {
			select {
			case <-ctx.Done():
				_ = cmd.Process.Kill()
				// Drain remaining output until the reader closes.
				for range raw {
				}
				return
			case s, ok := <-raw:
				if !ok {
					return
				}
				resetIdle()
				chunks <- Chunk{Data: s}
			case <-idle:
				chunks <- Chunk{Idle: true}
				resetIdle()
			}
		}
	}()

	return chunks, done
}

// Answer writes a response (e.g. "y\n") to a child blocked on a prompt. It is
// exposed for the rare case the caller decides to auto-answer; the PTY's master
// file is the same object you'd write to. Kept here for symmetry — most flows
// pass non-interactive flags and never need it.
func Answer(w *os.File, text string) error {
	_, err := w.WriteString(text)
	return err
}
