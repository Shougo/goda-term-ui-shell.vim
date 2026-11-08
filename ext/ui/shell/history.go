package shell

import (
	"os"
	"strings"
)

// The history file: a command line a line, the newest last.

// readHistory is the lines of path, without consecutive duplicates.
func readHistory(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return dedup(nonEmpty(strings.Split(string(b), "\n"))), nil
}

func nonEmpty(lines []string) []string {
	out := lines[:0]
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func dedup(lines []string) []string {
	var out []string
	for i, l := range lines {
		if i == 0 || l != lines[i-1] {
			out = append(out, l)
		}
	}
	return out
}

// appendHistory appends line to path, and trims it to limit entries.
func appendHistory(path, line string, limit int) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return trimHistory(path, limit)
}

// trimHistory keeps the last limit entries (without consecutive duplicates),
// through a temporary file, so that a crash leaves the old one.
func trimHistory(path string, limit int) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := nonEmpty(strings.Split(string(b), "\n"))
	if limit <= 0 || len(lines) <= limit {
		return nil
	}
	lines = dedup(lines[max(0, len(lines)-2*limit):])
	lines = lines[max(0, len(lines)-limit):]
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
