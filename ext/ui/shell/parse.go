package shell

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The command line, as ddt-ui-shell parses it: words with quotes and
// backslashes, leading NAME=value assignments, $VAR and ${VAR}, ~, and
// globs. There are no pipes and no redirections (the command is run
// directly, not by a shell).

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
}

var (
	errUnclosedQuote = errors.New("unclosed quote")
	// errSeveralLines: a new line out of quotes, which ends a command in a
	// shell. There is one command at a time here.
	errSeveralLines = errors.New("several command lines: one at a time")
)

// incomplete: input goes on in the next line, as in a shell: a quote is
// not closed, or it ends with a backslash.
func incomplete(input string) bool {
	_, trailing, err := scanTokens(input)
	return trailing || errors.Is(err, errUnclosedQuote)
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
		result = append(result, token{text.String(), segs})
		segs, quote, inToken, force = nil, quoteNone, false, false
		buf.Reset()
	}
	for _, c := range input {
		switch st {
		case normal:
			switch c {
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

// parsed is a command line: its assignments and its words.
type parsed struct {
	env  map[string]string
	args []string
}

// parseCommandLine parses input in the directory cwd; getenv is the
// environment ($HOME for ~ too).
func parseCommandLine(cwd, input string, getenv func(string) (string, bool)) (parsed, error) {
	toks, err := parseTokens(input)
	if err != nil {
		return parsed{}, err
	}
	env := map[string]string{}
	i := 0
	for ; i < len(toks); i++ {
		m := assignment.FindStringSubmatch(toks[i].text)
		if m == nil {
			break
		}
		key, val := m[1], m[2]
		if val == "" && i+1 < len(toks) {
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
	for _, t := range toks[i:] {
		args = append(args, expandArg(cwd, t, lookup)...)
	}
	return parsed{env, args}, nil
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
