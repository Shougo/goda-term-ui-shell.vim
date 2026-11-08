package shell

import (
	"reflect"
	"testing"
)

func lines(r *renderer) []string {
	var out []string
	for _, l := range r.lines {
		out = append(out, l.text)
	}
	return out
}

func TestRenderer(t *testing.T) {
	r := newRenderer()
	r.write("plain\r\n\x1b[31mred\x1b[0m \x1b[1;4mbold\x1b[22m under\x1b[m\r\n\r\nlast")
	if got := lines(r); !reflect.DeepEqual(got, []string{"plain", "red bold under", "", "last"}) {
		t.Fatalf("lines %q", got)
	}
	want := []span{{1, 3, "fg", 1}, {5, 4, "bold", 0}, {5, 4, "underline", 0}, {9, 6, "underline", 0}}
	if got := r.lines[1].spans; !reflect.DeepEqual(got, want) {
		t.Errorf("spans %+v", got)
	}
	first, ls := r.take()
	if first != 0 || len(ls) != 4 {
		t.Errorf("take %d %d", first, len(ls))
	}
	// The line not ended yet goes on.
	r.write(" line\n")
	if first, ls := r.take(); first != 3 || len(ls) != 1 || ls[0].text != "last line" {
		t.Errorf("partial: %d %+v", first, ls)
	}
	// Overwritten: \r, CSI G, CSI 2K, CSI A.
	r = newRenderer()
	r.write("10%\r20%\r\n\x1b[0Gx\x1b[0Gy\nab\x1b[2Kcd\nline\n\x1b[Aover\n")
	if got := lines(r); !reflect.DeepEqual(got, []string{"20%", "y", "cd", "over"}) {
		t.Errorf("overwrite %q", got)
	}
	// Progress lines.
	r = newRenderer()
	r.write("Downloading file.tar.gz 10/100\nDownloading file.tar.gz 20/100\ndone\n")
	if got := lines(r); !reflect.DeepEqual(got, []string{"Downloading file.tar.gz 20/100", "done"}) {
		t.Errorf("progress %q", got)
	}
	// Other sequences are dropped; 256 colors under 16 are colors.
	r = newRenderer()
	r.write("\x1b]0;title\x07a\x1b[?25lb\x1b[38;5;9mc\x1b[38;2;1;2;3md\x1b[39me\bf\n")
	if got := lines(r); !reflect.DeepEqual(got, []string{"abcdf"}) {
		t.Errorf("dropped %q", got)
	}
	if got := r.lines[0].spans; !reflect.DeepEqual(got, []span{{3, 1, "fg", 9}}) {
		t.Errorf("256 %+v", got)
	}
}
