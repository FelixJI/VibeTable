package objectrepo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

func acquireProcessLock(ctx context.Context, path string) (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		acquired, lockErr := tryPlatformFileLock(file)
		if lockErr != nil {
			_ = file.Close()
			return nil, lockErr
		}
		if acquired {
			return func() error {
				return errors.Join(unlockPlatformFile(file), file.Close())
			}, nil
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
