//go:build !windows

package workspacev2

import (
	"os"
	"path/filepath"
)

func replaceGrantedFile(source string, destination string) error {
	if err := os.Rename(source, destination); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
