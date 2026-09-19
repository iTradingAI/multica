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

	p := &unixPty{file: file, cmd: cmd, output: make(chan []byte, 8), exited: make(chan int, 1)}

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
	go func() {
		waitErr := cmd.Wait()
		p.mu.Lock()
		p.waitErr = waitErr
		p.mu.Unlock()
		code := 0
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		p.exited <- code
	}()

	return p, nil
}

// unixPty wraps one creack/pty session.
type unixPty struct {
	mu      sync.Mutex
	file    *os.File
	cmd     *exec.Cmd
	output  chan []byte
	exited  chan int
	waitErr error
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

// Wait blocks for the child's exit code.
func (p *unixPty) Wait() int {
	code := <-p.exited
	_ = p.file.Close()
	return code
}

func (p *unixPty) Output() <-chan []byte { return p.output }
