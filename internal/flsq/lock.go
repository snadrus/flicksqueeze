package flsq

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/snadrus/flicksqueeze/internal/paths"
	"github.com/snadrus/flicksqueeze/internal/vfs"
)

func acquireLock(fsys vfs.FS, inputPath string, timeout time.Duration) (release func(), err error) {
	lockPath := inputPath + paths.LockSuffix

	err = tryCreateLock(fsys, lockPath)
	if err == nil {
		return func() { removeLock(fsys, lockPath) }, nil
	}

	if !os.IsExist(err) {
		return nil, fmt.Errorf("lock error: %w", err)
	}

	info, statErr := fsys.Stat(lockPath)
	if statErr != nil {
		return nil, fmt.Errorf("cannot stat lock %s: %w", lockPath, statErr)
	}
	if time.Since(info.ModTime()) < timeout {
		return nil, fmt.Errorf("locked by another instance (mtime %s)", info.ModTime().Format(time.RFC3339))
	}

	log.Printf("breaking stale lock %s (age %v)", lockPath, time.Since(info.ModTime()).Round(time.Minute))
	_ = fsys.Remove(lockPath)

	err = tryCreateLock(fsys, lockPath)
	if err != nil {
		return nil, fmt.Errorf("lock retry failed: %w", err)
	}
	return func() { removeLock(fsys, lockPath) }, nil
}

func tryCreateLock(fsys vfs.FS, lockPath string) error {
	f, err := fsys.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	fmt.Fprintf(f, "%s %s\n", paths.Hostname(), time.Now().Format(time.RFC3339))
	return f.Close()
}

func removeLock(fsys vfs.FS, lockPath string) {
	if err := fsys.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		log.Printf("warning: could not remove lock %s: %v", lockPath, err)
	}
}
