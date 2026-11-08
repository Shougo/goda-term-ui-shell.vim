//go:build linux || darwin

package pty

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"
)

func start(p *Pty, path string, o Options) error {
	master, slave, err := open()
	if err != nil {
		return err
	}
	ws := struct{ row, col, x, y uint16 }{uint16(o.Rows), uint16(o.Cols), 0, 0}
	_ = ioctl(slave.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))
	cmd := exec.Command(path, o.Args...)
	cmd.Env, cmd.Dir = o.Env, o.Dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	// Its own session, the terminal as its controlling terminal (fd 0).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		master.Close()
		slave.Close()
		return err
	}
	// The command has it now.
	slave.Close()
	p.proc, p.wait = cmd.Process, waitCmd(cmd)
	p.r, p.w, p.terminal = master, master, true
	return nil
}

func ioctl(fd, req, arg uintptr) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg); e != 0 {
		return e
	}
	return nil
}

func closeTTY(*Pty) {}

// hangup: SIGHUP to the session of the command (as when its terminal is
// gone), and SIGKILL after a moment.
func hangup(p *Pty) {
	pid := p.proc.Pid
	_ = syscall.Kill(-pid, syscall.SIGHUP)
	select {
	case <-p.done:
	case <-time.After(500 * time.Millisecond):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
}

// Interrupt sends SIGINT to the command (as <C-c> in its terminal).
func (p *Pty) Interrupt() error { return syscall.Kill(-p.proc.Pid, syscall.SIGINT) }

// Resize sets the size of the terminal.
func (p *Pty) Resize(rows, cols int) error {
	f, ok := p.r.(*os.File)
	if !ok {
		return nil
	}
	ws := struct{ row, col, x, y uint16 }{uint16(rows), uint16(cols), 0, 0}
	return ioctl(f.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))
}

func isEIO(err error) bool { return errors.Is(err, syscall.EIO) }
