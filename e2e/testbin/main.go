// The binary of the end-to-end tests: the UI shell.
package main

import (
	"github.com/Shougo/goda-term-ui-shell.vim/ext/ui/shell"
	"github.com/Shougo/goda-term.vim/term"
)

func main() {
	b := term.New()
	b.AddUI(shell.New())
	b.Main()
}
