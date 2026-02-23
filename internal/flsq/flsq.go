package flsq

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/snadrus/flicksqueeze/internal/ffmpeglib"
	"github.com/snadrus/flicksqueeze/internal/paths"
	"github.com/snadrus/flicksqueeze/internal/scanner"
	"github.com/snadrus/flicksqueeze/internal/validator"
	"github.com/snadrus/flicksqueeze/internal/vfs"
)

const (
	idleSleep   = 24 * time.Hour
	baselineGHz = 2.5
	baseRateH   = 3.0
	safetyMult  = 5.0
	minTimeoutH = 8.0
	maxTimeoutH = 96.0
)

type Config struct {
	RootPath    string
	NoDelete    bool
	FS          vfs.FS
	UploadQueue chan<- remoteUploadJob // when set, remote encodes queue uploads instead of blocking
	UploadWg    *sync.WaitGroup       // incremented per queued upload; wait before exit
}

// remoteUploadJob is sent to the upload worker after a remote encode completes.
type remoteUploadJob struct {
	LocalOut      string
	RemoteTmpPath string
	OutPath       string
	TmpDir        string
	EncType       string
	C             scanner.Candidate
	Cfg           Config
	Enc           *ffmpeglib.Encoder
	St            *status
}

// status tracks what the converter is doing so the interactive console
// can report it on demand.
type status struct {
	mu          sync.Mutex
	sessionStart time.Time
	file        string
	size        int64
	codec       string
	encType     string
	startedAt   time.Time
	ffmpegTime  string // latest time= from ffmpeg progress
	ffmpegSpd   string // latest speed= from ffmpeg progress
	filesTotal  int
	bytesSaved  int64
}

func (s *status) startEncode(path, codec, encType string, size int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.file = path
	s.size = size
	s.codec = codec
	s.encType = encType
	s.startedAt = time.Now()
	s.ffmpegTime = ""
	s.ffmpegSpd = ""
}

func (s *status) updateProgress(line string) {
	if !strings.Contains(line, "time=") {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := extractField(line, "time="); t != "" {
		s.ffmpegTime = t
	}
	if sp := extractField(line, "speed="); sp != "" {
		s.ffmpegSpd = sp
	}
}

func (s *status) finishEncode(saved int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filesTotal++
	s.bytesSaved += saved
	s.file = ""
}

func (s *status) print() {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "─── flicksqueeze status ───")
	if s.file != "" {
		elapsed := time.Since(s.startedAt).Round(time.Second)
		fmt.Fprintf(os.Stderr, "  encoding [%s]: %s\n", s.encType, filepath.Base(s.file))
		fmt.Fprintf(os.Stderr, "  codec: %s, size: %s, elapsed: %v\n",
			s.codec, scanner.HumanSize(s.size), elapsed)
		if s.ffmpegTime != "" {
			fmt.Fprintf(os.Stderr, "  progress: time=%s speed=%s\n", s.ffmpegTime, s.ffmpegSpd)
		}
	} else {
		fmt.Fprintln(os.Stderr, "  idle (scanning or waiting)")
	}
	sessionHours := time.Since(s.sessionStart).Hours()
	fmt.Fprintf(os.Stderr, "  session: %d files converted, %s saved", s.filesTotal, scanner.HumanSize(s.bytesSaved))
	if sessionHours >= 0.01 && s.bytesSaved > 0 {
		perHour := int64(float64(s.bytesSaved) / sessionHours)
		fmt.Fprintf(os.Stderr, " (%s/hr)", scanner.HumanSize(perHour))
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "───────────────────────────")
	fmt.Fprintln(os.Stderr, "  [q + Enter] quit after current encode")
	fmt.Fprintln(os.Stderr, "  [Enter]     refresh status")
	fmt.Fprintln(os.Stderr, "")
}

func extractField(line, key string) string {
	i := strings.Index(line, key)
	if i < 0 {
		return ""
	}
	val := line[i+len(key):]
	if j := strings.IndexByte(val, ' '); j >= 0 {
		val = val[:j]
	}
	return strings.TrimSpace(val)
}

// startConsole reads lines from stdin. Enter shows status, "q" triggers quit.
func startConsole(st *status) <-chan struct{} {
	quitCh := make(chan struct{})
	go func() {
		r := bufio.NewReader(os.Stdin)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			if line == "q" || line == "Q" || line == "quit" {
				close(quitCh)
				return
			}
			st.print()
		}
	}()
	return quitCh
}

func Run(ctx context.Context, cfg Config) error {
	enc := ffmpeglib.New()
	if cfg.FS.IsRemote() {
		enc.ProbeExec = cfg.FS.Exec
	}
	if err := enc.EnsureAvailable(ctx); err != nil {
		return err
	}

	st := status{sessionStart: time.Now()}
	quitCh := startConsole(&st)

	// scanCtx is cancelled when the user asks to quit, stopping the scanner
	// and the candidate loop. The parent ctx stays live so the in-flight
	// ffmpeg encode finishes gracefully before the process exits.
	scanCtx, cancelScan := context.WithCancel(ctx)
	defer cancelScan()
	go func() {
		select {
		case <-quitCh:
			log.Println(">>> graceful stop requested — will finish current encode then exit")
			cancelScan()
		case <-ctx.Done():
		}
	}()

	hw := enc.DetectHW(ctx)
	threads := encodeThreads()
	ghz := cpuGHz()
	score := math.Sqrt(float64(threads)) * (ghz / baselineGHz)
	ratePerGB := (baseRateH / score) * safetyMult
	log.Printf("flicksqueeze watching %s (threads=%d, cpu=%.1f GHz, ~%.1fh timeout per GB)",
		cfg.RootPath, threads, ghz, ratePerGB)
	if hw.UseHEVCFirst() {
		log.Printf("HEVC hw available (%s): will convert worst codecs to HEVC first, AV1 after", hw.HEVCProfile.Name)
	}
	if cfg.FS.IsRemote() {
		log.Println("remote mode: files will be downloaded for local encoding (upload overlaps with next download)")
	}
	log.Println("press Enter for status, q+Enter to quit")

	for {
		ch := make(chan scanner.Candidate)
		go scanner.Scan(scanCtx, cfg.FS, enc, cfg.RootPath, ch)

		var uploadChan chan remoteUploadJob
		var uploadWg sync.WaitGroup
		if cfg.FS.IsRemote() {
			uploadChan = make(chan remoteUploadJob, 4)
			cfg.UploadQueue = uploadChan
			cfg.UploadWg = &uploadWg
			go runUploadWorker(uploadChan, &uploadWg)
		}

		processed := 0
		for c := range ch {
			if scanCtx.Err() != nil {
				for range ch {
				}
				if cfg.FS.IsRemote() {
					close(uploadChan)
					uploadWg.Wait()
				}
				return nil
			}
			processed++
			log.Printf("candidate %d: [%s] %s (%s, codec=%s)",
				processed, scanner.HumanSize(c.Size), c.Path, fmtWaste(c.WasteScore), c.Codec)
			processCandidate(ctx, cfg, enc, c, hw, &st)
			if scanCtx.Err() != nil {
				for range ch {
				}
				if cfg.FS.IsRemote() {
					close(uploadChan)
					uploadWg.Wait()
				}
				return nil
			}
		}

		if cfg.FS.IsRemote() {
			close(uploadChan)
			uploadWg.Wait()
		}

		if scanCtx.Err() != nil {
			return nil
		}

		if processed == 0 {
			log.Println("no conversion candidates found, sleeping 24 hours")
			if !sleepCtx(scanCtx, idleSleep) {
				return nil
			}
		}
	}
}

var hevcFirstCodecs = map[string]bool{
	"h264": true, "mpeg4": true, "mpeg2video": true, "mpeg1video": true,
	"msmpeg4v1": true, "msmpeg4v2": true, "msmpeg4v3": true,
	"wmv1": true, "wmv2": true, "wmv3": true, "vp8": true,
}

func processCandidate(ctx context.Context, cfg Config, enc *ffmpeglib.Encoder, c scanner.Candidate, hw ffmpeglib.HWCaps, st *status) {
	fsys := cfg.FS
	timeout := encodeTimeoutForSize(c.Size)
	release, err := acquireLock(fsys, c.Path, timeout)
	if err != nil {
		log.Printf("skipping %s: %v", c.Path, err)
		return
	}
	defer release()

	// --- freshness check: input may have changed since scanning ---
	info, err := fsys.Stat(c.Path)
	if err != nil {
		log.Printf("skipping %s: file no longer exists", c.Path)
		return
	}
	if info.Size() != c.Size {
		log.Printf("skipping %s: size changed since scan (%d -> %d)", c.Path, c.Size, info.Size())
		return
	}

	outPath := paths.OutputPath(c.Path)

	// --- collision / restart detection ---
	if _, err := fsys.Stat(outPath); err == nil {
		comment, _ := enc.Comment(ctx, outPath)
		if !paths.IsOurComment(comment) {
			log.Printf("skipping %s: output %s already exists (not ours)", c.Path, outPath)
			return
		}
		if err := validator.Validate(ctx, fsys, enc, c.Path, outPath, c.Size); err == nil {
			log.Printf("restart recovery: %s already converted, finishing up", c.Path)
			comment, _ := enc.Comment(ctx, outPath)
			encType := "av1"
			if comment == paths.HEVCMetaComment {
				encType = "hevc"
			}
			finishConversion(fsys, c, outPath, cfg.RootPath, cfg.NoDelete, encType, st)
			return
		}
		log.Printf("stale output %s from previous failed run, removing", outPath)
		_ = fsys.Remove(outPath)
	}

	// --- choose encoder ---
	useHEVC := hw.UseHEVCFirst() && hevcFirstCodecs[strings.ToLower(c.Codec)]
	encType := "av1"
	if useHEVC {
		encType = "hevc"
	}
	st.startEncode(c.Path, c.Codec, encType, c.Size)
	progress := func(p ffmpeglib.ProgressLine) {
		st.updateProgress(p.Raw)
	}

	var queuedJob *remoteUploadJob
	if fsys.IsRemote() && cfg.UploadQueue != nil {
		queuedJob = &remoteUploadJob{C: c, Cfg: cfg, Enc: enc, St: st, EncType: encType}
	}
	if fsys.IsRemote() {
		err = encodeRemote(ctx, cfg, enc, c, outPath, useHEVC, hw, timeout, progress, encType, queuedJob)
	} else {
		if useHEVC {
			err = encodeHEVC(ctx, enc, c.Path, outPath, hw, timeout, progress)
		} else {
			err = encodeAV1(ctx, enc, c.Path, outPath, timeout, progress)
		}
	}

	if err != nil {
		log.Printf("encode failed for %s: %v", c.Path, err)
		_ = fsys.Remove(outPath)
		if ctx.Err() == nil {
			scanner.MarkFailed(fsys, cfg.RootPath, c.Path)
		}
		return
	}

	// --- remote async: upload runs in worker so next download can start immediately ---
	if fsys.IsRemote() && cfg.UploadQueue != nil && queuedJob != nil {
		cfg.UploadWg.Add(1)
		cfg.UploadQueue <- *queuedJob
		return
	}

	// --- validate (probes run where files live) ---
	if err := validator.Validate(ctx, fsys, enc, c.Path, outPath, c.Size); err != nil {
		log.Printf("validation failed for %s: %v", c.Path, err)
		_ = fsys.Remove(outPath)
		if ctx.Err() == nil {
			scanner.MarkFailed(fsys, cfg.RootPath, c.Path)
		}
		return
	}

	finishConversion(fsys, c, outPath, cfg.RootPath, cfg.NoDelete, encType, st)
}

// encodeRemote downloads the source, encodes locally, and optionally uploads (sync) or fills job for async upload.
// If job is non-nil, a unique tmpDir is used and the worker must remove it after uploading; upload is not done here.
func encodeRemote(ctx context.Context, cfg Config, enc *ffmpeglib.Encoder, c scanner.Candidate, outPath string, useHEVC bool, hw ffmpeglib.HWCaps, timeout time.Duration, progress func(ffmpeglib.ProgressLine), encType string, job *remoteUploadJob) error {
	tmpDir := filepath.Join(os.TempDir(), "flicksqueeze-work")
	if job != nil {
		tmpDir = filepath.Join(os.TempDir(), "flicksqueeze-work-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	} else {
		os.RemoveAll(tmpDir)
	}
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return err
	}
	if job == nil {
		defer os.RemoveAll(tmpDir)
	}

	localIn := filepath.Join(tmpDir, "input"+filepath.Ext(c.Path))
	localOut := filepath.Join(tmpDir, "output"+paths.OutputExt)

	log.Printf("downloading %s...", c.Path)
	if err := cfg.FS.CopyToLocal(c.Path, localIn); err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	var err error
	if useHEVC {
		err = encodeHEVC(ctx, enc, localIn, localOut, hw, timeout, progress)
	} else {
		err = encodeAV1(ctx, enc, localIn, localOut, timeout, progress)
	}
	if err != nil {
		if job != nil {
			os.RemoveAll(tmpDir)
		}
		return err
	}

	remoteTmpPath := outPath[:len(outPath)-len(filepath.Ext(outPath))] +
		".tmp-flsq-upload-" + paths.Hostname() + paths.OutputExt

	if job != nil {
		job.LocalOut = localOut
		job.RemoteTmpPath = remoteTmpPath
		job.OutPath = outPath
		job.TmpDir = tmpDir
		job.EncType = encType
		return nil
	}

	log.Printf("uploading result to %s...", remoteTmpPath)
	if err := cfg.FS.CopyFromLocal(localOut, remoteTmpPath); err != nil {
		return fmt.Errorf("upload failed: %w", err)
	}
	if err := cfg.FS.Rename(remoteTmpPath, outPath); err != nil {
		_ = cfg.FS.Remove(remoteTmpPath)
		return fmt.Errorf("remote rename failed: %w", err)
	}
	return nil
}

// runUploadWorker consumes upload jobs and runs upload, validate, and finishConversion.
// Allows the next candidate to start downloading/encoding while the previous upload runs.
func runUploadWorker(ch <-chan remoteUploadJob, wg *sync.WaitGroup) {
	for job := range ch {
		func() {
			defer wg.Done()
			ctx := context.Background()
			log.Printf("uploading result to %s...", job.RemoteTmpPath)
			if err := job.Cfg.FS.CopyFromLocal(job.LocalOut, job.RemoteTmpPath); err != nil {
				log.Printf("upload failed for %s: %v", job.C.Path, err)
				_ = job.Cfg.FS.Remove(job.OutPath)
				scanner.MarkFailed(job.Cfg.FS, job.Cfg.RootPath, job.C.Path)
				os.RemoveAll(job.TmpDir)
				return
			}
			if err := job.Cfg.FS.Rename(job.RemoteTmpPath, job.OutPath); err != nil {
				_ = job.Cfg.FS.Remove(job.RemoteTmpPath)
				log.Printf("remote rename failed for %s: %v", job.C.Path, err)
				scanner.MarkFailed(job.Cfg.FS, job.Cfg.RootPath, job.C.Path)
				os.RemoveAll(job.TmpDir)
				return
			}
			os.RemoveAll(job.TmpDir)
			if err := validator.Validate(ctx, job.Cfg.FS, job.Enc, job.C.Path, job.OutPath, job.C.Size); err != nil {
				log.Printf("validation failed for %s: %v", job.C.Path, err)
				_ = job.Cfg.FS.Remove(job.OutPath)
				scanner.MarkFailed(job.Cfg.FS, job.Cfg.RootPath, job.C.Path)
				return
			}
			finishConversion(job.Cfg.FS, job.C, job.OutPath, job.Cfg.RootPath, job.Cfg.NoDelete, job.EncType, job.St)
		}()
	}
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
		if err != nil {
			log.Printf("HEVC retry without subtitles failed: %v", err)
		}
	}
	return err
}

// isHighBitDepth reports whether the ffmpeg pix_fmt is 10- or 12-bit (e.g. yuv420p10le).
func isHighBitDepth(pixFmt string) bool {
	return strings.Contains(pixFmt, "10") || strings.Contains(pixFmt, "12")
}

func encodeAV1(ctx context.Context, enc *ffmpeglib.Encoder, inPath, outPath string, timeout time.Duration, progress func(ffmpeglib.ProgressLine)) error {
	log.Printf("AV1 sw encode %s -> %s", inPath, outPath)

	pixFmt := "yuv420p10le"
	if pf, err := enc.VideoPixFmt(ctx, inPath); err == nil && pf != "" && !isHighBitDepth(pf) {
		pixFmt = "yuv420p"
	}

	opts := ffmpeglib.AV1Options{
		CRF:              30,
		Preset:           5,
		Threads:          encodeThreads(),
		SkipIfAlreadyAV1: true,
		Container:        "mkv",
		PixFmt:           pixFmt,
		MetaComment:      paths.MetaComment,
	}

	encCtx, encCancel := context.WithTimeout(ctx, timeout)
	err := enc.EncodeToAV1SVT(encCtx, inPath, outPath, opts, progress)
	encCancel()

	if err != nil && !errors.Is(err, ffmpeglib.ErrAlreadyAV1) && ctx.Err() == nil {
		log.Printf("AV1 encode failed (retrying without subtitles): %v", err)
		_ = os.Remove(outPath)
		opts.DropSubtitles = true
		encCtx2, encCancel2 := context.WithTimeout(ctx, timeout)
		err = enc.EncodeToAV1SVT(encCtx2, inPath, outPath, opts, progress)
		encCancel2()
		if err != nil {
			log.Printf("AV1 retry without subtitles failed: %v", err)
		}
	}
	return err
}

func finishConversion(fsys vfs.FS, c scanner.Candidate, outPath, rootPath string, noDelete bool, encType string, st *status) {
	outInfo, err := fsys.Stat(outPath)
	if err != nil {
		log.Printf("error: cannot stat output %s: %v", outPath, err)
		return
	}
	outSize := outInfo.Size()
	saved := c.Size - outSize
	st.finishEncode(saved)
	log.Printf("validated OK [%s]: %s saved (%s -> %s)",
		encType, scanner.HumanSize(saved), scanner.HumanSize(c.Size), scanner.HumanSize(outSize))

	retireOriginal(fsys, c.Path, noDelete)

	finalPath := outPath
	base := filepath.Base(outPath)
	if strings.Contains(base, paths.AV1TmpTag) {
		dir := filepath.Dir(outPath)
		finalPath = filepath.Join(dir, strings.Replace(base, paths.AV1TmpTag, "", 1))
		if err := fsys.Rename(outPath, finalPath); err != nil {
			log.Printf("error: rename %s -> %s failed: %v", outPath, finalPath, err)
			return
		}
	}

	appendTally(fsys, rootPath, encType, c.Codec, c.Path, c.Size, finalPath, outSize)
	log.Printf("done: %s", finalPath)
}

func appendTally(fsys vfs.FS, rootPath, encType, fromCodec, origPath string, origSize int64, outPath string, outSize int64) {
	f, err := fsys.OpenFile(filepath.Join(rootPath, paths.TallyFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\t%s\t%s\t%d\t%d\t%s\t%s\n",
		time.Now().Format(time.RFC3339), encType, fromCodec, origSize, outSize, origPath, outPath)
}

func retireOriginal(fsys vfs.FS, path string, noDelete bool) {
	if noDelete {
		ext := filepath.Ext(path)
		tagged := path[:len(path)-len(ext)] + paths.DeleteMeTag + ext
		if err := fsys.Rename(path, tagged); err != nil {
			log.Printf("warning: could not rename original %s -> %s: %v", path, tagged, err)
		}
		return
	}
	if err := fsys.Remove(path); err != nil {
		log.Printf("warning: could not remove original %s: %v", path, err)
	}
}

func encodeThreads() int {
	return runtime.NumCPU()
}

func encodeTimeoutForSize(fileSize int64) time.Duration {
	threads := float64(encodeThreads())
	speedFactor := cpuGHz() / baselineGHz
	// sqrt(threads): SVT-AV1 has diminishing returns beyond ~6 threads
	// due to pipeline stages, synchronization, and memory bandwidth.
	score := math.Sqrt(threads) * speedFactor

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
