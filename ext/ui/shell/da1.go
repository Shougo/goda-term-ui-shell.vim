package shell

import "strings"

// DA1 (primary device attributes): a program asks the terminal what it is,
// and waits for the answer. The TUI of Neovim waits for it up to 1 second
// when it exits ("nvim --remote" in $EDITOR).
var da1Queries = []string{"\x1b[c", "\x1b[0c"}

// da1Reply is a VT100 class terminal with ANSI colors (as the terminal of
// Neovim, without OSC 52).
const da1Reply = "\x1b[?61;22c"

// da1Scanner counts the DA1 queries of the output: a query can be split
// between chunks.
type da1Scanner struct {
	tail string // the end of the last chunk that can start a query
}

// count is the number of the queries in data (with the tail of the last
// chunk).
func (s *da1Scanner) count(data string) int {
	buf := s.tail + data
	n := 0
	for _, q := range da1Queries {
		n += strings.Count(buf, q)
	}
	s.tail = ""
	for k := min(len(buf), 3); k > 0 && s.tail == ""; k-- {
		end := buf[len(buf)-k:]
		for _, q := range da1Queries {
			if len(end) < len(q) && strings.HasPrefix(q, end) {
				s.tail = end
				break
			}
		}
	}
	return n
}
