//go:build linux || darwin

package pty

import (
	"errors"
	"os"
	"os/exec"
	"slices"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

func start(p *Pty, path string, o Options) error {
	return startPipeline(p, []Stage{{path, o.Args, o.Env, o.Stdin, o.Stdout, o.Stderr}}, o)
}

// CanPipe: StartPipeline may have several commands.
func CanPipe() bool { return true }

// startPipeline starts the stages, each in a session of its own: a signal
// to the group of each reaches it.  Only one of them can have the terminal
// as its controlling terminal: the first with the terminal as its 0, 1 or
// 2 (or the first, as its 3, when none has it).  The others cannot open
// /dev/tty (a sudo there cannot ask its password), and do not get SIGWINCH.
func startPipeline(p *Pty, stages []Stage, o Options) error {
	master, slave, err := open()
	if err != nil {
		return err
	}
	ws := struct{ row, col, x, y uint16 }{uint16(o.Rows), uint16(o.Cols), 0, 0}
	_ = ioctl(slave.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))
	// The commands have them now (or did not start): those of this process
	// are closed (the reading end of a pipe sees its end once its command
	// is gone).
	opened := []*os.File{slave}
	defer func() {
		for _, f := range opened {
			f.Close()
		}
	}()
	n := len(stages)
	files := make([][3]*os.File, n)
	for i := range files {
		files[i] = [3]*os.File{slave, slave, slave}
	}
	for i := range n - 1 {
		r, w, err := os.Pipe()
		if err != nil {
			master.Close()
			return err
		}
		opened = append(opened, r, w)
		files[i][1], files[i+1][0] = w, r
	}
	for i, s := range stages {
		for k, f := range [3]*os.File{s.Stdin, s.Stdout, s.Stderr} {
			if f != nil {
				files[i][k] = f
			}
		}
	}
	// The controlling terminal: through a descriptor of the terminal in its
	// command.
	owner, ctty := 0, 3
	for i := range files {
		if k := slices.Index(files[i][:], slave); k >= 0 {
			owner, ctty = i, k
			break
		}
	}
	cmds := make([]*exec.Cmd, n)
	for i, s := range stages {
		cmd := exec.Command(s.Path, s.Args...)
		cmd.Env, cmd.Dir = s.Env, o.Dir
		cmd.Stdin, cmd.Stdout, cmd.Stderr = files[i][0], files[i][1], files[i][2]
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if i == owner {
			if ctty == 3 {
				cmd.ExtraFiles = []*os.File{slave}
			}
			cmd.SysProcAttr.Setctty, cmd.SysProcAttr.Ctty = true, ctty
		}
		if err := cmd.Start(); err != nil {
			for _, c := range cmds[:i] {
				_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
				_ = c.Wait()
			}
			master.Close()
			return err
		}
		cmds[i] = cmd
		p.procs = append(p.procs, cmd.Process)
	}
	p.proc = cmds[n-1].Process
	p.reaped = make([]atomic.Bool, n)
	p.wait = func() (int, error) {
		var code int
		var err error
		for i, c := range cmds {
			code, err = waitCmd(c)()
			p.reaped[i].Store(true)
		}
		return code, err
	}
	p.r, p.w, p.terminal = master, master, true
	return nil
}

func ioctl(fd, req, arg uintptr) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg); e != 0 {
		return e
	}
	return nil
}

// CanRedirect: Options may have files.
func CanRedirect() bool { return true }

func closeTTY(*Pty) {}

// hangup: SIGHUP to the session of each command (as when its terminal is
// gone), and SIGKILL after a moment.
func hangup(p *Pty) {
	p.signal(syscall.SIGHUP)
	select {
	case <-p.done:
	case <-time.After(500 * time.Millisecond):
		p.signal(syscall.SIGKILL)
	}
}

// Interrupt sends SIGINT to the commands (as <C-c> in their terminal).
func (p *Pty) Interrupt() error { return p.signal(syscall.SIGINT) }

// signal sends sig to the group of each command not reaped yet (its pid
// may be another one's then): the first error, but of one gone.
func (p *Pty) signal(sig syscall.Signal) error {
	var first error
	for i, proc := range p.procs {
		if p.reaped[i].Load() {
			continue
		}
		if err := syscall.Kill(-proc.Pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) && first == nil {
			first = err
		}
	}
	return first
}

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
