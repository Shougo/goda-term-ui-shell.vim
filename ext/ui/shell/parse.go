package shell

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The command line, as ddt-ui-shell parses it: words with quotes and
// backslashes, leading NAME=value assignments, $VAR and ${VAR}, ~, and
// globs; and commands one after another with "&&", "||", ";" and new
// lines, and in pipes ("|"); "#" at the start of a word is a comment, to
// the end of its line. Each command is run directly, not by a shell. Its
// redirections are "<", ">", ">>" with a file, and ">&" with a descriptor,
// each after a descriptor (0, 1 or 2) or not ("2>err", "2>&1").

type quoteKind int

const (
	quoteNone quoteKind = iota
	quoteSingle
	quoteDouble
)

type segment struct {
	text  string
	quote quoteKind
}

type token struct {
	text     string
	segments []segment
	// op: an operator, not a word: "|", or the kind of redir ("<", ">",
	// ">>").
	op    string
	redir redirection
}

// redirection is "[fd]<", "[fd]>", "[fd]>>" with the file path, or
// "[fd]>&dup" (dup is -1 otherwise).
type redirection struct {
	fd, dup int
	op      string
	path    string
}

var (
	// errPipe: a pipe where one command is wanted.
	errPipe       = errors.New("a pipe is not one command")
	errEmptyStage = errors.New("a command is missing in a pipe")
	// errDescriptor: not 0, 1 or 2.
	errDescriptor = errors.New("bad file descriptor")
	errNoFile     = errors.New("a file is missing after")
	errAmbiguous  = errors.New("ambiguous redirection")
)

// redirectAt is the redirection at i of s, with the descriptor before it,
// and its length (0: none). An error has the length read.
func redirectAt(s string, i int) (redirection, int, error) {
	r := redirection{fd: -1, dup: -1}
	j := i
	if j < len(s) && '0' <= s[j] && s[j] <= '9' {
		r.fd = int(s[j] - '0')
		j++
	}
	if j >= len(s) || s[j] != '<' && s[j] != '>' {
		return redirection{}, 0, nil
	}
	r.op = s[j : j+1]
	j++
	if r.op == ">" && j < len(s) && s[j] == '>' {
		r.op = ">>"
		j++
	}
	if r.fd < 0 {
		r.fd = 1
		if r.op == "<" {
			r.fd = 0
		}
	}
	if r.fd > 2 {
		return r, j - i, fmt.Errorf("%w: %d", errDescriptor, r.fd)
	}
	if r.op == ">" && j < len(s) && s[j] == '&' {
		j++
		if j >= len(s) || s[j] < '0' || s[j] > '2' {
			return r, j - i, fmt.Errorf("%w after %q", errDescriptor, s[i:j])
		}
		r.dup = int(s[j] - '0')
		j++
	}
	return r, j - i, nil
}

var (
	errUnclosedQuote = errors.New("unclosed quote")
	// errSeveralLines: a new line out of quotes in one command (splitList
	// has made the commands of the lines).
	errSeveralLines = errors.New("several command lines: one at a time")
)

// incomplete: input goes on in the next line, as in a shell: a quote is
// not closed, or it ends with a backslash, "&&" or "||".
func incomplete(input string) bool {
	items, open, err := scanList(input)
	if err != nil {
		return errors.Is(err, errUnclosedQuote)
	}
	if open {
		return true
	}
	if len(items) == 0 {
		return false
	}
	toks, trailing, err := scanTokens(items[len(items)-1].line)
	// "|" at the end goes on too, as "&&" does.
	return trailing || errors.Is(err, errUnclosedQuote) ||
		err == nil && len(toks) > 0 && toks[len(toks)-1].op == "|"
}

// listItem is a command of a command line, and the operator before it
// ("" for the first one, "&&", "||" or ";").
type listItem struct {
	line, op string
}

var errEmptyCommand = errors.New("a command is missing around")

// splitList is the commands of input. A new line after an operator goes on
// with the next command.
func splitList(input string) ([]listItem, error) {
	items, open, err := scanList(input)
	if err == nil && open {
		err = fmt.Errorf("%w %q", errEmptyCommand, items[len(items)-1].op)
	}
	return items, err
}

// scanList splits input at the operators out of quotes; open: it ends
// with "&&" or "||" (the next command is still to come). A ";" at the end
// is nothing.  A new line is a ";", as in a shell (commands pasted with
// their lines run one after another), but after "&&", "||" or "|", where
// the command goes on, and an empty line is nothing.  A comment ("#" at
// the start of a word) is not in the commands.
func scanList(input string) (items []listItem, open bool, err error) {
	op := ""
	start := 0
	const (
		normal = iota
		inDouble
		inSingle
	)
	st, esc := normal, false
	// pipe: the last of the line out of quotes is a "|".
	pipe := false
	for i := 0; i < len(input); i++ {
		c := input[i]
		comment := st == normal && !esc && c == '#' && (i == 0 || strings.IndexByte(" \t\n;&|", input[i-1]) >= 0)
		if st == normal && !esc && !comment && c != ' ' && c != '\t' && c != '\n' {
			pipe = c == '|' && !(i+1 < len(input) && input[i+1] == '|')
		}
		switch {
		case esc:
			esc = false
		case st == inDouble:
			switch c {
			case '\\':
				esc = true
			case '"':
				st = normal
			}
		case st == inSingle:
			if c == '\'' {
				st = normal
			}
		case c == '\\':
			esc = true
		case c == '"':
			st = inDouble
		case c == '\'':
			st = inSingle
		case comment:
			// A comment: out of the line.
			end := strings.IndexByte(input[i:], '\n')
			if end < 0 {
				end = len(input) - i
			}
			input = input[:i] + input[i+end:]
			i--
		case c == '\n':
			line := strings.TrimSpace(input[start:i])
			switch {
			case pipe:
				// "a |" goes on with the next line (one command): of one
				// line for it.
				input = input[:i] + " " + input[i+1:]
				pipe = false
			case line == "":
				// An empty line, or one after "&&" or "||".
				start = i + 1
			default:
				items = append(items, listItem{line, op})
				op = ";"
				start = i + 1
			}
		case c == ';' || (c == '&' || c == '|') && i+1 < len(input) && input[i+1] == c:
			// ("&" and "|" alone are words.)
			n := 1
			if c != ';' {
				n = 2
			}
			line := strings.TrimSpace(input[start:i])
			if line == "" {
				return items, false, fmt.Errorf("%w %q", errEmptyCommand, input[i:i+n])
			}
			items = append(items, listItem{line, op})
			op = input[i : i+n]
			i += n - 1
			start = i + 1
		}
	}
	line := strings.TrimSpace(input[start:])
	switch {
	case st != normal:
		return items, false, errUnclosedQuote
	case line != "":
		items = append(items, listItem{line, op})
	case op == "&&" || op == "||":
		return items, true, nil
	}
	return items, false, nil
}

func parseTokens(input string) ([]token, error) {
	toks, _, err := scanTokens(input)
	return toks, err
}

// scanTokens is the tokens of input, and whether it ends with a backslash
// (which is kept then).
func scanTokens(input string) (result []token, trailing bool, err error) {
	type state int
	const (
		normal state = iota
		inDouble
		inSingle
		escNormal
		escDouble
		escSingle
	)
	var segs []segment
	var buf strings.Builder
	inToken, force := false, false
	quote := quoteNone
	st := normal
	flush := func() {
		if buf.Len() > 0 || force {
			segs = append(segs, segment{buf.String(), quote})
			buf.Reset()
			force = false
		}
	}
	push := func() {
		flush()
		var text strings.Builder
		for _, s := range segs {
			text.WriteString(s.text)
		}
		result = append(result, token{text: text.String(), segments: segs})
		segs, quote, inToken, force = nil, quoteNone, false, false
		buf.Reset()
	}
	var size int
	for i := 0; i < len(input); i += size {
		var c rune
		c, size = utf8.DecodeRuneInString(input[i:])
		switch st {
		case normal:
			switch c {
			case '|':
				if inToken {
					push()
				}
				result = append(result, token{text: "|", op: "|"})
			case '<', '>':
				// A word of one digit just before: the descriptor.
				start := i
				if inToken && len(segs) == 0 && !force && quote == quoteNone && buf.Len() == 1 &&
					'0' <= buf.String()[0] && buf.String()[0] <= '9' {
					start = i - 1
					buf.Reset()
					inToken = false
				} else if inToken {
					push()
				}
				r, n, err := redirectAt(input, start)
				if err != nil {
					return nil, false, err
				}
				result = append(result, token{text: input[start : start+n], op: r.op, redir: r})
				size = start + n - i
			case '"', '\'':
				flush()
				quote, force, inToken = quoteDouble, true, true
				st = inDouble
				if c == '\'' {
					quote, st = quoteSingle, inSingle
				}
			case '\\':
				quote, st, inToken = quoteNone, escNormal, true
			case ' ', '\t':
				if inToken {
					push()
				}
			case '\n':
				return nil, false, errSeveralLines
			default:
				if quote != quoteNone {
					flush()
					quote = quoteNone
				}
				buf.WriteRune(c)
				inToken = true
			}
		case inDouble:
			switch c {
			case '"':
				flush()
				quote, st = quoteNone, normal
			case '\\':
				st = escDouble
			default:
				buf.WriteRune(c)
			}
		case inSingle:
			switch c {
			case '\'':
				flush()
				quote, st = quoteNone, normal
			case '\\':
				st = escSingle
			default:
				buf.WriteRune(c)
			}
		case escNormal:
			switch c {
			case '\n':
			case 't':
				buf.WriteByte('\t')
			default:
				buf.WriteRune(c)
			}
			st = normal
		case escDouble:
			switch c {
			case 'n':
				buf.WriteByte('\n')
			case 'r':
				buf.WriteByte('\r')
			case 't':
				buf.WriteByte('\t')
			default:
				buf.WriteRune(c)
			}
			st = inDouble
		case escSingle:
			if c != '\'' {
				buf.WriteByte('\\')
			}
			buf.WriteRune(c)
			st = inSingle
		}
	}
	switch st {
	case inDouble, inSingle, escDouble, escSingle:
		return nil, false, errUnclosedQuote
	case escNormal:
		buf.WriteByte('\\')
		trailing = true
	}
	if inToken {
		push()
	}
	return result, trailing, nil
}

// splitArgs is the words of input, without expansions.
func splitArgs(input string) ([]string, error) {
	toks, err := parseTokens(input)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(toks))
	for i, t := range toks {
		out[i] = t.text
	}
	return out, nil
}

var assignment = regexp.MustCompile(`(?s)^\$?([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)
var (
	doubleEscape = regexp.MustCompile(`\\(["\\$` + "`" + `nrt])`)
	singleEscape = regexp.MustCompile(`\\(')`)
	plainEscape  = regexp.MustCompile(`\\([ \t\\])`)
)

// parsed is a command line: its assignments, its words and its
// redirections (in their order).
type parsed struct {
	env    map[string]string
	args   []string
	redirs []redirection
}

// parseCommandLine parses input, one command, in the directory cwd;
// getenv is the environment ($HOME for ~ too).
func parseCommandLine(cwd, input string, getenv func(string) (string, bool)) (parsed, error) {
	stages, err := parsePipeline(cwd, input, getenv)
	if err != nil {
		return parsed{}, err
	}
	if len(stages) > 1 {
		return parsed{}, errPipe
	}
	return stages[0], nil
}

// parsePipeline parses input, its commands separated by "|", as
// parseCommandLine.
func parsePipeline(cwd, input string, getenv func(string) (string, bool)) ([]parsed, error) {
	toks, err := parseTokens(input)
	if err != nil {
		return nil, err
	}
	var stages []parsed
	start := 0
	for i := 0; i <= len(toks); i++ {
		if i < len(toks) && toks[i].op != "|" {
			continue
		}
		if i == start && (i < len(toks) || start > 0) {
			return nil, errEmptyStage
		}
		c, err := parseCommand(cwd, toks[start:i], getenv)
		if err != nil {
			return nil, err
		}
		stages = append(stages, c)
		start = i + 1
	}
	return stages, nil
}

// parseCommand parses the tokens of one command.
func parseCommand(cwd string, toks []token, getenv func(string) (string, bool)) (parsed, error) {
	env := map[string]string{}
	i := 0
	for ; i < len(toks); i++ {
		m := assignment.FindStringSubmatch(toks[i].text)
		if m == nil || toks[i].op != "" {
			break
		}
		key, val := m[1], m[2]
		if val == "" && i+1 < len(toks) && toks[i+1].op == "" {
			i++
			val = toks[i].text
		} else {
			for strings.HasSuffix(val, "\\") && i+1 < len(toks) {
				i++
				val += " " + toks[i].text
			}
		}
		switch {
		case len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"':
			val = doubleEscape.ReplaceAllStringFunc(val[1:len(val)-1], func(s string) string {
				switch s[1] {
				case 'n':
					return "\n"
				case 'r':
					return "\r"
				case 't':
					return "\t"
				}
				return s[1:]
			})
		case len(val) >= 2 && val[0] == '\'' && val[len(val)-1] == '\'':
			val = singleEscape.ReplaceAllString(val[1:len(val)-1], "$1")
		default:
			val = plainEscape.ReplaceAllString(val, "$1")
		}
		env[key] = val
	}
	lookup := func(name string) (string, bool) {
		if v, ok := env[name]; ok {
			return v, true
		}
		return getenv(name)
	}
	var args []string
	var redirs []redirection
	rest := toks[i:]
	for k := 0; k < len(rest); k++ {
		t := rest[k]
		switch {
		case t.op != "":
			r := t.redir
			if r.dup < 0 {
				if k+1 >= len(rest) || rest[k+1].op != "" {
					return parsed{}, fmt.Errorf("%w %q", errNoFile, t.text)
				}
				k++
				words := expandArg(cwd, rest[k], lookup)
				if len(words) != 1 {
					return parsed{}, fmt.Errorf("%w: %s", errAmbiguous, rest[k].text)
				}
				r.path = words[0]
				if !filepath.IsAbs(r.path) {
					r.path = filepath.Join(cwd, r.path)
				}
			}
			redirs = append(redirs, r)
		default:
			args = append(args, expandArg(cwd, t, lookup)...)
		}
	}
	return parsed{env, args, redirs}, nil
}

func expandArg(cwd string, t token, lookup func(string) (string, bool)) []string {
	var b strings.Builder
	for _, s := range t.segments {
		if s.quote == quoteSingle {
			b.WriteString(s.text)
		} else {
			b.WriteString(expandVariables(s.text, lookup))
		}
	}
	arg := b.String()
	if strings.HasPrefix(arg, "~") {
		home, ok := lookup("HOME")
		if !ok {
			home, _ = os.UserHomeDir()
		}
		if home != "" {
			arg = home + arg[1:]
		}
	}
	if strings.ContainsAny(arg, "*?[") {
		pattern := arg
		if !filepath.IsAbs(pattern) {
			pattern = filepath.Join(cwd, pattern)
		}
		if matches, err := filepath.Glob(pattern); err == nil && len(matches) > 0 {
			for i, m := range matches {
				if rel, err := filepath.Rel(cwd, m); err == nil && !filepath.IsAbs(arg) {
					matches[i] = rel
				}
			}
			return matches
		}
	}
	return []string{arg}
}

func isVarStart(c byte) bool {
	return c == '_' || 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z'
}

func isVarChar(c byte) bool { return isVarStart(c) || '0' <= c && c <= '9' }

var varName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// expandVariables expands $VAR and ${VAR}; invalid ones stay as they are.
func expandVariables(text string, lookup func(string) (string, bool)) string {
	var out strings.Builder
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c != '$' || i+1 >= len(text) {
			out.WriteByte(c)
			continue
		}
		if text[i+1] == '{' {
			end := strings.IndexByte(text[i+2:], '}')
			if end < 0 {
				out.WriteByte(c)
				continue
			}
			end += i + 2
			name := text[i+2 : end]
			if !varName.MatchString(name) {
				out.WriteString(text[i : end+1])
			} else {
				v, _ := lookup(name)
				out.WriteString(v)
			}
			i = end
			continue
		}
		if !isVarStart(text[i+1]) {
			out.WriteByte(c)
			continue
		}
		end := i + 2
		for end < len(text) && isVarChar(text[end]) {
			end++
		}
		name := resolveVariableName(text[i+1:end], lookup)
		v, _ := lookup(name)
		out.WriteString(v)
		i += len(name)
	}
	return out.String()
}

// resolveVariableName prefers the whole name, but falls back from
// "$HOGEbar" to "HOGE" when that is set (the rest stays as text).
func resolveVariableName(candidate string, lookup func(string) (string, bool)) string {
	if _, ok := lookup(candidate); ok {
		return candidate
	}
	if i := strings.IndexFunc(candidate, func(r rune) bool { return 'a' <= r && r <= 'z' }); i > 0 {
		if _, ok := lookup(candidate[:i]); ok {
			return candidate[:i]
		}
	}
	return candidate
}
