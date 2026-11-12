" The Vim script of the UI "shell": a buffer with a prompt, and the output
" of the commands.

let s:namespace = has('nvim') ? nvim_create_namespace('goda-term-ui-shell') : 0

" The params whose values are expressions (exprParams), evaluated: with
" uiParams, the params as they were given.
function! goda_term#ui#shell#_eval_params(params) abort
  let uiParams = deepcopy(a:params)
  let params = deepcopy(a:params)
  for name in get(a:params, 'exprParams', [])
    if type(get(params, name, 0)) == v:t_string
      try
        let params[name] = eval(params[name])
      catch
        echohl ErrorMsg
        echomsg 'goda-term: invalid expression in the param ' . name . ': ' . v:exception
        echohl None
      endtry
    endif
  endfor
  return params
endfunction

" A new buffer of the session {name} in the window of {params}: its number.
function! goda_term#ui#shell#_new(name, params) abort
  call goda_term#ui#_split(a:params)
  let prev = bufnr('%')
  let bufnr = bufadd('goda-term-shell-' . a:name)
  execute 'buffer' bufnr
  " The empty buffer it was is gone.
  if a:params.split ==# '' && bufname(prev) ==# '' && bufexists(prev)
        \ && !getbufvar(prev, '&modified') && prev != bufnr
    execute 'silent! bwipeout!' prev
  endif
  if a:params.startInsert
    startinsert
  endif
  let winid = win_getid()
  for [name, value] in items(#{list: 0, foldenable: 0, number: 0,
        \ relativenumber: 0, spell: 0, wrap: 0, colorcolumn: '',
        \ foldcolumn: 0, signcolumn: 'no'})
    call setwinvar(winid, '&' . name, value)
  endfor
  if exists('+statuscolumn')
    call setwinvar(winid, '&statuscolumn', '')
  endif
  setlocal buftype=nofile bufhidden=hide noswapfile nomodified
  augroup goda-term-shell
    autocmd! * <buffer>
    autocmd CursorMovedI,TextChangedI,TextChangedP,InsertEnter <buffer>
          \ call goda_term#ui#shell#_check_prompt()
  augroup END
  setlocal filetype=goda-term-shell
  return bufnr
endfunction

" The buffer {bufnr} of the session again: its last line.
function! goda_term#ui#shell#_switch(params, bufnr) abort
  call goda_term#ui#_split(a:params)
  execute 'buffer' a:bufnr
  return getbufoneline(a:bufnr, '$')
endfunction

" A new prompt (the user prompt above it), with {command_line} typed.
function! goda_term#ui#shell#_new_command_line(bufnr, params, command_line) abort
  let prompt = a:params.prompt . ' '
  call setbufvar(a:bufnr, 'goda_term_ui_shell_prompt', prompt)
  let user_prompts = a:params.userPrompt ==# '' ? []
        \ : split(eval(a:params.userPrompt), "\n")
  let lines = getbufline(a:bufnr, 1, '$')
  let last = empty(lines) ? '' : lines[-1]
  if last ==# prompt
    " The user prompt of the prompt replaced goes too.
    let pos = -1
    let check = len(lines) - 1
    while check > 0 && lines[check - 1] =~# '^' . a:params.userPromptPattern
      let pos = check
      let check -= 1
    endwhile
    if pos > 0
      call deletebufline(a:bufnr, pos + 1, '$')
    endif
  endif
  let prompt_lines = user_prompts + [prompt . a:command_line]
  if last ==# '' || last ==# prompt
    call setbufline(a:bufnr, '$', prompt_lines)
  else
    call appendbufline(a:bufnr, '$', prompt_lines)
  endif
  call setbufvar(a:bufnr, '&modified', 0)
  let lnum = len(getbufline(a:bufnr, 1, '$'))
  let row = lnum - 1
  for _ in user_prompts
    call s:highlight(a:params.userPromptHighlight, 'userPrompt', 1, a:bufnr, row, 1, 0)
    let row -= 1
  endfor
  call s:highlight(a:params.promptHighlight, 'prompt', 1, a:bufnr, lnum, 1,
        \ len(a:params.prompt))
  call goda_term#ui#shell#_move_cursor_last(a:bufnr)
  " Also when the output of a command ended (no key, no redraw).
  redraw
endfunction

function! goda_term#ui#shell#_move_cursor_last(bufnr) abort
  let winid = get(win_findbuf(a:bufnr), 0, -1)
  if winid < 0
    stopinsert
    return
  endif
  call win_execute(winid, 'call cursor(line("$"), col([line("$"), "$"]) + 1)')
  " Scrolled to the side for a long command line: back, when the prompt
  " fits.
  call win_execute(winid, 'if virtcol(".") < winwidth(0) | call winrestview(#{leftcol: 0}) | endif')
endfunction

" The command line of the cursor: its line without the prompt ({prompt},
" the last one, when the line is not one of {pattern}), or '' when it is not
" a prompt line.
function! goda_term#ui#shell#_command_line(pattern, prompt) abort
  let line = getline('.')
  if line !~# a:pattern && line('.') != line('$')
    return ''
  endif
  let sub = substitute(line, a:pattern, '', '')
  return sub ==# line ? line[len(a:prompt) :] : sub
endfunction

" For executeLine: the command line, the file under the cursor, and whether
" the cursor is not on the last line.
function! goda_term#ui#shell#_line_info(pattern, prompt) abort
  return #{commandLine: goda_term#ui#shell#_command_line(a:pattern, a:prompt),
        \ cfile: expand('<cfile>'), notLast: line('.') != line('$') ? v:true : v:false}
endfunction

" What is typed before the cursor.
function! goda_term#ui#shell#_input(pattern, prompt) abort
  let line = goda_term#ui#shell#_command_line(a:pattern, a:prompt)
  let end = col('.') - (mode() ==# 'n' ? 2 : 3)
  return end < 0 ? '' : line[: end]
endfunction

" The output lines from {lnum} (replaced), with their highlights: [line
" (from 0), col, length, group, name, priority].
function! goda_term#ui#shell#_flush(bufnr, lnum, lines, highlights) abort
  if !bufexists(a:bufnr)
    return
  endif
  let end = a:lnum + len(a:lines) - 1
  if has('nvim')
    call nvim_buf_clear_namespace(a:bufnr, s:namespace, a:lnum - 1, end)
  elseif !empty(prop_type_list(#{bufnr: a:bufnr}))
    call prop_clear(a:lnum, min([end, getbufinfo(a:bufnr)[0].linecount]), #{bufnr: a:bufnr})
  endif
  call setbufline(a:bufnr, a:lnum, a:lines)
  for [row, col, length, group, name, priority] in a:highlights
    call s:highlight(group, name, priority, a:bufnr, a:lnum + row, col, length)
  endfor
  call setbufvar(a:bufnr, 'goda_term_ui_shell_prompt', getbufoneline(a:bufnr, '$'))
  call setbufvar(a:bufnr, '&modified', 0)
  call goda_term#ui#shell#_move_cursor_last(a:bufnr)
  redraw
endfunction

" A command started: a new line under its prompt, as a terminal makes, so
" that it shows that it runs also before it writes anything. Its output
" goes there (and what is typed there goes to it); the next prompt does,
" when it writes nothing.
function! goda_term#ui#shell#_running(bufnr) abort
  call appendbufline(a:bufnr, '$', '')
  call setbufvar(a:bufnr, 'goda_term_ui_shell_prompt', '')
  call setbufvar(a:bufnr, '&modified', 0)
  call goda_term#ui#shell#_move_cursor_last(a:bufnr)
  redraw
endfunction

" A message after the output: it is the prompt, for the input typed after
" it. On the last line when it is empty (the line of the output of a command
" that wrote nothing on it), as a terminal writes.
function! goda_term#ui#shell#_message(bufnr, msg) abort
  if getbufoneline(a:bufnr, '$') ==# ''
    call setbufline(a:bufnr, '$', a:msg)
  else
    call appendbufline(a:bufnr, '$', a:msg)
  endif
  call setbufvar(a:bufnr, 'goda_term_ui_shell_prompt', a:msg)
  call goda_term#ui#shell#_move_cursor_last(a:bufnr)
endfunction

function! goda_term#ui#shell#_append(bufnr, lines) abort
  call appendbufline(a:bufnr, '$', a:lines)
endfunction

" The directory {dir} is the current one.
function! goda_term#ui#shell#_cd(dir) abort
  let t:goda_term_ui_last_directory = a:dir
  call chdir(a:dir)
  doautocmd <nomodeline> DirChanged
endfunction

" The width of the window of {bufnr}, for the size of the terminal.
function! goda_term#ui#shell#_width(bufnr) abort
  let winid = get(win_findbuf(a:bufnr), 0, -1)
  return winid < 0 ? &columns : winwidth(winid)
endfunction

" Keep the cursor after the prompt, and the prompt on the last line.
function! goda_term#ui#shell#_check_prompt() abort
  if !exists('b:goda_term_ui_shell_prompt_pattern')
    return
  endif
  let line = getline('.')
  if line ==# ''
    return
  endif
  if line('$') == line('.')
    let prompt = get(b:, 'goda_term_ui_shell_prompt', '')
    if prompt ==# '' || stridx(line, prompt) == 0
      return
    endif
    call setline('.', prompt)
    startinsert!
    return
  endif
  let pattern = '^' . b:goda_term_ui_shell_prompt_pattern
  if line !~# pattern
    return
  endif
  let end = matchend(line, pattern)
  if col('.') <= end
    call cursor(0, end + 1)
  endif
endfunction

function! s:highlight(group, name, priority, bufnr, row, col, length) abort
  if !hlexists(a:group)
    echohl ErrorMsg
    echomsg printf('goda-term: highlight "%s" does not exist', a:group)
    echohl None
    return
  endif
  if !has('nvim') && empty(prop_type_get(a:name, #{bufnr: a:bufnr}))
    call prop_type_add(a:name, #{bufnr: a:bufnr, highlight: a:group,
          \ priority: a:priority, override: v:true})
  endif
  let max_col = len(getbufoneline(a:bufnr, a:row))
  if a:col > max_col
    return
  endif
  let length = a:length == 0 || a:col + a:length > max_col
        \ ? max_col - a:col + 1 : a:length
  if has('nvim')
    call nvim_buf_set_extmark(a:bufnr, s:namespace, a:row - 1, a:col - 1,
          \ #{end_col: a:col - 1 + length, hl_group: a:group, priority: a:priority})
  else
    call prop_add(a:row, a:col, #{length: length, type: a:name, bufnr: a:bufnr})
  endif
endfunction
