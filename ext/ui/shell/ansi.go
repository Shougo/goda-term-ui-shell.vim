package shell

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The output of a command, as lines with the highlights of their ANSI
// colors (SGR) and attributes. It is not a terminal: a line can be
// overwritten (\r, CSI G, CSI 2K, CSI A: a progress bar), and the rest of
// the escape sequences are dropped (full-screen programs are for the UI
// terminal).

// attr is what a span highlights: "fg", "bg" (with a color 0-15), "bold",
// "italic", "underline".
type span struct {
	col, length int // in bytes, col from 1
	attr        string
	color       int
}

type outLine struct {
	text  string
	spans []span
}

type style struct {
	fg, bg                  int // -1: none
	bold, italic, underline bool
}

var plain = style{fg: -1, bg: -1}

// renderer makes the lines of the output given in chunks.
type renderer struct {
	st    style
	lines []outLine
	// pending is the line not ended yet, shown meanwhile as the last line
	// (partial), with the style before it (pendingSt).
	pending   string
	partial   bool
	pendingSt style
	// dirty is the first line changed since the last take.
	dirty int
}

func newRenderer() *renderer { return &renderer{st: plain} }

func (r *renderer) touch(i int) {
	if i < r.dirty {
		r.dirty = i
	}
}

// write adds the chunk data.
func (r *renderer) write(data string) {
	if r.partial {
		r.lines = r.lines[:len(r.lines)-1]
		r.touch(len(r.lines))
		r.st = r.pendingSt
		r.partial = false
	}
	data = r.pending + data
	parts := strings.Split(data, "\n")
	r.pending = parts[len(parts)-1]
	for _, raw := range parts[:len(parts)-1] {
		r.add(raw)
	}
	if r.pending != "" {
		r.pendingSt = r.st
		r.add(r.pending)
		r.partial = true
	}
}

// add renders one line.
func (r *renderer) add(raw string) {
	l, up := r.render(strings.TrimRight(raw, "\r"))
	n := len(r.lines)
	switch {
	case up > 0 && n > 0:
		i := max(n-up, 0)
		r.lines = r.lines[:i+1]
		r.lines[i] = l
		r.touch(i)
	case n > 0 && isProgressOverwriteLine(r.lines[n-1].text, l.text):
		r.lines[n-1] = l
		r.touch(n - 1)
	default:
		r.lines = append(r.lines, l)
		r.touch(n)
	}
}

// take is the lines changed since the last take: from the index first.
func (r *renderer) take() (first int, lines []outLine) {
	first = r.dirty
	if first < len(r.lines) {
		lines = r.lines[first:]
	}
	r.dirty = len(r.lines)
	return first, lines
}

// render is the line of raw, and how many lines up it overwrites (CSI A).
func (r *renderer) render(raw string) (outLine, int) {
	var text strings.Builder
	var spans []span
	up := 0
	reset := func() {
		text.Reset()
		spans = nil
	}
	addText := func(s string) {
		if s == "" {
			return
		}
		col := text.Len() + 1
		text.WriteString(s)
		add := func(attr string, color int) {
			spans = append(spans, span{col, len(s), attr, color})
		}
		if r.st.bg >= 0 {
			add("bg", r.st.bg)
		}
		if r.st.fg >= 0 {
			add("fg", r.st.fg)
		}
		if r.st.bold {
			add("bold", 0)
		}
		if r.st.italic {
			add("italic", 0)
		}
		if r.st.underline {
			add("underline", 0)
		}
	}
	start := 0
	for i := 0; i < len(raw); {
		c := raw[i]
		if c >= 0x20 && c != 0x7f || c == '\t' {
			i++
			continue
		}
		addText(raw[start:i])
		switch c {
		case '\x1b':
			i = r.escape(raw, i, func(final byte, params []int) {
				switch final {
				case 'G':
					if len(params) == 0 || params[0] <= 1 {
						reset()
					}
				case 'K':
					if len(params) > 0 && params[0] == 2 {
						reset()
					}
				case 'A':
					up = 1
					if len(params) > 0 && params[0] > 1 {
						up = params[0]
					}
					reset()
				case 'J':
					reset()
				}
			})
		case '\r':
			reset()
			i++
		case '\b':
			s := text.String()
			if _, size := utf8.DecodeLastRuneInString(s); size > 0 {
				s = s[:len(s)-size]
				text.Reset()
				text.WriteString(s)
				spans = clip(spans, len(s))
			}
			i++
		default:
			i++
		}
		start = i
	}
	addText(raw[start:])
	return outLine{text.String(), spans}, up
}

// clip is spans within the first n bytes.
func clip(spans []span, n int) []span {
	out := spans[:0]
	for _, s := range spans {
		if s.col > n {
			continue
		}
		s.length = min(s.length, n-s.col+1)
		out = append(out, s)
	}
	return out
}

// escape handles the escape sequence at raw[i] (ESC), and returns the
// index after it; f gets the final byte and the params of a CSI other than
// SGR.
func (r *renderer) escape(raw string, i int, f func(byte, []int)) int {
	if i+1 >= len(raw) {
		return len(raw)
	}
	switch raw[i+1] {
	case '[':
		j := i + 2
		for j < len(raw) && (raw[j] < 0x40 || raw[j] > 0x7e) {
			j++
		}
		if j >= len(raw) {
			return len(raw)
		}
		body := raw[i+2 : j]
		private := strings.ContainsAny(body, "?<=>")
		params := parseParams(strings.TrimLeft(body, "?<=>"))
		if raw[j] == 'm' && !private {
			r.sgr(params)
		} else if !private {
			f(raw[j], params)
		}
		return j + 1
	case ']', 'P', '_', '^':
		// OSC, DCS, ...: up to BEL or ST.
		for j := i + 2; j < len(raw); j++ {
			if raw[j] == '\a' {
				return j + 1
			}
			if raw[j] == '\x1b' && j+1 < len(raw) && raw[j+1] == '\\' {
				return j + 2
			}
		}
		return len(raw)
	case '(', ')', '*', '+':
		return min(i+3, len(raw))
	}
	return i + 2
}

func parseParams(s string) []int {
	if s == "" {
		return nil
	}
	var out []int
	for p := range strings.SplitSeq(s, ";") {
		n, _ := strconv.Atoi(strings.SplitN(p, ":", 2)[0])
		out = append(out, n)
	}
	return out
}

func (r *renderer) sgr(params []int) {
	if len(params) == 0 {
		params = []int{0}
	}
	for i := 0; i < len(params); i++ {
		switch p := params[i]; {
		case p == 0:
			r.st = plain
		case p == 1:
			r.st.bold = true
		case p == 3:
			r.st.italic = true
		case p == 4:
			r.st.underline = true
		case p == 22:
			r.st.bold = false
		case p == 23:
			r.st.italic = false
		case p == 24:
			r.st.underline = false
		case 30 <= p && p <= 37:
			r.st.fg = p - 30
		case 90 <= p && p <= 97:
			r.st.fg = p - 90 + 8
		case p == 39:
			r.st.fg = -1
		case 40 <= p && p <= 47:
			r.st.bg = p - 40
		case 100 <= p && p <= 107:
			r.st.bg = p - 100 + 8
		case p == 49:
			r.st.bg = -1
		case p == 38 || p == 48:
			// 256 colors (5;n) and true colors (2;r;g;b): only the 16 of
			// the highlights.
			color := -1
			if i+2 < len(params) && params[i+1] == 5 {
				if n := params[i+2]; n < 16 {
					color = n
				}
				i += 2
			} else if i+4 < len(params) && params[i+1] == 2 {
				i += 4
			}
			if p == 38 {
				r.st.fg = color
			} else {
				r.st.bg = color
			}
		}
	}
}

var (
	progressWords = regexp.MustCompile(`(?i)\b(progress|downloading|uploading|processing|writing|receiving|resolving|enumerating|counting|compressing|fetching|step|chunk|copying|syncing|building|installing|testing|verifying|extracting|transferring|loading|saving|updating)\b`)
	percent       = regexp.MustCompile(`\d+%`)
	fraction      = regexp.MustCompile(`\d+/\d+`)
	ofCount       = regexp.MustCompile(`(?i)\d+ of \d+`)
	digitOrPct    = regexp.MustCompile(`[\d%]`)
	normPercent   = regexp.MustCompile(`\b\d+%`)
	normFraction  = regexp.MustCompile(`\b\d+/\d+\b`)
	normOf        = regexp.MustCompile(`(?i)\b\d+ of \d+\b`)
	normNumber    = regexp.MustCompile(`\b\d+\b`)
	spaces        = regexp.MustCompile(`\s+`)
)

// isProgressOverwriteLine: next is prev, a progress line, with other
// counts (ddt-ui-shell overwrites those).
func isProgressOverwriteLine(prev, next string) bool {
	if !isProgressLike(prev) || !isProgressLike(next) {
		return false
	}
	p, n := normalizeProgress(prev), normalizeProgress(next)
	if p == "" || p != n {
		return false
	}
	prefix := 0
	for prefix < min(len(prev), len(next)) && prev[prefix] == next[prefix] {
		prefix++
	}
	return prefix >= max(12, min(len(prev), len(next))*7/10)
}

func isProgressLike(s string) bool {
	return digitOrPct.MatchString(s) && (progressWords.MatchString(s) ||
		percent.MatchString(s) || fraction.MatchString(s) || ofCount.MatchString(s))
}

func normalizeProgress(s string) string {
	s = normPercent.ReplaceAllString(s, "%")
	s = normFraction.ReplaceAllString(s, "#/#")
	s = normOf.ReplaceAllString(s, "# of #")
	s = normNumber.ReplaceAllString(s, "#")
	return strings.TrimSpace(spaces.ReplaceAllString(s, " "))
}
