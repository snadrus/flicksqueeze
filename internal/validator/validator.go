package validator

import (
	"context"
	"fmt"
	"math"
	"os"

	"github.com/snadrus/flicksqueeze/internal/ffmpeglib"
	"github.com/snadrus/flicksqueeze/internal/paths"
)

const maxDurationDrift = 5.0 // seconds

// Validate checks that an encoded output file is acceptable relative to its source.
//   - Output must be smaller than the original.
//   - Output must be at least MinSize (guards against corrupt/truncated files).
//   - Durations must match within maxDurationDrift seconds.
func Validate(ctx context.Context, enc *ffmpeglib.Encoder, inputPath, outputPath string, inputSize int64) error {
	outInfo, err := os.Stat(outputPath)
	if err != nil {
		return fmt.Errorf("cannot stat output: %w", err)
	}
	outSize := outInfo.Size()

	if outSize >= inputSize {
		return fmt.Errorf("output (%d bytes) is not smaller than input (%d bytes)", outSize, inputSize)
	}
	if outSize < paths.MinSize {
		return fmt.Errorf("output too small (%d bytes), likely corrupt", outSize)
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
