// Package ptyrun runs a child process under a pseudo-terminal and streams its
// output. Many CLIs go quiet on a pipe and only emit rich progress/color when
// stdout is a TTY, so allocating a PTY gets us the real output to parse.
package ptyrun

import (
	"context"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

// Chunk is a piece of child output.
type Chunk struct {
	Data string
}

// Options configures a Stream run.
type Options struct {
	Env []string // full environment for the child (defaults to os.Environ)
	Dir string   // working directory, optional
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
	// coordinator can multiplex it against ctx.
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
				chunks <- Chunk{Data: s}
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
