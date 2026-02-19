package paths

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	MinSize     int64 = 10 * 1024 * 1024 // 10 MB — scanner filter + validator floor
	OutputExt         = ".mkv"
	AV1TmpTag         = ".av1tmp"
	DeleteMeTag       = "_deleteMe"
	TmpPrefix         = ".tmp-"
	MetaComment     = "converted to av1 with flicksqueeze"
	HEVCMetaComment = "hevc pass by flicksqueeze - av1 pending"
	TallyFile       = ".flicksqueeze.log"
)

// OutputPath computes the conversion output path for an input file.
// Non-.mkv inputs get a .mkv extension. .mkv inputs get .av1tmp.mkv to
// avoid clobbering the source during encode. Every result ends with a
// real container extension.
func OutputPath(inPath string) string {
	ext := filepath.Ext(inPath)
	stem := inPath[:len(inPath)-len(ext)]
	if strings.EqualFold(ext, OutputExt) {
		return stem + AV1TmpTag + OutputExt
	}
	return stem + OutputExt
}

// IsWorkFile returns true for filenames that are intermediate work
// products and should be ignored by the scanner.
func IsWorkFile(basename string) bool {
	return strings.Contains(basename, AV1TmpTag) ||
		strings.Contains(basename, TmpPrefix) ||
		strings.Contains(basename, DeleteMeTag)
}

// IsOurComment returns true if the comment was written by flicksqueeze
// (either AV1 final or HEVC intermediate).
func IsOurComment(comment string) bool {
	return comment == MetaComment || comment == HEVCMetaComment
}

// OutputExists checks whether the expected output for a conversion
// already exists on disk.
func OutputExists(path string) bool {
	_, err := os.Stat(OutputPath(path))
	return err == nil
}
