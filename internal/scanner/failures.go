package scanner

import (
	"bufio"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/snadrus/flicksqueeze/internal/vfs"
)

const failuresFile = ".flicksqueeze.failures"

func failuresPath(rootPath string) string {
	// SSH/remote paths are Unix-style; filepath.Join on Windows produces
	// backslashes which break SFTP. Use path.Join for Unix-style paths.
	if strings.HasPrefix(rootPath, "/") {
		return path.Join(path.Clean(rootPath), failuresFile)
	}
	return filepath.Join(rootPath, failuresFile)
}

func LoadFailures(fsys vfs.FS, rootPath string) map[string]bool {
	set := make(map[string]bool)
	failPath := failuresPath(rootPath)
	rc, err := fsys.Open(failPath)
	if err != nil {
		return set
	}
	defer rc.Close()
	sc := bufio.NewScanner(rc)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			set[line] = true
		}
	}
	if len(set) > 0 {
		log.Printf("scan: loaded %d paths from %s", len(set), failPath)
	}
	return set
}

var failMu sync.Mutex

func MarkFailed(fsys vfs.FS, rootPath, moviePath string) {
	failMu.Lock()
	defer failMu.Unlock()
	fp := failuresPath(rootPath)
	f, err := fsys.OpenFile(fp, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write([]byte(moviePath + "\n"))
}
