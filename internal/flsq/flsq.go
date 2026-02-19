package flsq

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/snadrus/flicksqueeze/internal/ffmpeglib"
	"github.com/snadrus/flicksqueeze/internal/paths"
	"github.com/snadrus/flicksqueeze/internal/scanner"
	"github.com/snadrus/flicksqueeze/internal/validator"
)

const (
	idleSleep   = 24 * time.Hour
	baselineGHz = 2.5  // reference CPU speed for timeout scaling
	baseRateH   = 3.0  // wall-clock hours per GB of input at score=1 (1 baseline thread)
	safetyMult  = 3.0  // safety margin on top of expected time
	minTimeoutH = 4.0
	maxTimeoutH = 96.0
)

type Config struct {
	RootPath string
	NoDelete bool
	QuitCh   <-chan struct{} // closed or sent to when user requests graceful stop
}

// ListenForQuit starts a goroutine that reads lines from stdin. Any input
// (e.g. pressing Enter) signals a graceful stop. Returns a channel that
// receives when the user requests quit.
func ListenForQuit() <-chan struct{} {
	ch := make(chan struct{}, 1)
	go func() {
		r := bufio.NewReader(os.Stdin)
		for {
			_, err := r.ReadString('\n')
			if err != nil {
				return
			}
			select {
			case ch <- struct{}{}:
				log.Println(">>> graceful stop requested — will finish current encode then exit")
			default:
			}
		}
	}()
	return ch
}

func quitRequested(ch <-chan struct{}) bool {
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// Run is the main scan-convert-validate loop. It blocks until the context is
// cancelled or an unrecoverable error occurs. The scanner runs in its own
// goroutine and streams candidates to the converter via a channel, so
// conversion can begin while the scan is still in progress.
func Run(ctx context.Context, cfg Config) error {
	enc := ffmpeglib.New()
	if err := enc.EnsureAvailable(ctx); err != nil {
		return err
	}

	hw := enc.DetectHW(ctx)
	threads := encodeThreads()
	ghz := cpuGHz()
	score := float64(threads) * (ghz / baselineGHz)
	ratePerGB := (baseRateH / score) * safetyMult
	log.Printf("flicksqueeze watching %s (threads=%d, cpu=%.1f GHz, ~%.1fh timeout per GB)",
		cfg.RootPath, threads, ghz, ratePerGB)
	if hw.UseHEVCFirst() {
		log.Printf("HEVC hw available (%s): will convert worst codecs to HEVC first, AV1 after", hw.HEVCProfile.Name)
	}
	log.Println("press Enter to stop after the current encode finishes")

	for {
		ch := make(chan scanner.Candidate, scanner.MaxCandidates)
		go scanner.Scan(ctx, enc, cfg.RootPath, ch)

		processed := 0
		for c := range ch {
			if ctx.Err() != nil || quitRequested(cfg.QuitCh) {
				for range ch {
				}
				return nil
			}
			processed++
			log.Printf("candidate %d: [%s] %s (%s, codec=%s)",
				processed, scanner.HumanSize(c.Size), c.Path, fmtWaste(c.WasteScore), c.Codec)
			processCandidate(ctx, enc, c, cfg.RootPath, cfg.NoDelete, hw)
		}

		if ctx.Err() != nil || quitRequested(cfg.QuitCh) {
			return nil
		}

		if processed == 0 {
			log.Println("no conversion candidates found, sleeping 24 hours")
			if !sleepCtx(ctx, idleSleep) {
				return nil
			}
		}
	}
}

// Codecs less efficient than HEVC where a fast hw HEVC encode saves space
// immediately. Once nothing worse than HEVC remains, the scanner naturally
// feeds HEVC files for the slower AV1 sw encode.
var hevcFirstCodecs = map[string]bool{
	"h264": true, "mpeg4": true, "mpeg2video": true, "mpeg1video": true,
	"msmpeg4v1": true, "msmpeg4v2": true, "msmpeg4v3": true,
	"wmv1": true, "wmv2": true, "wmv3": true, "vp8": true,
}

func processCandidate(ctx context.Context, enc *ffmpeglib.Encoder, c scanner.Candidate, rootPath string, noDelete bool, hw ffmpeglib.HWCaps) {
	outPath := paths.OutputPath(c.Path)

	// --- collision / restart detection ---
	if _, err := os.Stat(outPath); err == nil {
		comment, _ := enc.Comment(ctx, outPath)
		if !paths.IsOurComment(comment) {
			log.Printf("skipping %s: output %s already exists (not ours)", c.Path, outPath)
			return
		}
		if err := validator.Validate(ctx, enc, c.Path, outPath, c.Size); err == nil {
			log.Printf("restart recovery: %s already converted, finishing up", c.Path)
			comment, _ := enc.Comment(ctx, outPath)
			encType := "av1"
			if comment == paths.HEVCMetaComment {
				encType = "hevc"
			}
			finishConversion(c, outPath, rootPath, noDelete, encType)
			return
		}
		log.Printf("stale output %s from previous failed run, removing", outPath)
		_ = os.Remove(outPath)
	}

	// --- choose encoder: HEVC hw (fast) or AV1 sw (small) ---
	useHEVC := hw.UseHEVCFirst() && hevcFirstCodecs[strings.ToLower(c.Codec)]
	progress := func(p ffmpeglib.ProgressLine) { fmt.Println(p.Raw) }
	timeout := encodeTimeoutForSize(c.Size)

	var err error
	if useHEVC {
		err = encodeHEVC(ctx, enc, c.Path, outPath, hw, timeout, progress)
	} else {
		err = encodeAV1(ctx, enc, c.Path, outPath, timeout, progress)
	}

	if err != nil {
		log.Printf("encode failed for %s: %v", c.Path, err)
		_ = os.Remove(outPath)
		if ctx.Err() == nil {
			scanner.MarkFailed(rootPath, c.Path)
		}
		return
	}

	// --- validate ---
	if err := validator.Validate(ctx, enc, c.Path, outPath, c.Size); err != nil {
		log.Printf("validation failed for %s: %v", c.Path, err)
		_ = os.Remove(outPath)
		if ctx.Err() == nil {
			scanner.MarkFailed(rootPath, c.Path)
		}
		return
	}

	encType := "av1"
	if useHEVC {
		encType = "hevc"
	}
	finishConversion(c, outPath, rootPath, noDelete, encType)
}

func encodeHEVC(ctx context.Context, enc *ffmpeglib.Encoder, inPath, outPath string, hw ffmpeglib.HWCaps, timeout time.Duration, progress func(ffmpeglib.ProgressLine)) error {
	log.Printf("HEVC hw encode %s -> %s", inPath, outPath)

	hwCtx, hwCancel := context.WithTimeout(ctx, timeout)
	err := enc.EncodeToHEVCHW(hwCtx, inPath, outPath, *hw.HEVCProfile, paths.HEVCMetaComment, false, progress)
	hwCancel()

	if err != nil && ctx.Err() == nil {
		log.Printf("HEVC encode failed (retrying without subtitles): %v", err)
		_ = os.Remove(outPath)
		hwCtx2, hwCancel2 := context.WithTimeout(ctx, timeout)
		err = enc.EncodeToHEVCHW(hwCtx2, inPath, outPath, *hw.HEVCProfile, paths.HEVCMetaComment, true, progress)
		hwCancel2()
	}
	return err
}

func encodeAV1(ctx context.Context, enc *ffmpeglib.Encoder, inPath, outPath string, timeout time.Duration, progress func(ffmpeglib.ProgressLine)) error {
	log.Printf("AV1 sw encode %s -> %s", inPath, outPath)

	opts := ffmpeglib.AV1Options{
		CRF:              28,
		Preset:           5,
		Threads:          encodeThreads(),
		SkipIfAlreadyAV1: true,
		Container:        "mkv",
		PixFmt:           "yuv420p10le",
		MetaComment:      paths.MetaComment,
	}

	encCtx, encCancel := context.WithTimeout(ctx, timeout)
	_, err := enc.EncodeToAV1SVT(encCtx, inPath, outPath, opts, progress)
	encCancel()

	if err != nil && ctx.Err() == nil {
		log.Printf("AV1 encode failed (retrying without subtitles): %v", err)
		_ = os.Remove(outPath)
		opts.DropSubtitles = true
		encCtx2, encCancel2 := context.WithTimeout(ctx, timeout)
		_, err = enc.EncodeToAV1SVT(encCtx2, inPath, outPath, opts, progress)
		encCancel2()
	}
	return err
}

func finishConversion(c scanner.Candidate, outPath, rootPath string, noDelete bool, encType string) {
	outInfo, _ := os.Stat(outPath)
	outSize := outInfo.Size()
	saved := c.Size - outSize
	log.Printf("validated OK [%s]: %s saved (%s -> %s)",
		encType, scanner.HumanSize(saved), scanner.HumanSize(c.Size), scanner.HumanSize(outSize))

	retireOriginal(c.Path, noDelete)

	finalPath := outPath
	if strings.Contains(filepath.Base(outPath), paths.AV1TmpTag) {
		finalPath = strings.Replace(outPath, paths.AV1TmpTag, "", 1)
		if err := os.Rename(outPath, finalPath); err != nil {
			log.Printf("error: rename %s -> %s failed: %v", outPath, finalPath, err)
			return
		}
	}

	appendTally(rootPath, encType, c.Codec, c.Path, c.Size, finalPath, outSize)
	log.Printf("done: %s", finalPath)
}

// appendTally records a completed conversion to the tally log.
// Format: timestamp \t type \t fromCodec \t beforeSize \t afterSize \t inputPath \t outputPath
func appendTally(rootPath, encType, fromCodec, origPath string, origSize int64, outPath string, outSize int64) {
	f, err := os.OpenFile(filepath.Join(rootPath, paths.TallyFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\t%s\t%s\t%d\t%d\t%s\t%s\n",
		time.Now().Format(time.RFC3339), encType, fromCodec, origSize, outSize, origPath, outPath)
}

func retireOriginal(path string, noDelete bool) {
	if noDelete {
		ext := filepath.Ext(path)
		tagged := path[:len(path)-len(ext)] + paths.DeleteMeTag + ext
		if err := os.Rename(path, tagged); err != nil {
			log.Printf("warning: could not rename original %s -> %s: %v", path, tagged, err)
		}
		return
	}
	if err := os.Remove(path); err != nil {
		log.Printf("warning: could not remove original %s: %v", path, err)
	}
}

func encodeThreads() int {
	return runtime.NumCPU()
}

// encodeTimeoutForSize computes a per-file deadline scaled by input file size
// and the machine's throughput. File size captures both duration and
// resolution/bitrate, so a 20 GB 4K file gets much more time than a 2 GB
// 1080p file.
func encodeTimeoutForSize(fileSize int64) time.Duration {
	threads := float64(encodeThreads())
	speedFactor := cpuGHz() / baselineGHz
	score := threads * speedFactor

	gb := float64(fileSize) / (1024 * 1024 * 1024)
	hours := (baseRateH / score) * safetyMult * gb
	if hours < minTimeoutH {
		hours = minTimeoutH
	}
	if hours > maxTimeoutH {
		hours = maxTimeoutH
	}
	return time.Duration(hours * float64(time.Hour))
}

// cpuGHz returns the average CPU clock speed in GHz by reading
// /proc/cpuinfo (Linux). Falls back to baselineGHz on error.
func cpuGHz() float64 {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return baselineGHz
	}
	var total float64
	var count int
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "cpu MHz") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		mhz, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err != nil {
			continue
		}
		total += mhz
		count++
	}
	if count == 0 {
		return baselineGHz
	}
	return (total / float64(count)) / 1000.0
}

func fmtWaste(score float64) string {
	const gb = 1024 * 1024 * 1024
	if score >= gb {
		return fmt.Sprintf("waste=%.1f GiB", score/gb)
	}
	const mb = 1024 * 1024
	return fmt.Sprintf("waste=%.0f MiB", score/mb)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
