package shell

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	if got, err := readHistory(path); err != nil || got != nil {
		t.Errorf("none: %q %v", got, err)
	}
	for _, l := range []string{"cmd1", "cmd2", "cmd2", "cmd3"} {
		if err := appendHistory(path, l, 10); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := readHistory(path); !slices.Equal(got, []string{"cmd1", "cmd2", "cmd3"}) {
		t.Errorf("read %q", got)
	}
	// Trimmed and deduplicated over the limit.
	if err := os.WriteFile(path, []byte("a\nb\nb\nc\nd\ne\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := trimHistory(path, 3); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "c\nd\ne\n" {
		t.Errorf("trimmed %q", b)
	}
}

// A command line of several lines is one entry, written as zsh does.
func TestHistorySeveralLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	multi := "git tag -m \"v1\n\n- fix\""
	for _, l := range []string{"cmd1", multi, "cmd2"} {
		if err := appendHistory(path, l, 10); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(path); string(b) != "cmd1\ngit tag -m \"v1\\\n\\\n- fix\"\ncmd2\n" {
		t.Errorf("written %q", b)
	}
	if got, _ := readHistory(path); !slices.Equal(got, []string{"cmd1", multi, "cmd2"}) {
		t.Errorf("read %q", got)
	}
	// Trimmed by entries.
	if err := trimHistory(path, 2); err != nil {
		t.Fatal(err)
	}
	if got, _ := readHistory(path); !slices.Equal(got, []string{multi, "cmd2"}) {
		t.Errorf("trimmed %q", got)
	}
}
