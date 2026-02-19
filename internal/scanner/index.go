package scanner

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/snadrus/flicksqueeze/internal/paths"
)

const (
	indexVersion = 1
	indexHeader  = "# flicksqueeze codec index – do not edit | version:"
)

func indexFile() string { return ".flicksqueeze-" + paths.Hostname() + ".idx" }
func indexTmp() string  { return ".flicksqueeze-" + paths.Hostname() + ".idx.tmp" }

// pathKey transforms a path so that string comparison matches filepath.WalkDir
// traversal order. WalkDir descends into subdirectories before processing
// later siblings, so `/` must sort before every other byte.
func pathKey(p string) string {
	return strings.ReplaceAll(p, string(filepath.Separator), "\x00")
}

// ---------------- reader (streams old index one entry at a time) ----------------

type idxReader struct {
	f       *os.File
	sc      *bufio.Scanner
	curPath string
	cur     *idxEntry
}

type idxEntry struct {
	codec   string
	modTime time.Time
	size    int64
}

func openReader(path string) *idxReader {
	f, err := os.Open(path)
	if err != nil {
		return &idxReader{}
	}

	sc := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 2*1024*1024)

	if !sc.Scan() {
		f.Close()
		return &idxReader{}
	}
	parts := strings.SplitN(sc.Text(), "version:", 2)
	if len(parts) != 2 {
		f.Close()
		return &idxReader{}
	}
	ver, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || ver != indexVersion {
		f.Close()
		return &idxReader{}
	}

	r := &idxReader{f: f, sc: sc}
	r.next()
	return r
}

func (r *idxReader) next() {
	r.cur = nil
	r.curPath = ""
	if r.sc == nil {
		return
	}
	for r.sc.Scan() {
		line := r.sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.SplitN(line, "\t", 4)
		if len(fields) != 4 {
			continue
		}
		modUnix, err1 := strconv.ParseInt(fields[1], 10, 64)
		size, err2 := strconv.ParseInt(fields[2], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		r.curPath = fields[3]
		r.cur = &idxEntry{
			codec:   fields[0],
			modTime: time.Unix(modUnix, 0),
			size:    size,
		}
		return
	}
}

// advanceTo skips past reader entries whose path sorts before `path` in walk
// order. If the reader has an entry for `path` with matching mtime+size it
// returns the cached codec. The entry is always consumed so the reader stays
// in sync with the walk regardless of hit/miss.
func (r *idxReader) advanceTo(path string, modTime time.Time, size int64) (codec string, hit bool) {
	key := pathKey(path)
	for r.cur != nil && pathKey(r.curPath) < key {
		r.next()
	}
	if r.cur == nil || r.curPath != path {
		return "", false
	}
	e := r.cur
	r.next()
	if e.size == size && e.modTime.Equal(modTime.Truncate(time.Second)) {
		return e.codec, true
	}
	return "", false
}

func (r *idxReader) close() {
	if r.f != nil {
		r.f.Close()
	}
}

// ---------------- writer (appends entries to the new index) ----------------

type idxWriter struct {
	f *os.File
	w *bufio.Writer
	n int
}

func openWriter(path string) (*idxWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := bufio.NewWriter(f)
	fmt.Fprintf(w, "%s %d\n", indexHeader, indexVersion)
	return &idxWriter{f: f, w: w}, nil
}

func (iw *idxWriter) write(path, codec string, modTime time.Time, size int64) {
	fmt.Fprintf(iw.w, "%s\t%d\t%d\t%s\n", codec, modTime.Truncate(time.Second).Unix(), size, path)
	iw.n++
}

func (iw *idxWriter) close() error {
	if err := iw.w.Flush(); err != nil {
		iw.f.Close()
		return err
	}
	return iw.f.Close()
}

// ---------------- lifecycle ----------------

// prepareIndex picks whichever of .idx / .idx.tmp is larger (more complete),
// installs it as .idx.tmp (the read source), and removes the other.
// Returns (tmpPath to read, newPath to write).
func prepareIndex(rootPath string) (tmpPath, newPath string) {
	newPath = filepath.Join(rootPath, indexFile())
	tmpPath = filepath.Join(rootPath, indexTmp())

	baseInfo, baseErr := os.Stat(newPath)
	tmpInfo, tmpErr := os.Stat(tmpPath)

	switch {
	case baseErr != nil && tmpErr != nil:
		// nothing exists
	case baseErr != nil:
		// only tmp exists, keep it
	case tmpErr != nil:
		// only base exists, rotate to tmp
		os.Rename(newPath, tmpPath)
	default:
		if baseInfo.Size() >= tmpInfo.Size() {
			os.Remove(tmpPath)
			os.Rename(newPath, tmpPath)
		} else {
			os.Remove(newPath)
		}
	}

	return tmpPath, newPath
}

// finishIndex removes the tmp backup after a successful write.
func finishIndex(tmpPath string, written int) {
	_ = os.Remove(tmpPath)
	log.Printf("index: saved %d entries", written)
}
