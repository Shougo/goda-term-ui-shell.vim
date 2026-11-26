//go:build !linux && !darwin && !windows

package pty

func start(p *Pty, path string, o Options) error {
	if o.redirected() {
		return ErrRedirect
	}
	return startPipes(p, path, o)
}

// CanPipe: StartPipeline may have several commands (not yet here).
func CanPipe() bool { return false }

func startPipeline(*Pty, []Stage, Options) error { return ErrPipe }

// CanRedirect: Options may have files (not yet here).
func CanRedirect() bool { return false }

func closeTTY(p *Pty) { closePipes(p) }

func hangup(*Pty) {}

// Interrupt ends the command (there is no terminal for a signal).
func (p *Pty) Interrupt() error { return p.proc.Kill() }

func (p *Pty) Resize(int, int) error { return nil }

func isEIO(error) bool { return false }
