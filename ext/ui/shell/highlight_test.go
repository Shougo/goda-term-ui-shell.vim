package shell

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// testEnv has a directory with "file" and "sub/", and a $PATH with the
// executables "mycmd" and "sudo".
func testEnv(t *testing.T) hlEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("executable bits")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	for _, d := range []string{bin, filepath.Join(dir, "sub")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, mode := range map[string]os.FileMode{
		filepath.Join(bin, "mycmd"): 0o755, filepath.Join(bin, "sudo"): 0o755,
		filepath.Join(dir, "file"): 0o644, filepath.Join(dir, "run.sh"): 0o755,
	} {
		if err := os.WriteFile(path, nil, mode); err != nil {
			t.Fatal(err)
		}
	}
	return hlEnv{cwd: dir, home: dir, path: bin, aliases: map[string]string{"ll": "ls -l"},
		builtins: map[string]bool{"cd": true, "time": true}}
}

// kinds are the spans of line as "text:kind".
func kinds(line string, env hlEnv) []string {
	var out []string
	for _, s := range highlightSpans(line, env) {
		out = append(out, line[s.start:s.end]+":"+s.kind)
	}
	return out
}

func TestHighlightSpans(t *testing.T) {
	env := testEnv(t)
	for line, want := range map[string][]string{
		"mycmd -v --all file nofile": {"mycmd:command", "-v:option", "--all:option", "file:path"},
		"nocmd x":                    {"nocmd:unknown"},
		"cd sub":                     {"cd:builtin", "sub:path"},
		"ll":                         {"ll:alias"},
		"./run.sh":                   {"./run.sh:command"},
		"./sub/":                     {"./sub/:directory"},
		"./nothere":                  {"./nothere:unknown"},
		"mycmd ~/file":               {"mycmd:command", "~/file:path"},
		"~/file":                     {"~/file:unknown"},
		`mycmd 'a b' "x $HOME \n" ${V}`: {"mycmd:command", "'a b':singleQuote",
			`"x $HOME \n":doubleQuote`, "$HOME:variable", `\n:escape`, "${V}:variable"},
		`mycmd *.go a\ b`:       {"mycmd:command", "*:glob", `\ :escape`},
		"FOO=1 BAR='x' mycmd":   {"FOO=:assignment", "BAR=:assignment", "mycmd:command", "'x':singleQuote"},
		"time mycmd -x":         {"time:builtin", "mycmd:command", "-x:option"},
		"sudo -u root mycmd":    {"sudo:precommand", "-u:option", "mycmd:command"},
		`mycmd "unclosed`:       {"mycmd:command", `"unclosed:doubleQuote`},
		"mycmd -m \"a\n\n- b\"": {"mycmd:command", "-m:option", "\"a\n\n- b\":doubleQuote"},
		"mycmd a \\\n  file":    {"mycmd:command", "file:path"},
		"":                      nil,
		"   ":                   nil,
		`$EDITOR file`:          {"file:path", "$EDITOR:variable"},
	} {
		if got := kinds(line, env); !reflect.DeepEqual(got, want) {
			t.Errorf("%q:\n got %q\nwant %q", line, got, want)
		}
	}
}

// The spans of the lines of the buffer: after the prompt in the first.
func TestCommandLineSpans(t *testing.T) {
	env := testEnv(t)
	groups := DefaultCommandLineHighlights()
	groups["option"] = ""
	got := commandLineSpans([]string{`% mycmd -m "a`, `b" file`}, 10, 3, env, groups)
	want := []lineSpan{
		{Lnum: 10, Col: 3, Length: 5, Group: "Function", Name: "GodaTermShellCommandLine_command", Priority: 40},
		{Lnum: 2 + 9, Col: 4, Length: 4, Group: "Underlined", Name: "GodaTermShellCommandLine_path", Priority: 40},
		{Lnum: 10, Col: 12, Length: 2, Group: "String", Name: "GodaTermShellCommandLine_doubleQuote", Priority: 50},
		{Lnum: 11, Col: 1, Length: 2, Group: "String", Name: "GodaTermShellCommandLine_doubleQuote", Priority: 50},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if got := commandLineSpans([]string{"%"}, 1, 3, env, groups); got != nil {
		t.Errorf("no command line: %+v", got)
	}
}
