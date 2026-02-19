package flsq

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/snadrus/flicksqueeze/internal/paths"
)

// acquireLock atomically creates a lock file for the given input path.
// Returns a release function on success, or an error if the file is already
// locked by another instance (or the lock is stale and was broken).
//
// Lock content is "hostname timestamp" for debugging.
// Stale locks (mtime older than timeout) are broken and retried once.
func acquireLock(inputPath string, timeout time.Duration) (release func(), err error) {
	lockPath := inputPath + paths.LockSuffix

	err = tryCreateLock(lockPath)
	if err == nil {
		return func() { removeLock(lockPath) }, nil
	}

	if !os.IsExist(err) {
		return nil, fmt.Errorf("lock error: %w", err)
	}

	// Lock file exists -- check if stale.
	info, statErr := os.Stat(lockPath)
	if statErr != nil {
		return nil, fmt.Errorf("cannot stat lock %s: %w", lockPath, statErr)
	}
	if time.Since(info.ModTime()) < timeout {
		return nil, fmt.Errorf("locked by another instance (mtime %s)", info.ModTime().Format(time.RFC3339))
	}

	// Stale lock -- break it and retry once.
	log.Printf("breaking stale lock %s (age %v)", lockPath, time.Since(info.ModTime()).Round(time.Minute))
	_ = os.Remove(lockPath)

	err = tryCreateLock(lockPath)
	if err != nil {
		return nil, fmt.Errorf("lock retry failed: %w", err)
	}
	return func() { removeLock(lockPath) }, nil
}

func tryCreateLock(lockPath string) error {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	fmt.Fprintf(f, "%s %s\n", paths.Hostname(), time.Now().Format(time.RFC3339))
	return f.Close()
}

func removeLock(lockPath string) {
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		log.Printf("warning: could not remove lock %s: %v", lockPath, err)
	}
}
