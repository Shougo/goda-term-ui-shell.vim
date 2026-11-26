package pty

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedirect(t *testing.T) {
	if !CanRedirect() {
		p, err := Start(os.Args[0], Options{Stdout: os.Stderr})
		if err == nil {
			p.Close()
		}
		if !errors.Is(err, ErrRedirect) {
			t.Errorf("Start with a file: %v, want %v", err, ErrRedirect)
		}
		return
	}
	shell := sh(t)
	dir := t.TempDir()
	create := func(name, text string) *os.File {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		return f
	}
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	run := func(script string, o Options) string {
		t.Helper()
		o.Args, o.Env = []string{"-c", script}, os.Environ()
		p, err := Start(shell, o)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		out := readAll(t, p)
		if code := p.Wait(); code != 0 {
			t.Errorf("%s: exit %d", script, code)
		}
		return out
	}

	// The output to a file, the error still to the terminal.
	out := run("echo to-file; echo to-terminal >&2", Options{Stdout: create("out", "")})
	if got := read("out"); got != "to-file\n" {
		t.Errorf("file %q", got)
	}
	if !strings.Contains(out, "to-terminal") || strings.Contains(out, "to-file") {
		t.Errorf("terminal %q", out)
	}

	// The input from a file.
	if out := run("tr a-z A-Z", Options{Stdin: create("in", "hello\n")}); !strings.Contains(out, "HELLO") {
		t.Errorf("terminal %q", out)
	}

	// All of them redirected: the terminal is still the controlling one.
	f := create("all", "")
	out = run("echo to-file; echo to-tty >/dev/tty", Options{Stdin: create("none", ""), Stdout: f, Stderr: f})
	if got := read("all"); got != "to-file\n" {
		t.Errorf("file %q", got)
	}
	if !strings.Contains(out, "to-tty") {
		t.Errorf("terminal %q", out)
	}
}
