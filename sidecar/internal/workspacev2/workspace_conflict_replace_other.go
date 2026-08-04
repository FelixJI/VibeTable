//go:build !windows

package workspacev2

import (
	"errors"
	"os"
	"path/filepath"
)

func replaceConflictFile(source string, target string) error {
	if err := os.Rename(source, target); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
