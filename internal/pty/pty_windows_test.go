package pty

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func cmdExe(t *testing.T) string {
	t.Helper()
	p := os.Getenv("ComSpec")
	if p == "" {
		t.Skip("no cmd.exe")
	}
	return p
}

func TestConPTY(t *testing.T) {
	p, err := Start(cmdExe(t), Options{Args: []string{"/c", "echo hello&& exit /b 3"}, Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if hasConPTY() && !p.Terminal() {
		t.Error("no pseudo console")
	}
	if out := readAll(t, p); !strings.Contains(out, "hello") {
		t.Errorf("output %q", out)
	}
	if code := p.Wait(); code != 3 {
		t.Errorf("exit %d", code)
	}
}

func TestConPTYInput(t *testing.T) {
	// set /p reads a line of the console.
	p, err := Start(cmdExe(t), Options{Args: []string{"/c", "set /p x=&& call echo got %x%"},
		Env: os.Environ(), Dir: os.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := p.Write([]byte("abc\r\n")); err != nil {
		t.Fatal(err)
	}
	if out := readAll(t, p); !strings.Contains(out, "got abc") {
		t.Errorf("output %q", out)
	}
}

func TestConPTYClose(t *testing.T) {
	p, err := Start(cmdExe(t), Options{Args: []string{"/c", "ping -n 30 127.0.0.1"}, Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 1024)
		for {
			if _, err := p.Read(buf); err != nil {
				return
			}
		}
	}()
	time.Sleep(200 * time.Millisecond)
	p.Close()
	done := make(chan int, 1)
	go func() { done <- p.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("not ended")
	}
}

func TestEnvBlock(t *testing.T) {
	got := syscall.UTF16ToString(envBlock([]string{"b=1", "A=2", "=C:=C:\\x", "a=3"})[:])
	// The first string of the block; the rest after its NUL.
	if got != "=C:=C:\\x" {
		t.Errorf("first %q", got)
	}
	b := envBlock([]string{"b=1", "A=2", "a=3"})
	var all []string
	start := 0
	for i, c := range b {
		if c == 0 {
			if i == start {
				break
			}
			all = append(all, syscall.UTF16ToString(b[start:i]))
			start = i + 1
		}
	}
	if strings.Join(all, " ") != "a=3 b=1" {
		t.Errorf("block %q", all)
	}
}
