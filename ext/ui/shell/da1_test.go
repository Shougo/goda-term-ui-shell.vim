package shell

import "testing"

func TestDA1Scanner(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chunks []string
		want   int
	}{
		{"query", []string{"\x1b[c"}, 1},
		{"query 0", []string{"a\x1b[0cb"}, 1},
		{"two", []string{"\x1b[c\x1b[0c"}, 2},
		{"split after ESC", []string{"a\x1b", "[c"}, 1},
		{"split after [", []string{"\x1b[", "c"}, 1},
		{"split 0", []string{"\x1b[0", "c"}, 1},
		{"split in three", []string{"\x1b", "[", "c"}, 1},
		{"none", []string{"abc", "[c"}, 0},
		{"answer", []string{"\x1b[?61;22c"}, 0},
		{"DA2", []string{"\x1b[>c"}, 0},
		{"SGR", []string{"\x1b[0m"}, 0},
	} {
		var s da1Scanner
		n := 0
		for _, c := range tc.chunks {
			n += s.count(c)
		}
		if n != tc.want {
			t.Errorf("%s: %d queries, want %d", tc.name, n, tc.want)
		}
	}
}
