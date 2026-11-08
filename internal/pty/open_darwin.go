package pty

import (
	"bytes"
	"os"
	"syscall"
	"unsafe"
)

func open() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (*os.File, *os.File, error) {
		master.Close()
		return nil, nil, err
	}
	if err := ioctl(master.Fd(), syscall.TIOCPTYGRANT, 0); err != nil {
		return fail(err)
	}
	if err := ioctl(master.Fd(), syscall.TIOCPTYUNLK, 0); err != nil {
		return fail(err)
	}
	name := make([]byte, 128)
	if err := ioctl(master.Fd(), syscall.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); err != nil {
		return fail(err)
	}
	if i := bytes.IndexByte(name, 0); i >= 0 {
		name = name[:i]
	}
	slave, err = os.OpenFile(string(name), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return fail(err)
	}
	return master, slave, nil
}
