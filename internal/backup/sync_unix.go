//go:build !windows

package backup

import (
	"io/fs"
	"os"
	"path/filepath"
)

func syncTree(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() {
			return nil
		}
		f, e := os.Open(p)
		if e != nil {
			return e
		}
		defer f.Close()
		return f.Sync()
	})
}
