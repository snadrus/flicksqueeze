package paths

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	MinSize       int64 = 10 * 1024 * 1024
	OutputExt           = ".mkv"
	AV1TmpTag           = ".av1tmp"
	DeleteMeTag         = "_deleteMe"
	TmpPrefix           = ".tmp-"
	LockSuffix          = ".flsq-lock"
	MetaComment         = "converted to av1 with flicksqueeze"
	HEVCMetaComment     = "hevc pass by flicksqueeze - av1 pending"
	TallyFile           = ".flicksqueeze.log"
)

func OutputPath(inPath string) string {
	ext := filepath.Ext(inPath)
	stem := inPath[:len(inPath)-len(ext)]
	if strings.EqualFold(ext, OutputExt) {
		return stem + AV1TmpTag + OutputExt
	}
	return stem + OutputExt
}

func IsWorkFile(basename string) bool {
	return strings.Contains(basename, AV1TmpTag) ||
		strings.Contains(basename, TmpPrefix) ||
		strings.Contains(basename, DeleteMeTag)
}

func IsOurComment(comment string) bool {
	return comment == MetaComment || comment == HEVCMetaComment
}

var (
	hostnameOnce sync.Once
	hostname     string
)

func Hostname() string {
	hostnameOnce.Do(func() {
		h, err := os.Hostname()
		if err != nil || h == "" {
			h = "unknown"
		}
		hostname = h
	})
	return hostname
}
