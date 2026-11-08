# goda-term-ui-shell.vim

The UI `shell` of [goda-term.vim](https://github.com/Shougo/goda-term.vim):
a buffer with a prompt; each command line runs in a pseudo terminal, and
its output (with its colors) is written in the buffer (ddt-ui-shell).

## Requirements

- [goda-term.vim](https://github.com/Shougo/goda-term.vim)

## Usage

Add it to the configuration of goda-term.vim:

```go
import "github.com/Shougo/goda-term-ui-shell.vim/ext/ui/shell"

b.AddUI(shell.New())
```

Then:

```vim
call goda_term#start(#{name: 'shell', ui: 'shell'})
```

See `:help goda-term-ui-shell`.
