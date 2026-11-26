// Package pty runs a command in a pseudo terminal: /dev/ptmx on Linux and
// macOS, ConPTY on MS-Windows (10 1809 and later), and pipes elsewhere
// (and on an older MS-Windows): what it writes, and what is written to it.
package pty

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
)

// Pty is a command running in a pseudo terminal, or the commands of a
// pipeline.
type Pty struct {
	// proc is the command (the last of a pipeline), procs all of them.
	proc  *os.Process
	procs []*os.Process
	// reaped: those waited for (Unix), as procs.
	reaped []atomic.Bool
	// wait waits for the command: its exit code (-1: killed).
	wait func() (int, error)
	// r is what the command writes (stdout and stderr), w its input.
	r io.ReadCloser
	w io.WriteCloser
	// sys is what the system keeps of the terminal (the pseudo console,
	// the writing end of a pipe).
	sys any
	// terminal: a pseudo terminal (not pipes).
	terminal bool

	once sync.Once
	done chan struct{}
	code int
}

// Options are those of Start.
type Options struct {
	Args []string
	// Env is the whole environment ("NAME=value").
	Env []string
	Dir string
	// Rows and Cols are the size of the terminal (default 24x80).
	Rows, Cols int
	// Stdin, Stdout and Stderr are files instead of the terminal (nil: the
	// terminal), where CanRedirect.  Start does not close them.
	Stdin, Stdout, Stderr *os.File
}

// ErrRedirect: Options with files where not CanRedirect.
var ErrRedirect = errors.New("redirections are not supported on this system yet")

// ErrPipe: a pipeline of several commands where not CanPipe.
var ErrPipe = errors.New("pipes are not supported on this system yet")

// Stage is a command of a pipeline.
type Stage struct {
	// Path is the executable, with its directory.
	Path string
	Args []string
	// Env is the whole environment ("NAME=value").
	Env []string
	// Stdin, Stdout and Stderr are files instead of the pipe or the
	// terminal (nil), where CanRedirect.  StartPipeline does not close them.
	Stdin, Stdout, Stderr *os.File
}

func (o Options) redirected() bool { return o.Stdin != nil || o.Stdout != nil || o.Stderr != nil }

// Start runs path (an executable, with its directory) with o.
func Start(path string, o Options) (*Pty, error) {
	return StartPipeline([]Stage{{path, o.Args, o.Env, o.Stdin, o.Stdout, o.Stderr}}, o)
}

// StartPipeline runs stages in the terminal, each the input of the next
// (where CanPipe), as a shell runs "a | b": the input of the first and the
// output of the last are the terminal, the error of each too.  Of o, Dir,
// Rows and Cols are used.  Its exit code is that of the last.
func StartPipeline(stages []Stage, o Options) (*Pty, error) {
	if len(stages) == 0 {
		return nil, errors.New("pty: no command")
	}
	if o.Rows <= 0 {
		o.Rows = 24
	}
	if o.Cols <= 0 {
		o.Cols = 80
	}
	p := &Pty{done: make(chan struct{})}
	var err error
	switch {
	case len(stages) > 1 && !CanPipe():
		return nil, ErrPipe
	case len(stages) > 1:
		err = startPipeline(p, stages, o)
	default:
		s := stages[0]
		o.Args, o.Env, o.Stdin, o.Stdout, o.Stderr = s.Args, s.Env, s.Stdin, s.Stdout, s.Stderr
		err = start(p, s.Path, o)
	}
	if err != nil {
		return nil, err
	}
	if p.procs == nil {
		p.procs = []*os.Process{p.proc}
	}
	go func() {
		p.code, _ = p.wait()
		closeTTY(p)
		close(p.done)
	}()
	return p, nil
}

// Read reads what the command wrote; io.EOF once it exited and all is
// read.
func (p *Pty) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if err != nil && isEOF(err) {
		err = io.EOF
	}
	return n, err
}

// Write types b in the command.
func (p *Pty) Write(b []byte) (int, error) { return p.w.Write(b) }

// Wait waits for the command, and returns its exit code (-1: killed).
func (p *Pty) Wait() int {
	<-p.done
	return p.code
}

// Terminal reports whether the command has a pseudo terminal (not pipes).
func (p *Pty) Terminal() bool { return p.terminal }

// Close ends the command (as a hangup of its terminal) and releases the
// terminal.
func (p *Pty) Close() error {
	p.once.Do(func() {
		hangup(p)
		select {
		case <-p.done:
		default:
			for _, proc := range p.procs {
				_ = proc.Kill()
			}
		}
		_ = p.w.Close()
		_ = p.r.Close()
	})
	return nil
}

// Pid is that of the command (the last of a pipeline).
func (p *Pty) Pid() int { return p.proc.Pid }

func isEOF(err error) bool {
	// Linux reads EIO from the terminal once the command and its children
	// are gone.
	return errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) || isEIO(err)
}

// waitCmd is the wait of a command of os/exec.
func waitCmd(cmd *exec.Cmd) func() (int, error) {
	return func() (int, error) {
		err := cmd.Wait()
		if cmd.ProcessState != nil {
			return cmd.ProcessState.ExitCode(), nil
		}
		return -1, err
	}
}

// startPipes runs the command with pipes: no colors from most commands,
// and no terminal for those that ask for one.
func startPipes(p *Pty, path string, o Options) error {
	cmd := exec.Command(path, o.Args...)
	cmd.Env, cmd.Dir = o.Env, o.Dir
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		r.Close()
		w.Close()
		return err
	}
	p.proc, p.wait = cmd.Process, waitCmd(cmd)
	p.r, p.w, p.sys = r, in, w
	return nil
}

// closePipes: the command is gone, so is the writing end of the output
// pipe (reading ends then).
func closePipes(p *Pty) {
	if f, ok := p.sys.(*os.File); ok {
		f.Close()
	}
}
