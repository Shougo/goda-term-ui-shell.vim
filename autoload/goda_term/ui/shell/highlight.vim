vim9script

# The highlights of the output of a command on Vim, for
# goda_term#ui#shell#_flush(): the loop over them in Vim9 script, the text
# properties of a type added together (Neovim has
# lua/goda_term/ui/shell/highlight.lua).

# Add the highlights of the lines of the output from line lnum of bufnr:
# [row (from 0), col, length, group, name, priority]. As s:highlight() of
# autoload/goda_term/ui/shell.vim for each: a column past the end of its line
# is skipped, a length of 0 or past the end goes to the end. Returns the
# groups that do not exist (in the order found).
export def Add(bufnr: number, lnum: number, lines: list<string>,
    highlights: list<any>): list<string>
  var valid: dict<bool> = {}
  var missing: list<string> = []
  # The ranges of a type, and the [group, priority] of the type.
  var ranges: dict<list<any>> = {}
  var types: dict<list<any>> = {}
  for [row, col, length, group, name, priority] in highlights
    if !has_key(valid, group)
      valid[group] = hlexists(group) != 0
      if !valid[group]
        add(missing, group)
      endif
    endif
    if !valid[group]
      continue
    endif
    var line: string = row < len(lines) ? lines[row]
      : getbufoneline(bufnr, lnum + row)
    var max_col = strlen(line)
    if col > max_col
      continue
    endif
    var size: number = length == 0 || col + length > max_col
      ? max_col - col + 1 : length
    if !has_key(ranges, name)
      ranges[name] = []
      types[name] = [group, priority]
    endif
    add(ranges[name], [lnum + row, col, lnum + row, col + size])
  endfor
  for [name, l] in items(ranges)
    if empty(prop_type_get(name, {bufnr: bufnr}))
      prop_type_add(name, {bufnr: bufnr, highlight: types[name][0],
        priority: types[name][1], override: true})
    endif
    prop_add_list({bufnr: bufnr, type: name}, l)
  endfor
  return missing
enddef
