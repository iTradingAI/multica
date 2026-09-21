//go:build unix

package termhost

import (
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
)

func init() {
	// Wire the real spawner for unix platforms.
	openPtyDefault = openUnixPty
}

// openUnixPty starts the shell attached to a fresh pty. The child leads its
// own session (creack/pty handles setsid + controlling terminal), which is
// what makes Ctrl+C, job control and prompt redraws behave like a real
// terminal.
func openUnixPty(req SpawnRequest) (PtyProcess, error) {
	cmd := exec.Command(req.Shell)
	cmd.Env = shellEnv()
	if req.Cwd != "" {
		cmd.Dir = req.Cwd
	}
	file, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(req.Cols), Rows: uint16(req.Rows)})
	if err != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return nil, err
	}

	p := &unixPty{file: file, cmd: cmd, output: make(chan []byte, 8), exited: make(chan struct{})}

	// Reader: one chunk per send. Blocking on the channel is the only queue
	// here, so a slow consumer naturally blocks this read and the kernel's
	// pty buffer becomes the backpressure instead of unbounded memory.
	go func() {
		defer close(p.output)
		buf := make([]byte, 32*1024)
		for {
			n, readErr := file.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				select {
				case p.output <- chunk:
				case <-p.exited:
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	// Reaper: collect the exit code once the child is gone, then release the
	// pty file (creack/pty wants the child reaped before the master closes).
	// The exit is broadcast by closing exited, never by sending: the reader's
	// early-exit arm also receives from exited, and a send there would let it
	// consume the only code — leaving Wait() blocked forever and the exit
	// event undelivered.
	go func() {
		waitErr := cmd.Wait()
		p.mu.Lock()
		p.waitErr = waitErr
		if cmd.ProcessState != nil {
			p.code = cmd.ProcessState.ExitCode()
		}
		p.mu.Unlock()
		close(p.exited)
		// Release the master here, after the child is reaped (creack/pty
		// wants the child reaped before the master closes). Doing it in the
		// reaper — not in Wait — guarantees the fd is released on every
		// teardown path, including the ones that never call Wait.
		_ = p.file.Close()
	}()

	return p, nil
}

// unixPty wraps one creack/pty session.
type unixPty struct {
	mu      sync.Mutex
	file    *os.File
	cmd     *exec.Cmd
	output  chan []byte
	exited  chan struct{}
	waitErr error
	code    int
}

func (p *unixPty) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.file.Write(data)
}

func (p *unixPty) Resize(cols, rows int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return pty.Setsize(p.file, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

// Kill ends the child and closes the master. Closing the master also unblocks
// the reader goroutine.
func (p *unixPty) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	return p.file.Close()
}

// Wait blocks for the child's exit code. The reaper broadcasts the exit by
// closing exited and stores the code, so Wait is safe to call after the
// reader's early-exit arm has already observed the same close.
func (p *unixPty) Wait() int {
	<-p.exited
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.code
}

func (p *unixPty) Output() <-chan []byte { return p.output }
