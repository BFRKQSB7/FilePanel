package install

import (
	"os"
	"sync"
)

// Log keeps at most two 1MiB files per instance, independently of the init system.
type Log struct {
	mu   sync.Mutex
	Path string
}

func (l *Log) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(b) > 1<<20 {
		b = b[:1<<20]
	}
	if i, e := os.Lstat(l.Path); e == nil {
		if !i.Mode().IsRegular() {
			return 0, os.ErrPermission
		}
		if i.Size()+int64(len(b)) > 1<<20 {
			if e = os.Rename(l.Path, l.Path+".1"); e != nil {
				return 0, e
			}
		}
	} else if !os.IsNotExist(e) {
		return 0, e
	}
	f, e := os.OpenFile(l.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return 0, e
	}
	n, e := f.Write(b)
	ce := f.Close()
	if e == nil {
		e = ce
	}
	return n, e
}
