package shell

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseRedirections(t *testing.T) {
	cwd := t.TempDir()
	env := map[string]string{"OUT": "out.log"}
	getenv := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	in := func(name string) string { return filepath.Join(cwd, name) }
	// (With "/": a backslash is an escape on the command line.)
	abs := filepath.ToSlash(in("abs"))
	for _, tc := range []struct {
		in     string
		args   []string
		redirs []redirection
	}{
		{"make > log", []string{"make"}, []redirection{{fd: 1, dup: -1, op: ">", path: in("log")}}},
		{"make >log", []string{"make"}, []redirection{{fd: 1, dup: -1, op: ">", path: in("log")}}},
		{"make >> log", []string{"make"}, []redirection{{fd: 1, dup: -1, op: ">>", path: in("log")}}},
		{"sort < in", []string{"sort"}, []redirection{{fd: 0, dup: -1, op: "<", path: in("in")}}},
		{"make 2>err", []string{"make"}, []redirection{{fd: 2, dup: -1, op: ">", path: in("err")}}},
		{"make 2>>err", []string{"make"}, []redirection{{fd: 2, dup: -1, op: ">>", path: in("err")}}},
		{"make > log 2>&1", []string{"make"}, []redirection{
			{fd: 1, dup: -1, op: ">", path: in("log")}, {fd: 2, dup: 1, op: ">"}}},
		// In their order (a shell does them left to right).
		{"make 2>&1 >log", []string{"make"}, []redirection{
			{fd: 2, dup: 1, op: ">"}, {fd: 1, dup: -1, op: ">", path: in("log")}}},
		{"echo hi >&2", []string{"echo", "hi"}, []redirection{{fd: 1, dup: 2, op: ">"}}},
		// Anywhere among the words, and before the command.
		{"> log echo hi", []string{"echo", "hi"}, []redirection{{fd: 1, dup: -1, op: ">", path: in("log")}}},
		{"A=1 >log env", []string{"env"}, []redirection{{fd: 1, dup: -1, op: ">", path: in("log")}}},
		// A digit in a word is not a descriptor.
		{"echo a2>log", []string{"echo", "a2"}, []redirection{{fd: 1, dup: -1, op: ">", path: in("log")}}},
		{"echo 2 >log", []string{"echo", "2"}, []redirection{{fd: 1, dup: -1, op: ">", path: in("log")}}},
		// The file is expanded as a word.
		{"echo > $OUT", []string{"echo"}, []redirection{{fd: 1, dup: -1, op: ">", path: in("out.log")}}},
		{`echo > "my log"`, []string{"echo"}, []redirection{{fd: 1, dup: -1, op: ">", path: in("my log")}}},
		{"echo > " + abs, []string{"echo"}, []redirection{{fd: 1, dup: -1, op: ">", path: abs}}},
		// Quoted or escaped: words.
		{`echo '>' "a|b" \> \|`, []string{"echo", ">", "a|b", ">", "|"}, nil},
		{"echo a=>b", []string{"echo", "a="}, []redirection{{fd: 1, dup: -1, op: ">", path: in("b")}}},
	} {
		p, err := parseCommandLine(cwd, tc.in, getenv)
		if err != nil || !reflect.DeepEqual(p.args, tc.args) || !reflect.DeepEqual(p.redirs, tc.redirs) {
			t.Errorf("%s:\n got %q %+v %v\nwant %q %+v", tc.in, p.args, p.redirs, err, tc.args, tc.redirs)
		}
	}
}

func TestParseRedirectionErrors(t *testing.T) {
	cwd := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(cwd, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	getenv := func(string) (string, bool) { return "", false }
	for _, tc := range []struct {
		in   string
		want error
	}{
		// parseCommandLine is one command.
		{"ls | grep go", errPipe},
		{"ls |grep go", errPipe},
		{"echo >", errNoFile},
		{"echo > > x", errNoFile},
		{"echo > | x", errNoFile},
		{"echo 3>x", errDescriptor},
		{"echo >&x", errDescriptor},
		{"echo >&", errDescriptor},
		{"cat < *.txt", errAmbiguous},
	} {
		if _, err := parseCommandLine(cwd, tc.in, getenv); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.in, err, tc.want)
		}
	}
}

func TestParsePipeline(t *testing.T) {
	cwd := t.TempDir()
	getenv := func(string) (string, bool) { return "", false }
	in := func(name string) string { return filepath.Join(cwd, name) }
	stages, err := parsePipeline(cwd, "A=1 make 2>&1 | B=2 tee log|grep -v x > out", getenv)
	want := []parsed{
		{env: map[string]string{"A": "1"}, args: []string{"make"}, redirs: []redirection{{fd: 2, dup: 1, op: ">"}}},
		{env: map[string]string{"B": "2"}, args: []string{"tee", "log"}},
		{env: map[string]string{}, args: []string{"grep", "-v", "x"},
			redirs: []redirection{{fd: 1, dup: -1, op: ">", path: in("out")}}},
	}
	if err != nil || !reflect.DeepEqual(stages, want) {
		t.Errorf("got  %+v %v\nwant %+v", stages, err, want)
	}
	for _, s := range []string{"| ls", "ls |", "ls | | cat", "ls || cat"} {
		if _, err := parsePipeline(cwd, s, getenv); err == nil {
			t.Errorf("%q: no error", s)
		}
	}
	if _, err := parsePipeline(cwd, "ls | | cat", getenv); !errors.Is(err, errEmptyStage) {
		t.Errorf("an empty command: %v", err)
	}
}
