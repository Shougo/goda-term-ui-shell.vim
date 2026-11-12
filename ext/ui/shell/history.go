package shell

import (
	"os"
	"strings"
)

// The history file: a command line a line, the newest last. A command line
// of several lines has a backslash at the end of each but the last, as zsh
// writes it.

// readHistory is the command lines of path, without consecutive duplicates.
func readHistory(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return dedup(nonEmpty(entries(string(b)))), nil
}

// entries are the command lines of the history data.
func entries(data string) []string {
	var out []string
	var cur []string
	for _, l := range strings.Split(data, "\n") {
		if strings.HasSuffix(l, "\\") {
			cur = append(cur, strings.TrimSuffix(l, "\\"))
			continue
		}
		out = append(out, strings.Join(append(cur, l), "\n"))
		cur = nil
	}
	if cur != nil {
		out = append(out, strings.Join(cur, "\n"))
	}
	return out
}

// escape is the command line as the history has it.
func escape(line string) string { return strings.ReplaceAll(line, "\n", "\\\n") }

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
	if _, err := f.WriteString(escape(line) + "\n"); err != nil {
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
	lines := nonEmpty(entries(string(b)))
	if limit <= 0 || len(lines) <= limit {
		return nil
	}
	lines = dedup(lines[max(0, len(lines)-2*limit):])
	lines = lines[max(0, len(lines)-limit):]
	var data strings.Builder
	for _, l := range lines {
		data.WriteString(escape(l) + "\n")
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(data.String()), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
