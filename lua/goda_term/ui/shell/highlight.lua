-- The highlights of the output of a command on Neovim, for
-- goda_term#ui#shell#_flush(): the loop over them in Lua (Vim has
-- autoload/goda_term/ui/shell/highlight.vim).

local M = {}

--- Add the highlights of the lines of the output from line lnum.
-- As s:highlight() of autoload/goda_term/ui/shell.vim for each: a column
-- past the end of its line is skipped, a length of 0 or past the end goes to
-- the end.
--
-- @param bufnr      buffer
-- @param ns         namespace of goda-term-ui-shell
-- @param lnum       line of lines[1]
-- @param lines      the lines, as set in the buffer
-- @param highlights list of {row (from 0), col, length, group, name, priority}
-- @return list of the groups that do not exist (in the order found)
function M.add(bufnr, ns, lnum, lines, highlights)
  local valid = {}
  local missing = {}
  local set_extmark = vim.api.nvim_buf_set_extmark
  for _, h in ipairs(highlights) do
    local row, col, length, group, priority = h[1], h[2], h[3], h[4], h[6]
    local ok = valid[group]
    if ok == nil then
      ok = vim.fn.hlexists(group) == 1
      valid[group] = ok
      if not ok then
        missing[#missing + 1] = group
      end
    end
    if ok then
      local line = lines[row + 1]
      if line == nil then
        line = vim.api.nvim_buf_get_lines(bufnr, lnum + row - 1, lnum + row, false)[1] or ''
      end
      local max_col = #line
      if col <= max_col then
        if length == 0 or col + length > max_col then
          length = max_col - col + 1
        end
        set_extmark(bufnr, ns, lnum + row - 1, col - 1, {
          end_col  = col - 1 + length,
          hl_group = group,
          priority = priority,
        })
      end
    end
  end
  return missing
end

return M
