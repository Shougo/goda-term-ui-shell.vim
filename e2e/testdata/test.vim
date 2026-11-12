" The end-to-end scenario: the UI shell in a real Vim or Neovim. Failures go
" to $GODA_TERM_RESULT; it says "ok" at the end.

set nocompatible
set encoding=utf-8
set nomore
syntax on
let s:errors = []
let s:result = $GODA_TERM_RESULT

execute 'set runtimepath^=' . fnameescape($GODA_VIM_DIR)
execute 'set runtimepath^=' . fnameescape($GODA_TERM_DIR)
execute 'set runtimepath^=' . fnameescape($GODA_TERM_UI_DIR)
let g:goda_term#binary = $GODA_TERM_BINARY
" goda-term sets it.
let $EDITOR = ''

function! s:check(name, got, want) abort
  if a:got !=# a:want
    call add(s:errors, printf('%s: got %s, want %s', a:name, string(a:got), string(a:want)))
  endif
endfunction

" Wait for {cond} (an expression) to be true, 5 seconds at most: the job
" writes when it gets to it.
function! s:wait(cond, bufnr = bufnr('%')) abort
  for _ in range(250)
    if eval(a:cond)
      return v:true
    endif
    if has('nvim') || getbufvar(a:bufnr, '&buftype') !=# 'terminal'
      sleep 20m
    else
      call term_wait(a:bufnr, 20)
    endif
  endfor
  let lines = getbufvar(a:bufnr, '&buftype') ==# 'terminal' ? s:lines('') : getbufline(a:bufnr, 1, '$')
  " (Is the job of the terminal running?)
  let job = has('nvim') ? getbufvar(a:bufnr, 'terminal_job_id', -1) : -1
  let status = job > 0 ? ' job ' . job . ' ' . string(jobwait([job], 0)) : ''
  call add(s:errors, 'timed out: ' . a:cond . ' ' . string(lines) . status)
  return v:false
endfunction

" The lines of the job: Vim shows them in the buffer in Terminal-Job mode
" only (its screen has them meanwhile).
function! s:lines(pattern) abort
  if has('nvim')
    let lines = getbufline(s:buf, 1, '$')
  else
    let lines = map(range(1, term_getsize(s:buf)[0]), {_, r -> term_getline(s:buf, r)})
  endif
  return filter(lines, {_, l -> l =~# a:pattern})
endfunction

" Vim: the buffer as the job's screen is.
function! s:refresh() abort
  if !has('nvim') && bufnr('%') == s:buf
    call feedkeys("\<C-\>\<C-n>i\<C-\>\<C-n>", 'xt')
  endif
endfunction

" The error messages since the last call.
function! s:errmsgs() abort
  let msgs = split(execute('messages'), "\n")
  let new = msgs[get(s:, 'nmsgs', 0) :]
  let s:nmsgs = len(msgs)
  return join(new, "\n")
endfunction

" The UI shell: its buffer with the prompts, and the output of the commands.
function! s:shell() abort
  let history = tempname()
  let dir = resolve(fnamemodify(tempname(), ':p'))
  call mkdir(dir, 'p')
  highlight TestRed ctermfg=1 guifg=Red
  highlight TestBold cterm=bold gui=bold ctermfg=2 guifg=Green
  call goda_term#custom#patch_local('sh', #{uiParams: #{shell: #{
        \ shellHistoryPath: history,
        \ aliases: #{greet: 'echo aliased'},
        \ ansiColorHighlights: #{fgs: repeat(['TestRed'], 16), bold: 'TestBold'},
        \ userPrompt: '"| user"',
        \ noSaveHistoryCommands: ['history'],
        \ }}})
  enew
  call goda_term#start(#{name: 'sh', ui: 'shell'})
  let s:buf = bufnr('%')
  call s:check('shell filetype', &filetype, 'goda-term-shell')
  call s:check('shell prompt', getline(1, '$'), ['| user', '% '])
  call s:check('shell variables', [b:goda_term_name, t:goda_term_ui_shell_last_name], ['sh', 'sh'])

  if has('win32')
    " A command in ConPTY, and its output.
    call goda_term#ui#do_action('send', #{str: 'cmd /c echo hello'})
    call s:wait('index(getline(1, "$"), "hello") >= 0 && getline("$") ==# "% "')
  else
    call goda_term#ui#do_action('send', #{str: 'echo hello'})
    call s:wait('getline("$") ==# "% " && index(getline(1, "$"), "hello") >= 0')
    call s:check('history', readfile(history), ['echo hello'])
    call goda_term#ui#do_action('send', #{str: 'echo secret', noSaveHistory: v:true})
    call s:wait('getline("$") ==# "% " && index(getline(1, "$"), "secret") >= 0')
    call s:check('noSaveHistory', readfile(history), ['echo hello'])

    " The colors of the output.
    " (The color is over bold, whose group has a color too.)
    call goda_term#ui#do_action('send', #{str: "printf '\033[31mred\033[0m \033[1mbold\033[0m \033[1;31mboth\033[0m\n'"})
    call s:wait('index(getline(1, "$"), "red bold both") >= 0 && getline("$") ==# "% "')
    let lnum = index(getline(1, '$'), 'red bold both') + 1
    call s:check('colors', sort(s:highlights(lnum), {a, b -> a[0] != b[0] ? a[0] - b[0] : a[2] ># b[2] ? 1 : -1}),
          \ [[1, 3, 'TestRed', 30], [5, 4, 'TestBold', 10], [10, 4, 'TestBold', 10], [10, 4, 'TestRed', 30]])

    " An alias, an error, and one that is not there.
    call goda_term#ui#do_action('send', #{str: 'greet'})
    call s:wait('index(getline(1, "$"), "aliased") >= 0 && getline("$") ==# "% "')
    call goda_term#ui#do_action('send', #{str: 'false'})
    call s:wait('index(getline(1, "$"), "goda-term: exit 1") >= 0 && getline("$") ==# "% "')
    " Nothing written: the next prompt where its output would be.
    let lnum = index(getline(1, '$'), 'goda-term: exit 1') + 1
    call s:check('no output', getline(lnum - 1), '% false')
    call s:errmsgs()
    call goda_term#ui#do_action('send', #{str: 'no-such-command-x'})
    call s:check('not executable', s:errmsgs() =~# 'not an executable', v:true)

    " The builtin cd.
    call goda_term#ui#do_action('send', #{str: 'cd ' . dir})
    call s:check('cd', [getcwd(), t:goda_term_ui_last_directory], [dir, dir])
    call goda_term#ui#do_action('send', #{str: 'pwd'})
    call s:wait('index(getline(1, "$"), ' . string(dir) . ') >= 0 && getline("$") ==# "% "')

    " The command line: set, executed, put aside.
    call goda_term#ui#do_action('setCommandLine', #{str: 'echo again'})
    call s:check('setCommandLine', getline('$'), '% echo again')
    call cursor('$', 1)
    call s:check('get_input', goda_term#ui#get_input(), '')
    call goda_term#ui#do_action('executeLine')
    call s:wait('index(getline(1, "$"), "again") >= 0 && getline("$") ==# "% "')
    call goda_term#ui#do_action('setCommandLine', #{str: 'pushed'})
    call goda_term#ui#do_action('pushBufferStack')
    call s:check('pushBufferStack', getline('$'), '% ')
    call goda_term#ui#do_action('send', #{str: 'echo x'})
    call s:wait('getline("$") ==# "% pushed"')

    " A command line of several lines. A quote not closed: it goes on in the
    " next line (what is typed there stays), and the lines are one command.
    call goda_term#ui#do_action('setCommandLine', #{str: 'echo "a'})
    call goda_term#ui#do_action('executeLine')
    call s:check('continued', getline(line('$') - 1, '$'), ['% echo "a', ''])
    call feedkeys("Ab\"\<Esc>", 'xt')
    call s:check('typed in the next line', getline('$'), 'b"')
    call goda_term#ui#do_action('executeLine')
    call s:wait('getline("$") ==# "% "')
    call s:check('several lines', getline(line('$') - 5, line('$') - 2), ['% echo "a', 'b"', 'a', 'b'])
    let h = readfile(history)
    call s:check('history of several lines', h[index(h, 'echo "a\') :][: 1], ['echo "a\', 'b"'])
    " Pasted (an empty line in it).
    call goda_term#ui#do_action('setCommandLine', #{str: "printf '%s|' \"tag\n\n- faster\""})
    call s:check('pasted', getline(line('$') - 2, '$'), ["% printf '%s|' \"tag", '', '- faster"'])
    call goda_term#ui#do_action('executeLine')
    call s:wait('getline("$") ==# "% " && index(getline(1, "$"), "- faster|") >= 0')
    let lnum = index(getline(1, '$'), '- faster|') + 1
    call s:check('pasted output', getline(lnum - 2, lnum), ['tag', '', '- faster|'])
    " A new line out of quotes: a second command, not run.
    call s:errmsgs()
    call goda_term#ui#do_action('setCommandLine', #{str: "echo 1\necho 2"})
    call goda_term#ui#do_action('executeLine')
    call s:check('several commands', s:errmsgs() =~# 'one at a time', v:true)
    call s:check('several commands prompt', getline('$'), '% ')

    " A command that goes on: terminated.
    call goda_term#ui#do_action('send', #{str: 'sleep 30'})
    sleep 200m
    " Before it writes anything, a new line shows that it runs, the cursor
    " there (as in a terminal).
    call s:check('running', [getline(line('$') - 1, '$'), line('.')],
          \ [['% sleep 30', ''], line('$')])
    call goda_term#ui#do_action('terminate')
    call s:wait('getline("$") ==# "% "')
    " What it ends with is on that line, then the next prompt.
    call s:check('after running', getline(line('$') - 3, '$'),
          \ ['% sleep 30', 'goda-term: exit -1', '| user', '% '])

    " The builtins with and time.
    call goda_term#ui#do_action('send', #{str: 'with echo'})
    call s:check('with', getline('$'), '% echo ')
    call goda_term#ui#do_action('send', #{str: 'with'})
    call s:check('with ended', getline('$'), '% ')
    call goda_term#ui#do_action('send', #{str: 'time echo t'})
    call s:wait('match(getline(1, "$"), ''^goda-term: \d\+ ms$'') >= 0 && getline("$") ==# "% "')
    call s:check('history of time', index(readfile(history), 'time echo t') >= 0, v:true)
  endif

  " Shown again: the same buffer.
  wincmd n
  call goda_term#start(#{name: 'sh', ui: 'shell'})
  call s:check('shell again', bufnr('%'), s:buf)
endfunction

" The highlights of the line {lnum}: [[col, length, group]].
function! s:highlights(lnum) abort
  if has('nvim')
    let ns = nvim_get_namespaces()['goda-term-ui-shell']
    return map(nvim_buf_get_extmarks(0, ns, [a:lnum - 1, 0], [a:lnum - 1, -1], #{details: v:true}),
          \ {_, m -> [m[2] + 1, m[3].end_col - m[2], m[3].hl_group, m[3].priority]})
  endif
  return map(prop_list(a:lnum), {_, p -> [p.col, p.length]
        \ + map(['highlight', 'priority'], {_, k -> prop_type_get(p.type, #{bufnr: bufnr('%')})[k]})})
endfunction

try
  call s:shell()
catch
  call add(s:errors, v:throwpoint . ': ' . v:exception)
endtry
call writefile(s:errors + (empty(s:errors) ? ['ok'] : []) + goda#log(), s:result)
call goda#stop()
qall!
