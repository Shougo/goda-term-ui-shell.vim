package pty

import (
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func sh(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("no sh")
	}
	p, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	return p
}

func readAll(t *testing.T, p *Pty) string {
	t.Helper()
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(p)
		done <- string(b)
	}()
	select {
	case s := <-done:
		return s
	case <-time.After(10 * time.Second):
		t.Fatal("timed out")
	}
	return ""
}

func TestOutputAndExit(t *testing.T) {
	p, err := Start(sh(t), Options{Args: []string{"-c", "echo hello; exit 3"}, Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if out := readAll(t, p); !strings.Contains(out, "hello") {
		t.Errorf("output %q", out)
	}
	if code := p.Wait(); code != 3 {
		t.Errorf("exit %d", code)
	}
}

func TestTerminal(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no pseudo terminal")
	}
	// A terminal of the size given, and input from it.
	p, err := Start(sh(t), Options{
		Args: []string{"-c", "test -t 0 && test -t 1 && stty size; read x; echo got $x"},
		Env:  os.Environ(), Rows: 30, Cols: 100, Dir: os.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := p.Write([]byte("abc\n")); err != nil {
		t.Fatal(err)
	}
	out := readAll(t, p)
	if !strings.Contains(out, "30 100") || !strings.Contains(out, "got abc") {
		t.Errorf("output %q", out)
	}
	if p.Wait() != 0 {
		t.Error("exit")
	}
}

func TestClose(t *testing.T) {
	p, err := Start(sh(t), Options{Args: []string{"-c", "sleep 30"}, Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	p.Close()
	p.Wait()
	if time.Since(start) > 5*time.Second {
		t.Error("not ended")
	}
}

func TestInterrupt(t *testing.T) {
	p, err := Start(sh(t), Options{Args: []string{"-c", "sleep 30"}, Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	time.Sleep(100 * time.Millisecond)
	if err := p.Interrupt(); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() { done <- p.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("not interrupted")
	}
}
