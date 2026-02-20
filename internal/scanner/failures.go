package scanner

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/snadrus/flicksqueeze/internal/vfs"
)

const failuresFile = ".flicksqueeze.failures"

func LoadFailures(fsys vfs.FS, rootPath string) map[string]bool {
	set := make(map[string]bool)
	rc, err := fsys.Open(filepath.Join(rootPath, failuresFile))
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
	return set
}

var failMu sync.Mutex

func MarkFailed(fsys vfs.FS, rootPath, path string) {
	failMu.Lock()
	defer failMu.Unlock()
	f, err := fsys.OpenFile(filepath.Join(rootPath, failuresFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write([]byte(path + "\n"))
}
