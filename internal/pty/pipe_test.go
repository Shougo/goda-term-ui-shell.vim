package pty

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPipeline(t *testing.T) {
	if !CanPipe() {
		p, err := StartPipeline([]Stage{{Path: os.Args[0]}, {Path: os.Args[0]}}, Options{})
		if err == nil {
			p.Close()
		}
		if !errors.Is(err, ErrPipe) {
			t.Errorf("StartPipeline: %v, want %v", err, ErrPipe)
		}
		return
	}
	shell := sh(t)
	stage := func(script string) Stage {
		return Stage{Path: shell, Args: []string{"-c", script}, Env: os.Environ()}
	}
	run := func(stages ...Stage) (string, int) {
		t.Helper()
		p, err := StartPipeline(stages, Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		out := readAll(t, p)
		return out, p.Wait()
	}

	// The output of one, the input of the next.
	if out, code := run(stage(`printf 'b\na\n'`), stage("sort"), stage("tr a-z A-Z")); code != 0 ||
		strings.ReplaceAll(out, "\r", "") != "A\nB\n" {
		t.Errorf("sort: %q, %d", out, code)
	}
	// The exit code is that of the last.
	if _, code := run(stage("true"), stage("exit 3")); code != 3 {
		t.Errorf("last: %d", code)
	}
	if _, code := run(stage("exit 3"), stage("true")); code != 0 {
		t.Errorf("first: %d", code)
	}
	// The error of each goes to the terminal.
	if out, _ := run(stage("echo e1 >&2"), stage("cat >/dev/null; echo e2 >&2")); !strings.Contains(out, "e1") ||
		!strings.Contains(out, "e2") {
		t.Errorf("errors: %q", out)
	}
	// One that ends before the others: the one writing to it ends too
	// (SIGPIPE; this process keeps no end of the pipe).
	if out, code := run(stage("yes"), stage("head -n 1")); code != 0 || strings.TrimSpace(out) != "y" {
		t.Errorf("yes | head: %q, %d", out, code)
	}
	// A redirection takes the place of the pipe.
	file := filepath.Join(t.TempDir(), "out")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s := stage("echo to-file")
	s.Stdout = f
	if out, _ := run(s, stage("cat; echo end")); strings.Contains(out, "to-file") || !strings.Contains(out, "end") {
		t.Errorf("redirected: %q", out)
	}
	if b, _ := os.ReadFile(file); string(b) != "to-file\n" {
		t.Errorf("file %q", b)
	}
	// The first has the terminal as its controlling terminal; the others
	// cannot open /dev/tty.
	out, _ := run(stage("echo first >/dev/tty"),
		stage("cat >/dev/null; (echo second >/dev/tty) 2>/dev/null || echo no-tty >&2"))
	if !strings.Contains(out, "first") || !strings.Contains(out, "no-tty") {
		t.Errorf("controlling terminal: %q", out)
	}
}

// Interrupt and Close reach every command.
func TestPipelineSignals(t *testing.T) {
	if !CanPipe() {
		t.Skip("no pipes")
	}
	shell := sh(t)
	stage := func(script string) Stage {
		return Stage{Path: shell, Args: []string{"-c", script}, Env: os.Environ()}
	}
	for _, stop := range []string{"interrupt", "close"} {
		p, err := StartPipeline([]Stage{stage("exec sleep 30"), stage("exec sleep 30"), stage("exec sleep 30")}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
		if stop == "interrupt" {
			if err := p.Interrupt(); err != nil {
				t.Errorf("Interrupt: %v", err)
			}
		} else {
			p.Close()
		}
		done := make(chan int, 1)
		go func() { done <- p.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("%s: the commands still run", stop)
		}
		p.Close()
	}
}
