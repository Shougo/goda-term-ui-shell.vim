package pty

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// What is redirected goes to its file; what is not (the error here) to the
// pseudo console still.
func TestConPTYRedirect(t *testing.T) {
	if !hasConPTY() {
		t.Skip("no pseudo console")
	}
	dir := t.TempDir()
	out, err := os.Create(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	p, err := Start(cmdExe(t), Options{Args: []string{"/c", "echo to-file& echo to-console 1>&2"},
		Env: os.Environ(), Stdout: out})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	got := readAll(t, p)
	if code := p.Wait(); code != 0 {
		t.Errorf("exit %d", code)
	}
	if !strings.Contains(got, "to-console") || strings.Contains(got, "to-file") {
		t.Errorf("console %q", got)
	}
	if b, _ := os.ReadFile(out.Name()); !strings.Contains(string(b), "to-file") {
		t.Errorf("file %q", b)
	}

	// The input from a file.
	in := filepath.Join(dir, "in")
	if err := os.WriteFile(in, []byte("abc\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(in)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p, err = Start(cmdExe(t), Options{Args: []string{"/c", "set /p x=& call echo got %x%"},
		Env: os.Environ(), Stdin: f})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if got := readAll(t, p); !strings.Contains(got, "got abc") {
		t.Errorf("input %q", got)
	}
}

func TestConPTYPipeline(t *testing.T) {
	if !hasConPTY() {
		t.Skip("no pseudo console")
	}
	cmd := cmdExe(t)
	stage := func(script string) Stage {
		return Stage{Path: cmd, Args: []string{"/c", script}, Env: os.Environ()}
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
	findstr := filepath.Join(os.Getenv("SystemRoot"), "System32", "findstr.exe")
	find := func(s string) Stage { return Stage{Path: findstr, Args: []string{s}, Env: os.Environ()} }

	// The output of one, the input of the next; the exit code of the last.
	if out, code := run(stage("echo apple& echo banana"), find("ban")); code != 0 ||
		!strings.Contains(out, "banana") || strings.Contains(out, "apple") {
		t.Errorf("findstr: %q, %d", out, code)
	}
	if _, code := run(stage("echo apple"), find("cherry")); code != 1 {
		t.Errorf("no match: %d", code)
	}
	// The error of each to the pseudo console.
	if out, _ := run(stage("echo e1 1>&2"), stage("more > nul& echo e2 1>&2")); !strings.Contains(out, "e1") ||
		!strings.Contains(out, "e2") {
		t.Errorf("errors: %q", out)
	}
}

// CTRL-C reaches every command of a pipeline.
func TestConPTYPipelineInterrupt(t *testing.T) {
	if !hasConPTY() {
		t.Skip("no pseudo console")
	}
	cmd := cmdExe(t)
	stage := func(script string) Stage {
		return Stage{Path: cmd, Args: []string{"/c", script}, Env: os.Environ()}
	}
	p, err := StartPipeline([]Stage{stage("ping -n 30 127.0.0.1"), stage("more")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	go func() {
		buf := make([]byte, 1024)
		for {
			if _, err := p.Read(buf); err != nil {
				return
			}
		}
	}()
	time.Sleep(time.Second)
	if err := p.Interrupt(); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() { done <- p.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("not ended")
	}
}
