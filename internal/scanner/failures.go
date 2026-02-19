package scanner

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const failuresFile = ".flicksqueeze.failures"

// LoadFailures reads the persistent failures list. Files in this set have
// previously failed encoding or validation and should be skipped.
func LoadFailures(rootPath string) map[string]bool {
	set := make(map[string]bool)
	f, err := os.Open(filepath.Join(rootPath, failuresFile))
	if err != nil {
		return set
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			set[line] = true
		}
	}
	return set
}

var failMu sync.Mutex

// MarkFailed appends a path to the persistent failures list. Safe for
// concurrent use (scanner and converter run in separate goroutines).
func MarkFailed(rootPath, path string) {
	failMu.Lock()
	defer failMu.Unlock()
	f, err := os.OpenFile(filepath.Join(rootPath, failuresFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(path + "\n")
}
