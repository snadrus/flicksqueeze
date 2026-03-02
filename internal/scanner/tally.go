package scanner

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/snadrus/flicksqueeze/internal/paths"
	"github.com/snadrus/flicksqueeze/internal/vfs"
)

// LoadTally reads .flicksqueeze.log from the given paths and returns empirical savings
// ratios by codec. Key is lowercase codec (e.g. "h264"). Value is mean savings ratio
// in [0,1], i.e. (origSize - outSize) / origSize. Merges data from all readable paths.
// Returns nil if no file could be read or all were empty.
func LoadTally(fsys vfs.FS, tallyPaths ...string) map[string]float64 {
	type sum struct {
		totalRatio float64
		n          int
	}
	byCodec := make(map[string]*sum)

	parseFile := func(rc *bufio.Scanner) {
		for rc.Scan() {
			parts := strings.Split(rc.Text(), "\t")
			if len(parts) < 5 {
				continue
			}
			codec := strings.ToLower(strings.TrimSpace(parts[2]))
			origSize, err1 := strconv.ParseInt(parts[3], 10, 64)
			outSize, err2 := strconv.ParseInt(parts[4], 10, 64)
			if err1 != nil || err2 != nil || origSize <= 0 || outSize < 0 || outSize >= origSize {
				continue
			}
			ratio := float64(origSize-outSize) / float64(origSize)
			if byCodec[codec] == nil {
				byCodec[codec] = &sum{}
			}
			byCodec[codec].totalRatio += ratio
			byCodec[codec].n++
		}
	}

	for _, p := range tallyPaths {
		rc, err := fsys.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(rc)
		parseFile(sc)
		rc.Close()
	}

	// When local, also try home dir tally (vfs.Local can open any path)
	if !fsys.IsRemote() {
		if home, err := os.UserHomeDir(); err == nil {
			homeTally := filepath.Join(home, paths.TallyFile)
			rc, err := os.Open(homeTally)
			if err == nil {
				sc := bufio.NewScanner(rc)
				parseFile(sc)
				rc.Close()
			}
		}
	}

	if len(byCodec) == 0 {
		return nil
	}
	out := make(map[string]float64, len(byCodec))
	for codec, s := range byCodec {
		out[codec] = s.totalRatio / float64(s.n)
	}
	return out
}
