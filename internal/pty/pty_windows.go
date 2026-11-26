package pty

import (
	"errors"
	"os"
	"runtime"
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
	procSetConsoleCtrlHandler       = kernel32.NewProc("SetConsoleCtrlHandler")
)

// ctrlC serializes the processing of CTRL-C of this process around the
// creation of a command.
var ctrlC sync.Mutex

const (
	procThreadAttributePseudoConsole = 0x00020016
	procThreadAttributeHandleList    = 0x00020002
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
		if o.redirected() {
			return ErrRedirect
		}
		return startPipes(p, path, o)
	}
	return startConsole(p, []Stage{{path, o.Args, o.Env, o.Stdin, o.Stdout, o.Stderr}}, o)
}

// CanPipe: StartPipeline may have several commands (in a pseudo console).
func CanPipe() bool { return hasConPTY() }

// CanRedirect: Options may have files (in a pseudo console).
func CanRedirect() bool { return hasConPTY() }

func startPipeline(p *Pty, stages []Stage, o Options) error {
	if !hasConPTY() {
		return ErrPipe
	}
	return startConsole(p, stages, o)
}

// startConsole starts the stages in one pseudo console, all attached to it:
// a CTRL-C there reaches each, and each can read a password from it.  The
// standard handles of each are those of the console, but the pipes between
// them and the files of their redirections.
func startConsole(p *Pty, stages []Stage, o Options) error {
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

	// The pipes between the stages: the commands have them once started,
	// those of this process are closed (the end of a pipe is seen once its
	// command is gone).
	n := len(stages)
	std := make([][3]syscall.Handle, n)
	var ends []syscall.Handle
	defer func() { closeAll(ends...) }()
	fail := func(err error) error {
		c.close()
		closeAll(inW, outR)
		return err
	}
	for i := range n - 1 {
		var r, w syscall.Handle
		if err := syscall.CreatePipe(&r, &w, nil, 0); err != nil {
			return fail(err)
		}
		ends = append(ends, r, w)
		std[i][1], std[i+1][0] = w, r
	}
	for i, s := range stages {
		for k, f := range [3]*os.File{s.Stdin, s.Stdout, s.Stderr} {
			if f != nil {
				std[i][k] = syscall.Handle(f.Fd())
			}
		}
	}
	procs := make([]*os.Process, 0, n)
	for i, s := range stages {
		proc, err := createInConsole(c.handle, s, o.Dir, std[i])
		if err != nil {
			for _, proc := range procs {
				_ = proc.Kill()
			}
			return fail(err)
		}
		procs = append(procs, proc)
	}
	p.procs, p.proc = procs, procs[n-1]
	p.wait = func() (int, error) {
		code := -1
		var err error
		for _, proc := range procs {
			st, e := proc.Wait()
			if e != nil {
				code, err = -1, e
				continue
			}
			code, err = st.ExitCode(), nil
		}
		return code, err
	}
	p.r = os.NewFile(uintptr(outR), "conpty-out")
	p.w = os.NewFile(uintptr(inW), "conpty-in")
	p.sys, p.terminal = c, true
	return nil
}

// createInConsole runs s attached to the pseudo console hpc, with the
// standard handles std (0: that of the console).  Those given are passed
// as inheritable copies, and no other handle is inherited (a handle list).
func createInConsole(hpc uintptr, s Stage, dir string, std [3]syscall.Handle) (*os.Process, error) {
	self, err := syscall.GetCurrentProcess()
	if err != nil {
		return nil, err
	}
	var inherit []syscall.Handle
	defer func() {
		for _, h := range inherit {
			syscall.CloseHandle(h)
		}
	}()
	var handles [3]syscall.Handle
	for k, h := range std {
		if h == 0 {
			continue
		}
		var d syscall.Handle
		if err := syscall.DuplicateHandle(self, h, self, &d, 0, true, syscall.DUPLICATE_SAME_ACCESS); err != nil {
			return nil, err
		}
		inherit = append(inherit, d)
		handles[k] = d
	}

	count := uintptr(1)
	if len(inherit) > 0 {
		count = 2
	}
	var size uintptr
	procInitializeProcThreadAttList.Call(0, count, 0, uintptr(unsafe.Pointer(&size)))
	if size == 0 {
		return nil, errors.New("pty: InitializeProcThreadAttributeList")
	}
	list := make([]byte, size)
	if r, _, err := procInitializeProcThreadAttList.Call(uintptr(unsafe.Pointer(&list[0])), count, 0,
		uintptr(unsafe.Pointer(&size))); r == 0 {
		return nil, err
	}
	defer procDeleteProcThreadAttList.Call(uintptr(unsafe.Pointer(&list[0])))
	if r, _, err := procUpdateProcThreadAttribute.Call(uintptr(unsafe.Pointer(&list[0])), 0,
		procThreadAttributePseudoConsole, hpc, unsafe.Sizeof(hpc), 0, 0); r == 0 {
		return nil, err
	}
	if len(inherit) > 0 {
		if r, _, err := procUpdateProcThreadAttribute.Call(uintptr(unsafe.Pointer(&list[0])), 0,
			procThreadAttributeHandleList, uintptr(unsafe.Pointer(&inherit[0])),
			uintptr(len(inherit))*unsafe.Sizeof(inherit[0]), 0, 0); r == 0 {
			return nil, err
		}
	}

	si := startupInfoEx{attributeList: &list[0]}
	si.Cb = uint32(unsafe.Sizeof(si))
	// Not the standard handles of this process (the pipes of Vim): those
	// given, or (0) those of the pseudo console.
	si.Flags = startfUseStdHandles
	si.StdInput, si.StdOutput, si.StdErr = handles[0], handles[1], handles[2]
	cmdline := syscall.EscapeArg(s.Path)
	for _, a := range s.Args {
		cmdline += " " + syscall.EscapeArg(a)
	}
	cl, err := syscall.UTF16PtrFromString(cmdline)
	if err != nil {
		return nil, err
	}
	var wdir *uint16
	if dir != "" {
		if wdir, err = syscall.UTF16PtrFromString(dir); err != nil {
			return nil, err
		}
	}
	env := envBlock(s.Env)
	var pi syscall.ProcessInformation
	// CTRL-C processed by the command: this process may ignore it (as one
	// started with CREATE_NEW_PROCESS_GROUP, as a job of Vim), and a command
	// started then ignores it too (CTRL-C in the pseudo console ended
	// nothing).  This process ignores it again afterwards: CTRL-C in a
	// console it shares (Vim in a terminal) does not end it.
	ctrlC.Lock()
	procSetConsoleCtrlHandler.Call(0, 0)
	err = syscall.CreateProcess(nil, cl, nil, nil, len(inherit) > 0,
		extendedStartupInfoPresent|createUnicodeEnvironment, &env[0], wdir, &si.StartupInfo, &pi)
	procSetConsoleCtrlHandler.Call(0, 1)
	ctrlC.Unlock()
	runtime.KeepAlive(inherit)
	runtime.KeepAlive(list)
	if err != nil {
		return nil, err
	}
	syscall.CloseHandle(pi.Thread)
	proc, err := os.FindProcess(int(pi.ProcessId))
	syscall.CloseHandle(pi.Process)
	return proc, err
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
