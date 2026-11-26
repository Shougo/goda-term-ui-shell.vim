package shell

import "os"

// openRedirections are the files of the standard input, output and error
// after rs, done left to right as a shell does them (nil: the terminal);
// closeAll closes those opened.
func openRedirections(rs []redirection) (files [3]*os.File, closeAll func(), err error) {
	var opened []*os.File
	closeAll = func() {
		for _, f := range opened {
			f.Close()
		}
	}
	for _, r := range rs {
		if r.dup >= 0 {
			files[r.fd] = files[r.dup]
			continue
		}
		flag := os.O_RDONLY
		switch r.op {
		case ">":
			flag = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
		case ">>":
			flag = os.O_WRONLY | os.O_CREATE | os.O_APPEND
		}
		f, err := os.OpenFile(r.path, flag, 0o666)
		if err != nil {
			closeAll()
			return [3]*os.File{}, func() {}, err
		}
		opened = append(opened, f)
		files[r.fd] = f
	}
	return files, closeAll, nil
}
