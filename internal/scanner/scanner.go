package scanner

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/snadrus/flicksqueeze/internal/ffmpeglib"
	"github.com/snadrus/flicksqueeze/internal/paths"
)

const (
	MaxCandidates = 10
	flushEvery    = 1000 // flush the worst candidate after this many videos evaluated
	staleAge      = 3 * 24 * time.Hour
)

var movieExtensions = map[string]bool{
	".mp4": true, ".mkv": true, ".avi": true, ".mov": true,
	".wmv": true, ".flv": true, ".m4v": true, ".mpg": true,
	".mpeg": true, ".ts": true, ".webm": true, ".vob": true,
}

// Directories that likely belong to other software and should not be touched.
var skipDirs = map[string]bool{
	".cache": true, ".config": true, ".local": true, ".steam": true,
	"steam": true, "Steam": true, "SteamLibrary": true,
	"lib": true, "lib64": true, "lib32": true,
	"node_modules": true, ".git": true, ".svn": true,
	".thumbnails": true, ".Trash": true, ".Trash-1000": true,
	"lost+found": true, "snap": true, "flatpak": true,
	"__pycache__": true, ".venv": true, "venv": true,
	"AppData": true, "Application Support": true,
	"Caches": true, "Library": true,
}

var codecWaste = map[string]float64{
	"mpeg1video": 4.0,
	"mpeg2video": 4.0,
	"msmpeg4v1":  3.5,
	"msmpeg4v2":  3.5,
	"msmpeg4v3":  3.5,
	"wmv1":       3.5,
	"wmv2":       3.5,
	"wmv3":       3.5,
	"mpeg4":      3.0,
	"vp8":        2.5,
	"h264":       2.0,
	"hevc":       1.4,
	"vp9":        1.3,
}

type Candidate struct {
	Path       string
	Size       int64
	Codec      string
	WasteScore float64
}

func codecWasteMultiplier(codec string) float64 {
	c := strings.ToLower(codec)
	if m, ok := codecWaste[c]; ok {
		return m
	}
	return 2.0
}

// Scan walks rootPath in its own goroutine, streaming up to MaxCandidates
// candidates on out. The old index is read one entry at a time alongside the
// walk (never fully loaded into memory) and a new index is written as we go.
// After every flushEvery conversion candidates it sends the worst one found
// so far, so encoding can begin during the scan.
// The channel is closed when the scan is complete.
func Scan(ctx context.Context, enc *ffmpeglib.Encoder, rootPath string, out chan<- Candidate) {
	defer close(out)

	cutoff := time.Now().Add(-staleAge)
	failures := LoadFailures(rootPath)

	tmpPath, newPath := prepareIndex(rootPath)
	reader := openReader(tmpPath)
	defer reader.close()

	writer, err := openWriter(newPath)
	if err != nil {
		log.Printf("scan: cannot create index %s: %v", newPath, err)
		return
	}

	var buf []Candidate
	sent := 0
	scanned := 0
	writerOK := true

	enqueue := func(path, codec string, sz int64) {
		mult := codecWasteMultiplier(codec)
		buf = append(buf, Candidate{
			Path:       path,
			Size:       sz,
			Codec:      codec,
			WasteScore: float64(sz) * mult,
		})
		scanned++
		if scanned%flushEvery == 0 && sent < MaxCandidates {
			sent += flushBest(ctx, &buf, out, 1)
		}
	}

	_ = filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if !movieExtensions[ext] {
			return nil
		}
		if paths.IsWorkFile(filepath.Base(path)) {
			return nil
		}
		if failures[path] {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}
		mod := info.ModTime()
		sz := info.Size()

		cachedCodec, hit := reader.advanceTo(path, mod, sz)

		if hit {
			writer.write(path, cachedCodec, mod, sz)
			if sz < paths.MinSize || mod.After(cutoff) || cachedCodec == "X" || cachedCodec == "av1" || cachedCodec == "flicksqueeze" {
				return nil
			}
			if paths.OutputExists(path) {
				return nil
			}
			enqueue(path, cachedCodec, sz)
			return nil
		}

		if sz < paths.MinSize || mod.After(cutoff) {
			return nil
		}

		probed, err := enc.VideoCodec(ctx, path)
		if err != nil {
			log.Printf("scan: skipping %s (probe failed: %v)", path, err)
			writer.write(path, "X", mod, sz)
			return nil
		}
		codec := strings.ToLower(probed)

		if codec == "av1" {
			comment, _ := enc.Comment(ctx, path)
			if comment == paths.MetaComment {
				// Final AV1 output by flicksqueeze — permanently skip.
				codec = "flicksqueeze"
			}
			// AV1 files with HEVCMetaComment or no tag stay as "av1" —
			// already optimal, nothing to re-encode.
			writer.write(path, codec, mod, sz)
			return nil
		}
		writer.write(path, codec, mod, sz)
		if paths.OutputExists(path) {
			return nil
		}
		enqueue(path, codec, sz)
		return nil
	})

	if remaining := MaxCandidates - sent; remaining > 0 {
		flushBest(ctx, &buf, out, remaining)
	}

	if err := writer.close(); err != nil {
		log.Printf("scan: index write error: %v", err)
		writerOK = false
	}
	if writerOK {
		finishIndex(tmpPath, writer.n)
	}

	log.Printf("scan complete: %d conversion candidates evaluated", scanned)
}

// flushBest sorts buf descending by waste, sends up to n from the top, and
// removes them from buf. Returns the number actually sent.
func flushBest(ctx context.Context, buf *[]Candidate, out chan<- Candidate, n int) int {
	sort.Slice(*buf, func(i, j int) bool {
		return (*buf)[i].WasteScore > (*buf)[j].WasteScore
	})
	sent := 0
	for sent < n && len(*buf) > 0 {
		select {
		case out <- (*buf)[0]:
			*buf = (*buf)[1:]
			sent++
		case <-ctx.Done():
			return sent
		}
	}
	return sent
}

// HumanSize returns a human-readable byte size.
func HumanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
