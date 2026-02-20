package scanner

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/snadrus/flicksqueeze/internal/paths"
	"github.com/snadrus/flicksqueeze/internal/vfs"
)

const (
	indexVersion = 1
	indexHeader  = "# flicksqueeze codec index – do not edit | version:"
)

func indexFile() string { return ".flicksqueeze-" + paths.Hostname() + ".idx" }
func indexTmp() string  { return ".flicksqueeze-" + paths.Hostname() + ".idx.tmp" }

func pathKey(p string) string {
	return strings.ReplaceAll(p, string(filepath.Separator), "\x00")
}

// ---------------- reader ----------------

type idxReader struct {
	rc      io.Closer
	sc      *bufio.Scanner
	curPath string
	cur     *idxEntry
}

type idxEntry struct {
	codec   string
	modTime time.Time
	size    int64
}

func openReader(fsys vfs.FS, path string) *idxReader {
	rc, err := fsys.Open(path)
	if err != nil {
		return &idxReader{}
	}

	sc := bufio.NewScanner(rc)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 2*1024*1024)

	if !sc.Scan() {
		rc.Close()
		return &idxReader{}
	}
	parts := strings.SplitN(sc.Text(), "version:", 2)
	if len(parts) != 2 {
		rc.Close()
		return &idxReader{}
	}
	ver, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || ver != indexVersion {
		rc.Close()
		return &idxReader{}
	}

	r := &idxReader{rc: rc, sc: sc}
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
	if r.rc != nil {
		r.rc.Close()
	}
}

// ---------------- writer ----------------

type idxWriter struct {
	wc io.WriteCloser
	w  *bufio.Writer
	n  int
}

func openWriter(fsys vfs.FS, path string) (*idxWriter, error) {
	wc, err := fsys.Create(path)
	if err != nil {
		return nil, err
	}
	w := bufio.NewWriter(wc)
	fmt.Fprintf(w, "%s %d\n", indexHeader, indexVersion)
	return &idxWriter{wc: wc, w: w}, nil
}

func (iw *idxWriter) write(path, codec string, modTime time.Time, size int64) {
	fmt.Fprintf(iw.w, "%s\t%d\t%d\t%s\n", codec, modTime.Truncate(time.Second).Unix(), size, path)
	iw.n++
}

func (iw *idxWriter) close() error {
	if err := iw.w.Flush(); err != nil {
		iw.wc.Close()
		return err
	}
	return iw.wc.Close()
}

// ---------------- lifecycle ----------------

func prepareIndex(fsys vfs.FS, rootPath string) (tmpPath, newPath string) {
	newPath = filepath.Join(rootPath, indexFile())
	tmpPath = filepath.Join(rootPath, indexTmp())

	baseInfo, baseErr := fsys.Stat(newPath)
	tmpInfo, tmpErr := fsys.Stat(tmpPath)

	switch {
	case baseErr != nil && tmpErr != nil:
	case baseErr != nil:
	case tmpErr != nil:
		fsys.Rename(newPath, tmpPath)
	default:
		if baseInfo.Size() >= tmpInfo.Size() {
			fsys.Remove(tmpPath)
			fsys.Rename(newPath, tmpPath)
		} else {
			fsys.Remove(newPath)
		}
	}

	return tmpPath, newPath
}

func finishIndex(fsys vfs.FS, tmpPath string, written int) {
	_ = fsys.Remove(tmpPath)
	log.Printf("index: saved %d entries", written)
}
