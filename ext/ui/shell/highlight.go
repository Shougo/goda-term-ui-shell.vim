package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// The highlight of the command line being typed, as fast-syntax-highlighting
// makes it in zsh: the command by what it is (unknown in red), options,
// quotes, variables, globs, escapes, the paths that exist, and the
// operators between the commands ("&&", "||", ";").

// DefaultCommandLineHighlights are the groups of the kinds of the command
// line ("" for none).
func DefaultCommandLineHighlights() map[string]string {
	return map[string]string{
		"alias":       "Function",
		"assignment":  "Identifier",
		"builtin":     "Statement",
		"command":     "Function",
		"directory":   "Directory",
		"doubleQuote": "String",
		"escape":      "SpecialChar",
		"glob":        "Special",
		"option":      "Type",
		"path":        "Underlined",
		"precommand":  "Statement",
		"separator":   "Operator",
		"singleQuote": "String",
		"unknown":     "Error",
		"variable":    "Identifier",
	}
}

// hlSpan is a span of the command line, in bytes, of a kind.
type hlSpan struct {
	start, end int
	kind       string
}

// hlWord is a word of the command line: its span, its text without quotes
// and escapes, whether it is only that text (no quote, variable, glob nor
// escape), and the spans in it.
type hlWord struct {
	start, end int
	text       string
	simple     bool
	inner      []hlSpan
	// separator: an operator between commands.
	separator bool
}

// precommands run the next word as the command (with their options first).
var precommands = []string{"time", "with", "sudo", "nohup", "nice", "env", "command", "exec"}

// sudoArgOptions are the options of sudo with an argument.
var sudoArgOptions = []string{"-u", "-g", "-C", "-D", "-h", "-p", "-r", "-t", "-U", "-T"}

// hlEnv is what the kinds depend on.
type hlEnv struct {
	cwd, home, path string
	aliases         map[string]string
	builtins        map[string]bool
}

// highlightSpans are the spans of the command line line: the words first
// (the command, an option, a path), then what is in them, which is over.
func highlightSpans(line string, env hlEnv) []hlSpan {
	var out, inner []hlSpan
	command := true
	// pre: the precommand before (its options, then the command).
	pre := ""
	skip := false
	for _, w := range scanWords(line) {
		inner = append(inner, w.inner...)
		raw := line[w.start:w.end]
		switch {
		case w.separator:
			// The next word is a command again.
			out = append(out, hlSpan{w.start, w.end, "separator"})
			command, pre, skip = true, "", false
		case skip:
			// (The argument of an option of sudo.)
			skip = false
		case command && assignment.MatchString(raw):
			eq := strings.IndexByte(raw, '=')
			out = append(out, hlSpan{w.start, w.start + eq + 1, "assignment"})
		case command && pre != "" && strings.HasPrefix(raw, "-"):
			out = append(out, hlSpan{w.start, w.end, "option"})
			skip = pre == "sudo" && slices.Contains(sudoArgOptions, raw)
		case command:
			if kind := env.commandKind(w); kind != "" {
				out = append(out, hlSpan{w.start, w.end, kind})
			}
			pre = ""
			if w.simple && slices.Contains(precommands, w.text) {
				pre = w.text
			} else {
				command = false
			}
		case strings.HasPrefix(raw, "-"):
			out = append(out, hlSpan{w.start, w.end, "option"})
		case w.simple && env.exists(w.text):
			out = append(out, hlSpan{w.start, w.end, "path"})
		}
	}
	return append(out, inner...)
}

// commandKind is the kind of the command w: what it runs.
func (e hlEnv) commandKind(w hlWord) string {
	if !w.simple {
		return ""
	}
	name := w.text
	if _, ok := e.aliases[name]; ok {
		return "alias"
	}
	if e.builtins[name] {
		return "builtin"
	}
	if slices.Contains(precommands, name) && e.lookPath(name) {
		return "precommand"
	}
	if strings.ContainsRune(name, '/') || strings.HasPrefix(name, "~") || name == ".." {
		p := e.abs(name)
		if fi, err := os.Stat(p); err == nil {
			if fi.IsDir() {
				return "directory"
			}
			if executable(fi) {
				return "command"
			}
		}
		return "unknown"
	}
	if e.lookPath(name) {
		return "command"
	}
	return "unknown"
}

// abs is the path p: "~" expanded, from the directory.
func (e hlEnv) abs(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = e.home + p[1:]
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(e.cwd, p)
	}
	return p
}

// exists: the path p exists.
func (e hlEnv) exists(p string) bool {
	if p == "" || strings.HasPrefix(p, "~") && e.home == "" {
		return false
	}
	_, err := os.Stat(e.abs(p))
	return err == nil
}

// lookPath: name is an executable in e.path.
func (e hlEnv) lookPath(name string) bool {
	if runtime.GOOS == "windows" {
		_, err := exec.LookPath(name)
		return err == nil
	}
	for _, dir := range filepath.SplitList(e.path) {
		if dir == "" {
			dir = "."
		}
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil && !fi.IsDir() && executable(fi) {
			return true
		}
	}
	return false
}

func executable(fi os.FileInfo) bool {
	return runtime.GOOS == "windows" || fi.Mode()&0o111 != 0
}

// scanWords are the words of line, as parseTokens reads them, with their
// spans; it never fails (a quote not closed goes to the end).
func scanWords(line string) []hlWord {
	var words []hlWord
	i := 0
	for i < len(line) {
		// Spaces, and a backslash at the end of a line.
		switch {
		case line[i] == ' ' || line[i] == '\t' || line[i] == '\n':
			i++
			continue
		case strings.HasPrefix(line[i:], "\\\n"):
			i += 2
			continue
		}
		if n := separatorAt(line, i); n > 0 {
			words = append(words, hlWord{start: i, end: i + n, text: line[i : i+n], separator: true})
			i += n
			continue
		}
		w := hlWord{start: i, simple: true}
		var text strings.Builder
	word:
		for i < len(line) {
			c := line[i]
			switch {
			case c == ' ' || c == '\t' || c == '\n' || strings.HasPrefix(line[i:], "\\\n") ||
				separatorAt(line, i) > 0:
				break word
			case c == '\'':
				end := strings.IndexByte(line[i+1:], '\'')
				if end < 0 {
					end = len(line)
				} else {
					end += i + 2
				}
				w.inner = append(w.inner, hlSpan{i, end, "singleQuote"})
				w.simple = false
				i = end
			case c == '"':
				i = scanDouble(line, i, &w)
				w.simple = false
			case c == '\\':
				end := min(i+2, len(line))
				w.inner = append(w.inner, hlSpan{i, end, "escape"})
				w.simple = false
				i = end
			case c == '$':
				end := scanVariable(line, i)
				if end == i+1 {
					// A "$" alone.
					text.WriteByte(c)
					i++
					continue
				}
				w.inner = append(w.inner, hlSpan{i, end, "variable"})
				w.simple = false
				i = end
			case c == '*' || c == '?' || c == '[':
				w.inner = append(w.inner, hlSpan{i, i + 1, "glob"})
				w.simple = false
				i++
			default:
				text.WriteByte(c)
				i++
			}
		}
		w.end, w.text = i, text.String()
		words = append(words, w)
	}
	return words
}

// separatorAt is the length of the operator between commands at i ("&&",
// "||", ";"), or 0 (as splitList reads them).
func separatorAt(line string, i int) int {
	switch {
	case line[i] == ';':
		return 1
	case (line[i] == '&' || line[i] == '|') && i+1 < len(line) && line[i+1] == line[i]:
		return 2
	}
	return 0
}

// scanDouble reads the double quote at i into w: its escapes and variables
// in it. It is the index after it.
func scanDouble(line string, i int, w *hlWord) int {
	start := i
	i++
	var in []hlSpan
	for i < len(line) && line[i] != '"' {
		switch line[i] {
		case '\\':
			end := min(i+2, len(line))
			in = append(in, hlSpan{i, end, "escape"})
			i = end
		case '$':
			end := scanVariable(line, i)
			if end > i+1 {
				in = append(in, hlSpan{i, end, "variable"})
			}
			i = end
		default:
			i++
		}
	}
	if i < len(line) {
		i++
	}
	w.inner = append(w.inner, hlSpan{start, i, "doubleQuote"})
	w.inner = append(w.inner, in...)
	return i
}

// scanVariable is the index after the variable at i ("$NAME", "${NAME}"),
// or i+1 when it is not one.
func scanVariable(line string, i int) int {
	j := i + 1
	if j < len(line) && line[j] == '{' {
		if end := strings.IndexByte(line[j:], '}'); end >= 0 {
			return j + end + 1
		}
		return len(line)
	}
	for j < len(line) && (line[j] == '_' || line[j] >= 'a' && line[j] <= 'z' ||
		line[j] >= 'A' && line[j] <= 'Z' || j > i+1 && line[j] >= '0' && line[j] <= '9') {
		j++
	}
	return j
}

// hlPriority is the priority of a kind: what is in the words is over them.
func hlPriority(kind string) int {
	switch kind {
	case "doubleQuote", "singleQuote":
		return 50
	case "escape", "variable", "glob":
		return 60
	}
	return 40
}

// lineSpan is a span of a line of the buffer, for
// goda_term#ui#shell#_highlight_command_line().
type lineSpan struct {
	Lnum     int    `json:"lnum"`
	Col      int    `json:"col"`
	Length   int    `json:"length"`
	Group    string `json:"group"`
	Name     string `json:"name"`
	Priority int    `json:"priority"`
}

// commandLineSpans are the spans of the lines of a command line, from line
// lnum, its first one from byte col (after the prompt), with the groups of
// groups.
func commandLineSpans(lines []string, lnum, col int, env hlEnv, groups map[string]string) []lineSpan {
	if len(lines) == 0 || col < 1 || col-1 > len(lines[0]) {
		return nil
	}
	// starts: the offset of each line in the command line.
	parts := append([]string{lines[0][col-1:]}, lines[1:]...)
	starts := make([]int, len(parts))
	for k := 1; k < len(parts); k++ {
		starts[k] = starts[k-1] + len(parts[k-1]) + 1
	}
	var out []lineSpan
	for _, s := range highlightSpans(strings.Join(parts, "\n"), env) {
		group := groups[s.kind]
		if group == "" {
			continue
		}
		for k, st := range starts {
			b, e := max(s.start, st), min(s.end, st+len(parts[k]))
			if b >= e {
				continue
			}
			c := b - st + 1
			if k == 0 {
				c += col - 1
			}
			out = append(out, lineSpan{Lnum: lnum + k, Col: c, Length: e - b, Group: group,
				Name: "GodaTermShellCommandLine_" + s.kind, Priority: hlPriority(s.kind)})
		}
	}
	return out
}
