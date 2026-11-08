package pty

import (
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// ConPTY (MS-Windows 10 1809 and later): the command runs in a pseudo
// console, which reads its input from a pipe and writes what it shows
// (with escape sequences) to another. Without it, pipes.

var (
	kernel32                        = syscall.NewLazyDLL("kernel32.dll")
	procCreatePseudoConsole         = kernel32.NewProc("CreatePseudoConsole")
	procResizePseudoConsole         = kernel32.NewProc("ResizePseudoConsole")
	procClosePseudoConsole          = kernel32.NewProc("ClosePseudoConsole")
	procInitializeProcThreadAttList = kernel32.NewProc("InitializeProcThreadAttributeList")
	procUpdateProcThreadAttribute   = kernel32.NewProc("UpdateProcThreadAttribute")
	procDeleteProcThreadAttList     = kernel32.NewProc("DeleteProcThreadAttributeList")
)

const (
	procThreadAttributePseudoConsole = 0x00020016
	extendedStartupInfoPresent       = 0x00080000
	createUnicodeEnvironment         = 0x00000400
	startfUseStdHandles              = 0x00000100
)

// startupInfoEx is STARTUPINFOEXW.
type startupInfoEx struct {
	syscall.StartupInfo
	attributeList *byte
}

// console is the pseudo console of a command.
type console struct {
	handle uintptr
	once   sync.Once
}

func (c *console) close() {
	c.once.Do(func() { procClosePseudoConsole.Call(c.handle) })
}

func hasConPTY() bool { return procCreatePseudoConsole.Find() == nil }

// coord is a COORD passed by value.
func coord(rows, cols int) uintptr {
	return uintptr(uint32(uint16(int16(cols))) | uint32(uint16(int16(rows)))<<16)
}

func start(p *Pty, path string, o Options) error {
	if !hasConPTY() {
		return startPipes(p, path, o)
	}
	var inR, inW, outR, outW syscall.Handle
	if err := syscall.CreatePipe(&inR, &inW, nil, 0); err != nil {
		return err
	}
	if err := syscall.CreatePipe(&outR, &outW, nil, 0); err != nil {
		syscall.CloseHandle(inR)
		syscall.CloseHandle(inW)
		return err
	}
	closeAll := func(hs ...syscall.Handle) {
		for _, h := range hs {
			syscall.CloseHandle(h)
		}
	}
	c := &console{}
	if r, _, _ := procCreatePseudoConsole.Call(coord(o.Rows, o.Cols), uintptr(inR), uintptr(outW), 0,
		uintptr(unsafe.Pointer(&c.handle))); r != 0 {
		closeAll(inR, inW, outR, outW)
		return syscall.Errno(r)
	}
	// The pseudo console has its own of these.
	closeAll(inR, outW)
	fail := func(err error) error {
		c.close()
		closeAll(inW, outR)
		return err
	}

	var size uintptr
	procInitializeProcThreadAttList.Call(0, 1, 0, uintptr(unsafe.Pointer(&size)))
	if size == 0 {
		return fail(errors.New("pty: InitializeProcThreadAttributeList"))
	}
	list := make([]byte, size)
	if r, _, err := procInitializeProcThreadAttList.Call(uintptr(unsafe.Pointer(&list[0])), 1, 0,
		uintptr(unsafe.Pointer(&size))); r == 0 {
		return fail(err)
	}
	defer procDeleteProcThreadAttList.Call(uintptr(unsafe.Pointer(&list[0])))
	if r, _, err := procUpdateProcThreadAttribute.Call(uintptr(unsafe.Pointer(&list[0])), 0,
		procThreadAttributePseudoConsole, c.handle, unsafe.Sizeof(c.handle), 0, 0); r == 0 {
		return fail(err)
	}

	si := startupInfoEx{attributeList: &list[0]}
	si.Cb = uint32(unsafe.Sizeof(si))
	// Not the standard handles of this process (the pipes of Vim): those
	// of the pseudo console.
	si.Flags = startfUseStdHandles
	cmdline := syscall.EscapeArg(path)
	for _, a := range o.Args {
		cmdline += " " + syscall.EscapeArg(a)
	}
	cl, err := syscall.UTF16PtrFromString(cmdline)
	if err != nil {
		return fail(err)
	}
	var dir *uint16
	if o.Dir != "" {
		if dir, err = syscall.UTF16PtrFromString(o.Dir); err != nil {
			return fail(err)
		}
	}
	env := envBlock(o.Env)
	var pi syscall.ProcessInformation
	err = syscall.CreateProcess(nil, cl, nil, nil, false,
		extendedStartupInfoPresent|createUnicodeEnvironment, &env[0], dir, &si.StartupInfo, &pi)
	if err != nil {
		return fail(err)
	}
	syscall.CloseHandle(pi.Thread)
	proc, err := os.FindProcess(int(pi.ProcessId))
	syscall.CloseHandle(pi.Process)
	if err != nil {
		return fail(err)
	}
	p.proc = proc
	p.wait = func() (int, error) {
		st, err := proc.Wait()
		if err != nil {
			return -1, err
		}
		return st.ExitCode(), nil
	}
	p.r = os.NewFile(uintptr(outR), "conpty-out")
	p.w = os.NewFile(uintptr(inW), "conpty-in")
	p.sys, p.terminal = c, true
	return nil
}

// envBlock is env as CreateProcess takes it: "NAME=value\0...\0\0", sorted
// by name (ignoring case), the last of a name.
func envBlock(env []string) []uint16 {
	byName := map[string]string{}
	for _, kv := range env {
		name := kv
		if i := strings.IndexByte(kv[min(1, len(kv)):], '='); i >= 0 {
			name = kv[:i+min(1, len(kv))]
		}
		byName[strings.ToUpper(name)] = kv
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	var b []uint16
	for _, n := range names {
		b = append(b, utf16.Encode([]rune(byName[n]))...)
		b = append(b, 0)
	}
	if len(b) == 0 {
		b = append(b, 0)
	}
	return append(b, 0)
}

// closeTTY: the command is gone, and so is its pseudo console (whose
// output then ends).
func closeTTY(p *Pty) {
	switch s := p.sys.(type) {
	case *console:
		// It waits for its output to be read: not here.
		go s.close()
	default:
		closePipes(p)
	}
}

func hangup(p *Pty) {
	if c, ok := p.sys.(*console); ok {
		// Its commands are told the console closes.
		go c.close()
	}
}

// Interrupt types CTRL-C in the pseudo console (ends the command without
// one).
func (p *Pty) Interrupt() error {
	if _, ok := p.sys.(*console); ok {
		_, err := p.w.Write([]byte{3})
		return err
	}
	return p.proc.Kill()
}

// Resize sets the size of the pseudo console.
func (p *Pty) Resize(rows, cols int) error {
	c, ok := p.sys.(*console)
	if !ok {
		return nil
	}
	if r, _, _ := procResizePseudoConsole.Call(c.handle, coord(rows, cols)); r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

// isEIO: the end of the output of a pseudo console is a broken pipe.
func isEIO(err error) bool {
	return errors.Is(err, syscall.ERROR_BROKEN_PIPE) || errors.Is(err, syscall.ERROR_HANDLE_EOF)
}
