package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	e2eMutationBarrierDirectoryEnvironment = "VIBETABLE_E2E_MUTATION_BARRIER_DIR"
	e2eMutationBarrierArmFile              = "mutation-barrier.arm"
	e2eMutationBarrierClaimedFile          = "mutation-barrier.claimed"
	e2eMutationBarrierReadyFile            = "mutation-barrier.ready.json"
	e2eMutationBarrierReleaseFile          = "mutation-barrier.release"
	e2eMutationBarrierPoint                = "after_record"
	e2eMutationBarrierTimeout              = 2 * time.Minute
)

type e2eMutationBarrierReady struct {
	ProcessID int    `json:"pid"`
	Point     string `json:"point"`
}

// newE2EMutationBarrierFromEnvironment returns a dormant-by-default fault
// injector used only by the packaged product E2E runner. Activation requires
// both an explicit absolute directory in the process environment and a
// one-time arm file created before the sidecar starts. Nothing on the Web/RPC
// surface can arm or release this barrier.
func newE2EMutationBarrierFromEnvironment() func(string) error {
	directory := os.Getenv(e2eMutationBarrierDirectoryEnvironment)
	if directory == "" || !filepath.IsAbs(directory) {
		return nil
	}
	directory = filepath.Clean(directory)
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return nil
	}
	armPath := filepath.Join(directory, e2eMutationBarrierArmFile)
	if _, err := os.Stat(armPath); err != nil {
		return nil
	}

	var once sync.Once
	var barrierErr error
	return func(point string) error {
		if point != e2eMutationBarrierPoint {
			return nil
		}
		once.Do(func() {
			barrierErr = runE2EMutationBarrier(directory, armPath)
		})
		return barrierErr
	}
}

func runE2EMutationBarrier(directory string, armPath string) error {
	claimedPath := filepath.Join(directory, e2eMutationBarrierClaimedFile)
	if err := os.Rename(armPath, claimedPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("claim E2E mutation barrier: %w", err)
	}

	ready, err := json.Marshal(e2eMutationBarrierReady{
		ProcessID: os.Getpid(),
		Point:     e2eMutationBarrierPoint,
	})
	if err != nil {
		return fmt.Errorf("encode E2E mutation barrier: %w", err)
	}
	readyPath := filepath.Join(directory, e2eMutationBarrierReadyFile)
	temporaryPath := readyPath + ".tmp"
	if err := os.WriteFile(temporaryPath, append(ready, '\n'), 0o600); err != nil {
		return fmt.Errorf("write E2E mutation barrier: %w", err)
	}
	if err := os.Rename(temporaryPath, readyPath); err != nil {
		return fmt.Errorf("publish E2E mutation barrier: %w", err)
	}

	releasePath := filepath.Join(directory, e2eMutationBarrierReleaseFile)
	deadline := time.Now().Add(e2eMutationBarrierTimeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(releasePath); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read E2E mutation barrier release: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("E2E mutation barrier timed out")
}
