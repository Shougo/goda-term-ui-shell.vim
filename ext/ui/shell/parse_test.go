package shell

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestSplitArgs(t *testing.T) {
	for in, want := range map[string][]string{
		"ls -la":                                {"ls", "-la"},
		`echo "Hello World"`:                    {"echo", "Hello World"},
		"echo 'Hello World'":                    {"echo", "Hello World"},
		"  ls    -la   ":                        {"ls", "-la"},
		"":                                      nil,
		`echo "foo\"bar"`:                       {"echo", `foo"bar`},
		`echo 'don\'t'`:                         {"echo", "don't"},
		`echo "foo'bar" 'baz"qux'`:              {"echo", "foo'bar", `baz"qux`},
		`echo "\\"`:                             {"echo", `\`},
		`echo "\\\\"`:                           {"echo", `\\`},
		`echo foo\ bar`:                         {"echo", "foo bar"},
		`cmd "arg with \"quotes\"" 'single\'s'`: {"cmd", `arg with "quotes"`, "single's"},
		`echo "" ''`:                            {"echo", "", ""},
	} {
		got, err := splitArgs(in)
		if err != nil || len(got) != len(want) || len(want) > 0 && !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %q %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{`echo "unclosed`, `echo "foo\`, "echo 'unclosed", `echo 'foo\`} {
		if _, err := splitArgs(in); err == nil {
			t.Errorf("%s: no error", in)
		}
	}
}

func TestParseCommandLine(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"HOGE": "world", "HOME": tmp}
	getenv := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	for _, tc := range []struct {
		in   string
		env  map[string]string
		args []string
	}{
		{"arg1 arg2", map[string]string{}, []string{"arg1", "arg2"}},
		{"FOO=bar echo file.txt", map[string]string{"FOO": "bar"}, []string{"echo", "file.txt"}},
		{`$BAZ="qux quux" cmd --opt`, map[string]string{"BAZ": "qux quux"}, []string{"cmd", "--opt"}},
		{`FOO=foo\ bar run`, map[string]string{"FOO": "foo bar"}, []string{"run"}},
		{"A=1 B=2 -- cmd", map[string]string{"A": "1", "B": "2"}, []string{"--", "cmd"}},
		{"echo $HOGE", map[string]string{}, []string{"echo", "world"}},
		{`echo "${HOGE}"`, map[string]string{}, []string{"echo", "world"}},
		{"echo '$HOGE'", map[string]string{}, []string{"echo", "$HOGE"}},
		{"echo foo$HOGEbar", map[string]string{}, []string{"echo", "fooworldbar"}},
		{"HOGE=here echo $HOGE", map[string]string{"HOGE": "here"}, []string{"echo", "here"}},
		{"echo ~/file.txt", map[string]string{}, []string{"echo", tmp + "/file.txt"}},
		{"echo ${bad-name} $", map[string]string{}, []string{"echo", "${bad-name}", "$"}},
	} {
		p, err := parseCommandLine(t.TempDir(), tc.in, getenv)
		if err != nil || !reflect.DeepEqual(p.env, tc.env) || !reflect.DeepEqual(p.args, tc.args) {
			t.Errorf("%s: %q %q %v", tc.in, p.env, p.args, err)
		}
	}
	// Globs, relative to the directory.
	for _, f := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(tmp, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := parseCommandLine(tmp, "ls *.txt nomatch*", getenv)
	if err != nil {
		t.Fatal(err)
	}
	files := slices.Sorted(slices.Values(p.args[1:3]))
	if p.args[0] != "ls" || !slices.Equal(files, []string{"a.txt", "b.txt"}) || p.args[3] != "nomatch*" {
		t.Errorf("glob: %q", p.args)
	}
}

// A command line goes on in the next line when a quote is not closed or it
// ends with a backslash; a new line out of quotes is a second command.
func TestIncomplete(t *testing.T) {
	for in, want := range map[string]bool{
		`echo "a`:                 true,
		"echo 'a":                 true,
		`echo a\`:                 true,
		"echo \"a\nb\"":           false,
		"echo \"a\nb":             true,
		"echo a \\\nb":            false,
		"echo a":                  false,
		`echo "a\"`:               true,
		"git tag -m \"x\n\n- y\"": false,
	} {
		if got := incomplete(in); got != want {
			t.Errorf("incomplete(%q) = %v, want %v", in, got, want)
		}
	}
	got, err := splitArgs("git tag -a v0.2.0 -m \"ng v0.2.0\n\n- Start faster\"")
	if want := []string{"git", "tag", "-a", "v0.2.0", "-m", "ng v0.2.0\n\n- Start faster"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("several lines: %q %v, want %q", got, err, want)
	}
	if got, err := splitArgs("echo a \\\nb"); err != nil || !slices.Equal(got, []string{"echo", "a", "b"}) {
		t.Errorf("backslash at the end: %q %v", got, err)
	}
	if _, err := splitArgs("echo 1\necho 2"); !errors.Is(err, errSeveralLines) {
		t.Errorf("two commands: %v", err)
	}
}
