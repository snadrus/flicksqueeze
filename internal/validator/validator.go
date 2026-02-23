package validator

import (
	"context"
	"fmt"
	"math"

	"github.com/snadrus/flicksqueeze/internal/ffmpeglib"
	"github.com/snadrus/flicksqueeze/internal/paths"
	"github.com/snadrus/flicksqueeze/internal/vfs"
)

const maxDurationDrift = 5.0

func formatSizeBytes(n int64) string {
	const gb = 1024 * 1024 * 1024
	const mb = 1024 * 1024
	if n >= gb {
		return fmt.Sprintf("%.1f GiB (%d bytes)", float64(n)/gb, n)
	}
	return fmt.Sprintf("%.0f MiB (%d bytes)", float64(n)/mb, n)
}

func Validate(ctx context.Context, fsys vfs.FS, enc *ffmpeglib.Encoder, inputPath, outputPath string, inputSize int64) error {
	outInfo, err := fsys.Stat(outputPath)
	if err != nil {
		return fmt.Errorf("cannot stat output: %w", err)
	}
	outSize := outInfo.Size()

	if outSize >= inputSize {
		return fmt.Errorf("output %s (%s) is not smaller than input %s (%s)",
			outputPath, formatSizeBytes(outSize),
			inputPath, formatSizeBytes(inputSize))
	}
	if outSize < paths.MinSize {
		return fmt.Errorf("output %s too small (%s), likely corrupt", outputPath, formatSizeBytes(outSize))
	}

	inDur, err := enc.DurationSeconds(ctx, inputPath)
	if err != nil {
		return fmt.Errorf("cannot probe input duration: %w", err)
	}
	outDur, err := enc.DurationSeconds(ctx, outputPath)
	if err != nil {
		return fmt.Errorf("cannot probe output duration: %w", err)
	}

	if math.Abs(inDur-outDur) > maxDurationDrift {
		return fmt.Errorf("duration mismatch: input %.1fs vs output %.1fs", inDur, outDur)
	}

	return nil
}
