//go:build !linux && !darwin && !windows

package pty

func start(p *Pty, path string, o Options) error { return startPipes(p, path, o) }

func closeTTY(p *Pty) { closePipes(p) }

func hangup(*Pty) {}

// Interrupt ends the command (there is no terminal for a signal).
func (p *Pty) Interrupt() error { return p.proc.Kill() }

func (p *Pty) Resize(int, int) error { return nil }

func isEIO(error) bool { return false }
