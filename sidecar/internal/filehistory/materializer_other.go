//go:build !windows

package filehistory

import (
	"os"
	"path/filepath"
)

func replaceMaterializedFile(source string, destination string) error {
	if err := os.Rename(source, destination); err != nil {
		return err
	}
	return syncDirectoryDurable(filepath.Dir(destination))
}

func syncDirectoryDurable(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

func pathHasReparsePoint(path string) bool {
	info, err := os.Lstat(path)
	return err != nil || info.Mode()&os.ModeSymlink != 0
}
