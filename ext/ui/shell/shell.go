// Package shell is the UI "shell" of ddt-ui-shell: a buffer with a prompt,
// where each command line runs in a pseudo terminal of its own, and its
// output (with the ANSI colors) is written in the buffer. The command line
// is not given to a shell: there are builtins (cd, history, time, vim,
// with), aliases, and the expansions of parse.go.
package shell

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Shougo/goda.vim/sdk"

	"github.com/Shougo/goda-term-ui-shell.vim/internal/pty"
	"github.com/Shougo/goda-term.vim/term"
)

// ANSIColorHighlights are the highlight groups of the ANSI colors and
// attributes of the output.
type ANSIColorHighlights struct {
	Bgs       []string `json:"bgs,omitempty"`
	Bold      string   `json:"bold,omitempty"`
	Fgs       []string `json:"fgs,omitempty"`
	Italic    string   `json:"italic,omitempty"`
	Underline string   `json:"underline,omitempty"`
}

// Params are the params of the UI (those of ddt-ui-shell).
type Params struct {
	// Aliases replace the first word of a command line.
	Aliases             map[string]string   `json:"aliases"`
	ANSIColorHighlights ANSIColorHighlights `json:"ansiColorHighlights"`
	// CommandLineHighlights are the groups of the command line being typed,
	// by kind (DefaultCommandLineHighlights; "": none).
	CommandLineHighlights map[string]string `json:"commandLineHighlights"`
	Cwd                   string            `json:"cwd"`
	// ExprParams are the params whose strings are Vim expressions (of
	// uiParams, the params).
	ExprParams     []string `json:"exprParams"`
	FloatingBorder any      `json:"floatingBorder"`
	// FlushIntervalMs is how often the output is written.
	FlushIntervalMs int `json:"flushIntervalMs"`
	// NoSaveHistoryCommands are not written in the history.
	NoSaveHistoryCommands []string `json:"noSaveHistoryCommands"`
	// PasswordPattern asks for a password (inputsecret()) when the output
	// matches it.
	PasswordPattern string `json:"passwordPattern"`
	Prompt          string `json:"prompt"`
	PromptHighlight string `json:"promptHighlight"`
	// PromptPattern is the prompt, a Vim regexp.
	PromptPattern       string `json:"promptPattern"`
	ShellHistoryMax     int    `json:"shellHistoryMax"`
	ShellHistoryPath    string `json:"shellHistoryPath"`
	Split               string `json:"split"`
	StartInsert         bool   `json:"startInsert"`
	UserPrompt          string `json:"userPrompt"`
	UserPromptHighlight string `json:"userPromptHighlight"`
	UserPromptPattern   string `json:"userPromptPattern"`
	// WinCol, WinHeight, WinRow and WinWidth are numbers or expressions.
	WinCol    any `json:"winCol"`
	WinHeight any `json:"winHeight"`
	WinRow    any `json:"winRow"`
	WinWidth  any `json:"winWidth"`

	// noSaveHistory: the command line is not written in the history (the
	// send action with noSaveHistory).
	noSaveHistory bool
}

// DefaultParams are those of ddt-ui-shell.
func DefaultParams() Params {
	return Params{
		Aliases:               map[string]string{},
		CommandLineHighlights: DefaultCommandLineHighlights(),
		ExprParams:            []string{"winCol", "winRow", "winHeight", "winWidth"},
		FloatingBorder:        "",
		FlushIntervalMs:       100,
		PasswordPattern:       `(Enter |Repeat |[Oo]ld |[Nn]ew |login |Kerberos |EncFS |CVS |UNIX | SMB |LDAP |\[sudo( \w*)?\] )([Pp]assword|[Pp]assphrase)`,
		Prompt:                "%",
		PromptHighlight:       "Identifier",
		PromptPattern:         "% ",
		ShellHistoryMax:       500,
		UserPromptHighlight:   "Special",
		UserPromptPattern:     "| .*",
		WinCol:                "(&columns - eval(uiParams.winWidth)) / 2",
		WinHeight:             20,
		WinRow:                "&lines / 2 - 10",
		WinWidth:              "&columns / 2",
	}
}

type ui struct{}

// New returns the UI.
func New() term.UI { return ui{} }

func (ui) Name() string { return "shell" }

func (ui) New() term.Instance { return &instance{bufnr: -1} }

type instance struct {
	mu          sync.Mutex
	name        string
	bufnr       int
	cwd, oldCwd string
	// job is the command running, if any.
	job       *pty.Pty
	stack     []string
	with      []string
	startTime time.Time
}

func params(a term.Args) (Params, error) { return term.DecodeParams(a.Params, DefaultParams()) }

func toInt(v any) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case float64:
		return int(n)
	case int:
		return n
	}
	return -1
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func (t *instance) get() (bufnr int, cwd string, job *pty.Pty) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.bufnr, t.cwd, t.job
}

func printError(ctx context.Context, h sdk.Host, msg string) {
	_ = h.Cmd(ctx, "echohl ErrorMsg | echomsg "+vimString("goda-term: "+msg)+" | echohl None")
}

func vimString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func (t *instance) Redraw(ctx context.Context, h sdk.Host, a term.Args) error {
	p, err := params(a)
	if err != nil {
		return err
	}
	cwd := p.Cwd
	if cwd == "" {
		cwd = a.Context.Cwd
	}
	if v, err := h.Call(ctx, "expand", cwd); err == nil {
		cwd, _ = v.(string)
	}
	if !isDir(cwd) {
		v, err := h.Call(ctx, "confirm", cwd+" is not a directory.  Create?", "&Yes\n&No\n&Cancel")
		if err != nil || toInt(v) != 1 {
			return err
		}
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			return err
		}
	}
	resolved, err := h.Call(ctx, "goda_term#ui#shell#_eval_params", p)
	if err != nil {
		return err
	}
	if err := sdk.Decode(resolved, &p); err != nil {
		return err
	}
	t.mu.Lock()
	t.name = a.Options.Name
	bufnr, prevCwd := t.bufnr, t.cwd
	if prevCwd == "" {
		t.cwd = cwd
	}
	t.mu.Unlock()
	exists := false
	if bufnr > 0 {
		v, err := h.Call(ctx, "bufexists", bufnr)
		if err != nil {
			return err
		}
		exists = toInt(v) == 1
	}
	if exists {
		v, err := h.Call(ctx, "goda_term#ui#shell#_switch", p, bufnr)
		if err != nil {
			return err
		}
		if prevCwd != "" && cwd != prevCwd {
			// Started in another directory: the shell goes there.
			if err := t.newCdPrompt(ctx, h, p, cwd); err != nil {
				return err
			}
			if err := t.cd(ctx, h, p, cwd); err != nil {
				return err
			}
		} else if last, _ := v.(string); last == p.Prompt+" " {
			if err := t.newCommandLine(ctx, h, p, ""); err != nil {
				return err
			}
		}
	} else {
		if p.Prompt == "" || p.PromptPattern == "" {
			return fmt.Errorf("goda-term: the params \"prompt\" and \"promptPattern\" of the ui shell must be set")
		}
		v, err := h.Call(ctx, "goda_term#ui#shell#_new", a.Options.Name, p)
		if err != nil {
			return err
		}
		t.mu.Lock()
		t.bufnr = toInt(v)
		bufnr = t.bufnr
		t.cwd = cwd
		t.mu.Unlock()
		if err := t.newCommandLine(ctx, h, p, ""); err != nil {
			return err
		}
	}
	if _, err := h.Call(ctx, "setbufvar", bufnr, "goda_term_ui_shell_prompt_pattern", p.PromptPattern); err != nil {
		return err
	}
	_, err = h.Call(ctx, "goda_term#ui#_init_variables", a.Options.Name, "shell", bufnr, cwd)
	return err
}

func (t *instance) current(ctx context.Context, h sdk.Host) (bool, error) {
	bufnr, _, _ := t.get()
	v, err := h.Call(ctx, "bufnr", "%")
	return err == nil && toInt(v) == bufnr, err
}

// prompt is the last prompt (or message) of the buffer.
func (t *instance) prompt(ctx context.Context, h sdk.Host) string {
	bufnr, _, _ := t.get()
	v, _ := h.Call(ctx, "getbufvar", bufnr, "goda_term_ui_shell_prompt", "")
	s, _ := v.(string)
	return s
}

func (t *instance) commandLine(ctx context.Context, h sdk.Host, p Params) (string, error) {
	v, err := h.Call(ctx, "goda_term#ui#shell#_command_line", p.PromptPattern, t.prompt(ctx, h))
	s, _ := v.(string)
	return s, err
}

func (t *instance) GetInput(ctx context.Context, h sdk.Host, a term.Args) (string, error) {
	p, err := params(a)
	if err != nil {
		return "", err
	}
	if ok, err := t.current(ctx, h); err != nil || !ok || p.PromptPattern == "" {
		return "", err
	}
	v, err := h.Call(ctx, "goda_term#ui#shell#_input", p.PromptPattern, t.prompt(ctx, h))
	s, _ := v.(string)
	return s, err
}

// newCommandLine shows a new prompt with commandLine (or the prefix of
// "with", or the command line of the buffer stack); a command running is
// ended.
func (t *instance) newCommandLine(ctx context.Context, h sdk.Host, p Params, commandLine string) error {
	t.mu.Lock()
	job := t.job
	t.job = nil
	if len(t.with) > 0 {
		commandLine = strings.Join(t.with, " ") + " "
	}
	if commandLine == "" && len(t.stack) > 0 {
		commandLine = t.stack[len(t.stack)-1]
		t.stack = t.stack[:len(t.stack)-1]
	}
	bufnr := t.bufnr
	t.mu.Unlock()
	if job != nil {
		job.Close()
	}
	_, err := h.Call(ctx, "goda_term#ui#shell#_new_command_line", bufnr, p, commandLine)
	return err
}

func (t *instance) history(ctx context.Context, h sdk.Host, p Params) string {
	if p.ShellHistoryPath == "" {
		return ""
	}
	v, _ := h.Call(ctx, "expand", p.ShellHistoryPath)
	s, _ := v.(string)
	return s
}

func (t *instance) appendHistory(ctx context.Context, h sdk.Host, p Params, line string) {
	if path := t.history(ctx, h, p); path != "" {
		if err := appendHistory(path, line, p.ShellHistoryMax); err != nil {
			printError(ctx, h, "the history: "+err.Error())
		}
	}
}

func quote(dir string) string {
	if runtime.GOOS == "windows" {
		return `"` + dir + `"`
	}
	return "'" + dir + "'"
}

func (t *instance) newCdPrompt(ctx context.Context, h sdk.Host, p Params, dir string) error {
	line := "cd " + quote(dir)
	if err := t.newCommandLine(ctx, h, p, line); err != nil {
		return err
	}
	t.appendHistory(ctx, h, p, line)
	return nil
}

// cd changes the directory: "" is $HOME, "-" the previous one.
func (t *instance) cd(ctx context.Context, h sdk.Host, p Params, dir string) error {
	t.mu.Lock()
	cwd, old := t.cwd, t.oldCwd
	t.mu.Unlock()
	abs := dir
	switch {
	case dir == "":
		abs, _ = os.UserHomeDir()
		if v, err := h.Call(ctx, "expand", "~"); err == nil {
			abs, _ = v.(string)
		}
	case dir == "-":
		abs = old
	case !filepath.IsAbs(dir):
		abs = filepath.Join(cwd, dir)
	}
	if !isDir(abs) {
		printError(ctx, h, dir+" is not a directory.")
		return nil
	}
	t.mu.Lock()
	t.oldCwd, t.cwd = t.cwd, abs
	t.mu.Unlock()
	if _, err := h.Call(ctx, "goda_term#ui#shell#_cd", abs); err != nil {
		return err
	}
	return t.newCommandLine(ctx, h, p, "")
}

// expandDirectory is path as a directory (absolute, ./..., ../..., .../),
// or "".
func (t *instance) expandDirectory(ctx context.Context, h sdk.Host, path string) string {
	_, cwd, _ := t.get()
	v, _ := h.Call(ctx, "expand", path)
	expanded, _ := v.(string)
	abs := filepath.IsAbs(expanded)
	if !abs && !strings.HasPrefix(path, "./") && !strings.HasPrefix(path, "..") && !strings.HasSuffix(path, "/") {
		return ""
	}
	dir := expanded
	if !abs {
		dir = filepath.Join(cwd, path)
	}
	if isDir(dir) {
		return dir
	}
	return ""
}

// execute runs commandLine (or types it in the command running): whether a
// command was started, which writes the next prompt itself.
func (t *instance) execute(ctx context.Context, h sdk.Host, a term.Args, p Params, commandLine string) (bool, error) {
	bufnr, cwd, job := t.get()
	if job != nil {
		_, err := job.Write([]byte(commandLine + "\n"))
		return false, err
	}
	// (Of several lines, pasted: the empty line after them.)
	commandLine = strings.TrimRight(commandLine, " \t\n")
	if commandLine == "" {
		return false, nil
	}
	environ := map[string]string{}
	if v, err := h.Call(ctx, "environ"); err == nil {
		_ = sdk.Decode(v, &environ)
	}
	getenv := func(k string) (string, bool) {
		v, ok := environ[k]
		return v, ok
	}
	cl, err := parseCommandLine(cwd, commandLine, getenv)
	if err == nil && len(cl.args) > 0 {
		if alias, ok := p.Aliases[cl.args[0]]; ok {
			var c parsed
			c, err = parseCommandLine(cwd, strings.TrimSpace(alias+" "+strings.Join(cl.args[1:], " ")), getenv)
			cl.args = c.args
		}
	}
	if err != nil || len(cl.args) == 0 {
		if err != nil {
			printError(ctx, h, err.Error())
		}
		return false, t.newCommandLine(ctx, h, p, "")
	}
	cmd, args := cl.args[0], cl.args[1:]
	if !p.noSaveHistory && !slices.Contains(p.NoSaveHistoryCommands, cmd) {
		t.appendHistory(ctx, h, p, commandLine)
	}
	if b, ok := t.builtins()[cmd]; ok {
		started, skip, err := b(ctx, h, a, p, args)
		if err != nil || started || skip {
			return started, err
		}
		return false, t.newCommandLine(ctx, h, p, "")
	}
	if dir := t.expandDirectory(ctx, h, commandLine); dir != "" {
		return false, t.cd(ctx, h, p, dir)
	}
	v, err := h.Call(ctx, "exepath", cmd)
	if err != nil {
		return false, err
	}
	path, _ := v.(string)
	if path == "" {
		printError(ctx, h, cmd+" is not an executable file.")
		return false, t.newCommandLine(ctx, h, p, "")
	}
	maps := map[string]string{"GIT_PAGER": "cat", "MANPAGER": "cat", "PAGER": "cat", "TERM": "xterm"}
	env := make([]string, 0, len(environ)+len(cl.env)+len(maps))
	for k, v := range environ {
		if _, ok := cl.env[k]; !ok && maps[k] == "" {
			env = append(env, k+"="+v)
		}
	}
	for k, v := range cl.env {
		if maps[k] == "" {
			env = append(env, k+"="+v)
		}
	}
	for k, v := range maps {
		env = append(env, k+"="+v)
	}
	cols := 80
	if v, err := h.Call(ctx, "goda_term#ui#shell#_width", bufnr); err == nil && toInt(v) > 0 {
		cols = toInt(v)
	}
	j, err := pty.Start(path, pty.Options{Args: args, Env: env, Dir: cwd, Cols: cols})
	if err != nil {
		printError(ctx, h, err.Error())
		return false, t.newCommandLine(ctx, h, p, "")
	}
	v, err = h.Call(ctx, "getbufinfo", bufnr)
	if err != nil {
		j.Close()
		return false, err
	}
	var info []struct {
		Linecount int `json:"linecount"`
	}
	_ = sdk.Decode(v, &info)
	promptLine := 0
	if len(info) > 0 {
		promptLine = info[0].Linecount
	}
	t.mu.Lock()
	t.job = j
	t.mu.Unlock()
	// The line of its output, before it writes anything (a build that is
	// quiet for long shows that it runs).
	if _, err := h.Call(ctx, "goda_term#ui#shell#_running", bufnr); err != nil {
		log.Printf("goda-term: %v", err)
	}
	go t.output(h, p, j, bufnr, promptLine)
	return true, nil
}

// output writes what the command j writes after the line promptLine, then
// the next prompt.
func (t *instance) output(h sdk.Host, p Params, j *pty.Pty, bufnr, promptLine int) {
	ctx := context.Background()
	password, err := regexp.Compile(p.PasswordPattern)
	if err != nil {
		printError(ctx, h, "passwordPattern: "+err.Error())
		password = nil
	}
	chunks := make(chan string)
	go func() {
		defer close(chunks)
		buf := make([]byte, 32*1024)
		for {
			n, err := j.Read(buf)
			if n > 0 {
				chunks <- string(buf[:n])
			}
			if err != nil {
				if err != io.EOF {
					log.Printf("goda-term: %v", err)
				}
				return
			}
		}
	}()
	r := newRenderer()
	interval := time.Duration(max(p.FlushIntervalMs, 1)) * time.Millisecond
	tick := time.NewTicker(interval)
	defer tick.Stop()
	// ours: the command is still that of the buffer (a new prompt ends
	// it, and the output is no more written then).
	ours := func() bool {
		t.mu.Lock()
		defer t.mu.Unlock()
		return t.job == j
	}
	flush := func() {
		first, lines := r.take()
		if len(lines) == 0 || !ours() {
			return
		}
		texts := make([]string, len(lines))
		hls := [][]any{}
		for i, l := range lines {
			texts[i] = l.text
			for _, s := range l.spans {
				if g, name, prio := p.ANSIColorHighlights.group(s); g != "" {
					hls = append(hls, []any{i, s.col, s.length, g, name, prio})
				}
			}
		}
		if _, err := h.Call(ctx, "goda_term#ui#shell#_flush", bufnr, promptLine+1+first, texts, hls); err != nil {
			log.Printf("goda-term: %v", err)
		}
	}
loop:
	for {
		select {
		case c, ok := <-chunks:
			if !ok {
				break loop
			}
			r.write(c)
			if password != nil && password.MatchString(c) {
				flush()
				_ = h.Cmd(ctx, "normal! zz")
				v, err := h.Call(ctx, "inputsecret", "Password: ")
				s, _ := v.(string)
				if err == nil {
					_, _ = j.Write([]byte(s + "\n"))
				}
			}
		case <-tick.C:
			flush()
		}
	}
	flush()
	code := j.Wait()
	t.mu.Lock()
	mine := t.job == j
	if mine {
		t.job = nil
	}
	start := t.startTime
	t.startTime = time.Time{}
	t.mu.Unlock()
	j.Close()
	if !mine {
		return
	}
	if code != 0 {
		_, _ = h.Call(ctx, "goda_term#ui#shell#_message", bufnr, fmt.Sprintf("goda-term: exit %d", code))
	}
	if !start.IsZero() {
		_, _ = h.Call(ctx, "goda_term#ui#shell#_message", bufnr,
			fmt.Sprintf("goda-term: %d ms", time.Since(start).Milliseconds()))
	}
	if err := t.newCommandLine(ctx, h, p, ""); err != nil {
		log.Printf("goda-term: %v", err)
	}
}

// group is the highlight of a span: the group, the name of its text
// property, and its priority. The colors are over the attributes: the
// group of bold (often with colors of its own, "Bold") adds bold to the
// color, it does not replace it.
func (c ANSIColorHighlights) group(s span) (string, string, int) {
	switch s.attr {
	case "fg":
		if s.color < len(c.Fgs) {
			return c.Fgs[s.color], fmt.Sprintf("ANSIColorFG%d", s.color), 30
		}
	case "bg":
		if s.color < len(c.Bgs) {
			return c.Bgs[s.color], fmt.Sprintf("ANSIColorBG%d", s.color), 20
		}
	case "bold":
		return c.Bold, "ANSIColorBold", 10
	case "italic":
		return c.Italic, "ANSIColorItalic", 10
	case "underline":
		return c.Underline, "ANSIColorUnderline", 10
	}
	return "", "", 0
}

// builtin runs a builtin command: whether it started a command, and
// whether it shows the next prompt itself.
type builtin func(ctx context.Context, h sdk.Host, a term.Args, p Params, args []string) (started, skipPrompt bool, err error)

func (t *instance) builtins() map[string]builtin {
	return map[string]builtin{
		// cd [dir]: $HOME without one.
		"cd": func(ctx context.Context, h sdk.Host, _ term.Args, p Params, args []string) (bool, bool, error) {
			dir := ""
			if len(args) > 0 {
				dir = args[0]
			}
			return false, true, t.cd(ctx, h, p, dir)
		},
		// history: the history.
		"history": func(ctx context.Context, h sdk.Host, _ term.Args, p Params, _ []string) (bool, bool, error) {
			lines, err := readHistory(t.history(ctx, h, p))
			if err != nil {
				printError(ctx, h, err.Error())
			}
			bufnr, _, _ := t.get()
			if len(lines) > 0 {
				// (A command line of several lines, each.)
				_, err = h.Call(ctx, "goda_term#ui#shell#_append", bufnr,
					strings.Split(strings.Join(lines, "\n"), "\n"))
			}
			return false, false, err
		},
		// time command...: and how long it took.
		"time": func(ctx context.Context, h sdk.Host, a term.Args, p Params, args []string) (bool, bool, error) {
			t.mu.Lock()
			t.startTime = time.Now()
			t.mu.Unlock()
			started, err := t.execute(ctx, h, a, p, strings.Join(args, " "))
			return started, started, err
		},
		// vim file: edited.
		"vim": func(ctx context.Context, h sdk.Host, _ term.Args, p Params, args []string) (bool, bool, error) {
			if len(args) == 0 {
				return false, false, nil
			}
			if err := t.newCommandLine(ctx, h, p, ""); err != nil {
				return false, true, err
			}
			_, cwd, _ := t.get()
			path := args[0]
			if !filepath.IsAbs(path) {
				path = filepath.Join(cwd, path)
			}
			if err := h.Cmd(ctx, "execute 'edit' fnameescape("+vimString(path)+")"); err != nil {
				return false, true, err
			}
			return false, true, h.Cmd(ctx, "stopinsert")
		},
		// with words...: the command lines start with them (none: no
		// more).
		"with": func(_ context.Context, _ sdk.Host, _ term.Args, _ Params, args []string) (bool, bool, error) {
			t.mu.Lock()
			t.with = args
			t.mu.Unlock()
			return false, false, nil
		},
	}
}

func str(p term.Params, key string) string {
	s, _ := p[key].(string)
	return s
}

func (t *instance) Actions() map[string]term.Action {
	// onPrompt: in the UI's buffer, with a prompt pattern.
	onPrompt := func(f func(context.Context, sdk.Host, term.Args, Params, term.Params) error) term.Action {
		return func(ctx context.Context, h sdk.Host, a term.Args, ap term.Params) error {
			p, err := params(a)
			if err != nil {
				return err
			}
			if ok, err := t.current(ctx, h); err != nil || !ok || p.PromptPattern == "" {
				return err
			}
			return f(ctx, h, a, p, ap)
		}
	}
	inBuffer := func(f func(context.Context, sdk.Host, term.Args, Params, term.Params) error) term.Action {
		return func(ctx context.Context, h sdk.Host, a term.Args, ap term.Params) error {
			p, err := params(a)
			if err != nil {
				return err
			}
			if ok, err := t.current(ctx, h); err != nil || !ok {
				return err
			}
			return f(ctx, h, a, p, ap)
		}
	}
	anywhere := func(f func(context.Context, sdk.Host, term.Args, Params, term.Params) error) term.Action {
		return func(ctx context.Context, h sdk.Host, a term.Args, ap term.Params) error {
			p, err := params(a)
			if err != nil {
				return err
			}
			return f(ctx, h, a, p, ap)
		}
	}
	search := func(flags string) term.Action {
		return onPrompt(func(ctx context.Context, h sdk.Host, _ term.Args, p Params, _ term.Params) error {
			_, err := h.Call(ctx, "goda_term#ui#_search_prompt", p.PromptPattern, flags)
			return err
		})
	}
	return map[string]term.Action{
		// cd {directory}: the shell goes there.
		"cd": inBuffer(func(ctx context.Context, h sdk.Host, _ term.Args, p Params, ap term.Params) error {
			dir := str(ap, "directory")
			if err := t.newCdPrompt(ctx, h, p, dir); err != nil {
				return err
			}
			return t.cd(ctx, h, p, dir)
		}),
		// executeLine: the command line of the cursor (a directory under
		// it: cd there).
		"executeLine": onPrompt(func(ctx context.Context, h sdk.Host, a term.Args, p Params, _ term.Params) error {
			v, err := h.Call(ctx, "goda_term#ui#shell#_line_info", p.PromptPattern, t.prompt(ctx, h))
			if err != nil {
				return err
			}
			var info struct {
				CommandLine string `json:"commandLine"`
				Cfile       string `json:"cfile"`
				NotLast     bool   `json:"notLast"`
			}
			if err := sdk.Decode(v, &info); err != nil {
				return err
			}
			line := info.CommandLine
			if line == "" {
				line = t.expandDirectory(ctx, h, info.Cfile)
			}
			// A quote not closed, or a backslash at the end: it goes on in
			// the next line, as in a shell.
			if bufnr, _, job := t.get(); job == nil && !info.NotLast && incomplete(line) {
				_, err := h.Call(ctx, "goda_term#ui#shell#_continue", bufnr)
				return err
			}
			if info.NotLast || line == "" {
				// A command line of the history.
				if err := t.newCommandLine(ctx, h, p, line); err != nil {
					return err
				}
			}
			_, err = t.execute(ctx, h, a, p, line)
			return err
		}),
		// insert {str}: typed in the command running, or the command line.
		"insert": anywhere(func(ctx context.Context, h sdk.Host, _ term.Args, p Params, ap term.Params) error {
			if _, _, job := t.get(); job != nil {
				_, err := job.Write([]byte(str(ap, "str")))
				return err
			}
			return t.newCommandLine(ctx, h, p, str(ap, "str"))
		}),
		"nextPrompt": search("Wn"),
		// pastePrompt: the command line of the cursor, in a new prompt.
		"pastePrompt": onPrompt(func(ctx context.Context, h sdk.Host, _ term.Args, p Params, _ term.Params) error {
			line, err := t.commandLine(ctx, h, p)
			if err != nil {
				return err
			}
			return t.newCommandLine(ctx, h, p, line)
		}),
		"previousPrompt": search("bWn"),
		// pushBufferStack: the command line put aside for the next prompt.
		"pushBufferStack": onPrompt(func(ctx context.Context, h sdk.Host, _ term.Args, p Params, _ term.Params) error {
			line, err := t.commandLine(ctx, h, p)
			if err != nil || line == "" {
				return err
			}
			t.mu.Lock()
			stack := append(t.stack, line)
			t.stack = nil
			t.mu.Unlock()
			err = t.newCommandLine(ctx, h, p, "")
			t.mu.Lock()
			t.stack = stack
			t.mu.Unlock()
			return err
		}),
		// redraw: the prompt again.
		// The command line being typed is highlighted (the buffer notifies
		// it as it changes: lines from lnum, the first from byte col, at
		// changedtick, and $PATH).
		"highlightCommandLine": anywhere(func(ctx context.Context, h sdk.Host, _ term.Args, p Params, ap term.Params) error {
			var req struct {
				Bufnr       int      `json:"bufnr"`
				Changedtick int      `json:"changedtick"`
				Lnum        int      `json:"lnum"`
				Col         int      `json:"col"`
				Lines       []string `json:"lines"`
				Path        string   `json:"path"`
			}
			if err := sdk.Decode(map[string]any(ap), &req); err != nil {
				return err
			}
			bufnr, cwd, job := t.get()
			if req.Bufnr != bufnr || job != nil {
				return nil
			}
			home, _ := os.UserHomeDir()
			env := hlEnv{cwd: cwd, home: home, path: req.Path, aliases: p.Aliases,
				builtins: map[string]bool{}}
			for name := range t.builtins() {
				env.builtins[name] = true
			}
			spans := commandLineSpans(req.Lines, req.Lnum, req.Col, env, p.CommandLineHighlights)
			_, err := h.Call(ctx, "goda_term#ui#shell#_highlight_command_line", bufnr, req.Changedtick, req.Lnum, spans)
			return err
		}),
		"redraw": inBuffer(func(ctx context.Context, h sdk.Host, _ term.Args, p Params, _ term.Params) error {
			bufnr, _, _ := t.get()
			v, err := h.Call(ctx, "getbufoneline", bufnr, "$")
			if last, _ := v.(string); err == nil && last == p.Prompt+" " {
				return t.newCommandLine(ctx, h, p, "")
			}
			return err
		}),
		// send {str}: executed (noSaveHistory: not written in the
		// history).
		"send": anywhere(func(ctx context.Context, h sdk.Host, a term.Args, p Params, ap term.Params) error {
			p.noSaveHistory, _ = ap["noSaveHistory"].(bool)
			if _, _, job := t.get(); job == nil {
				if err := t.newCommandLine(ctx, h, p, str(ap, "str")); err != nil {
					return err
				}
			}
			_, err := t.execute(ctx, h, a, p, str(ap, "str"))
			return err
		}),
		// setCommandLine {str}: the command line of a new prompt.
		"setCommandLine": anywhere(func(ctx context.Context, h sdk.Host, _ term.Args, p Params, ap term.Params) error {
			return t.newCommandLine(ctx, h, p, str(ap, "str"))
		}),
		// terminate: the command running ends (or a new prompt).
		"terminate": inBuffer(func(ctx context.Context, h sdk.Host, _ term.Args, p Params, _ term.Params) error {
			if _, _, job := t.get(); job != nil {
				return job.Close()
			}
			return t.newCommandLine(ctx, h, p, "")
		}),
	}
}
