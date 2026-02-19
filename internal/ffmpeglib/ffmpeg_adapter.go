package ffmpeglib

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/snadrus/flicksqueeze/internal/paths"
)

// ErrAlreadyAV1 is returned when SkipIfAlreadyAV1 is set and the input is AV1.
var ErrAlreadyAV1 = errors.New("input already AV1")

type Encoder struct {
	FFmpegPath  string // default "ffmpeg"
	FFprobePath string // default "ffprobe"
}

func New() *Encoder {
	return &Encoder{
		FFmpegPath:  "ffmpeg",
		FFprobePath: "ffprobe",
	}
}

type AV1Options struct {
	CRF         int    // e.g. 28
	Preset      int    // SVT-AV1 preset, e.g. 5 or 6
	Threads     int    // 0 = ffmpeg default
	PixFmt      string // e.g. "yuv420p10le"
	Container   string // e.g. "mkv" (recommended), or "mp4" (works but pick a modern player stack)
	MetaComment string // written to the container comment tag for identification

	SkipIfAlreadyAV1 bool
	DropSubtitles    bool // use -sn instead of -c:s copy (fallback for incompatible subs)
	ExtraFFmpegArgs  []string
}

func (o AV1Options) withDefaults() AV1Options {
	if o.CRF == 0 {
		o.CRF = 28
	}
	if o.Preset == 0 {
		o.Preset = 5
	}
	if o.PixFmt == "" {
		o.PixFmt = "yuv420p10le"
	}
	if o.Container == "" {
		o.Container = "mkv"
	}
	return o
}

type ProgressLine struct {
	Raw string
}

type RunResult struct {
	Args      []string
	Stdout    string
	Stderr    string
	ExitError error
}

func (e *Encoder) EnsureAvailable(ctx context.Context) error {
	for _, bin := range []string{e.FFmpegPath, e.FFprobePath} {
		cmd := exec.CommandContext(ctx, bin, "-version")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s not runnable: %w", bin, err)
		}
	}
	return nil
}

// EncodeToAV1SVT encodes the input file to AV1 (SVT-AV1) into outPath.
// It writes to a temp file next to outPath and renames on success.
func (e *Encoder) EncodeToAV1SVT(ctx context.Context, inPath, outPath string, opt AV1Options, progress func(ProgressLine)) (*RunResult, error) {
	opt = opt.withDefaults()

	if opt.SkipIfAlreadyAV1 {
		vcodec, err := e.VideoCodec(ctx, inPath)
		if err == nil && strings.EqualFold(vcodec, "av1") {
			return nil, ErrAlreadyAV1
		}
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return nil, err
	}

	outExt := filepath.Ext(outPath)
	tmpPath := outPath[:len(outPath)-len(outExt)] + ".tmp-flsq-av1-" + paths.Hostname() + outExt
	_ = os.Remove(tmpPath) // clean up stale temp from a previous crash

	// Build ffmpeg args.
	args := []string{
		"-hide_banner",
		"-y",
		"-i", inPath,

		"-map", "0",

		"-c:v", "libsvtav1",
		"-crf", strconv.Itoa(opt.CRF),
		"-preset", strconv.Itoa(opt.Preset),
		"-pix_fmt", opt.PixFmt,
		"-g", "240",

		"-c:a", "copy",
	}

	if opt.DropSubtitles {
		args = append(args, "-sn")
	} else {
		args = append(args, "-c:s", "copy")
	}

	args = append(args, "-metadata", "comment="+opt.MetaComment)

	if opt.Threads > 0 {
		args = append(args, "-threads", strconv.Itoa(opt.Threads))
	}

	if f := containerMuxer(opt.Container); f != "" {
		args = append(args, "-f", f)
	}

	args = append(args, opt.ExtraFFmpegArgs...)

	args = append(args, tmpPath)

	res, err := runCmdStreaming(ctx, e.FFmpegPath, args, progress)
	if err != nil {
		_ = os.Remove(tmpPath)
		return res, err
	}

	// Replace output atomically-ish: rename over existing if possible.
	// On Windows you’d need extra handling; on Linux rename works well.
	if err := os.Rename(tmpPath, outPath); err != nil {
		_ = os.Remove(tmpPath)
		return res, err
	}

	return res, nil
}

func runCmdStreaming(ctx context.Context, bin string, args []string, progress func(ProgressLine)) (*RunResult, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	configureCmd(cmd, bin, args)

	var stdoutBuf, stderrBuf bytes.Buffer
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Stream stderr lines to progress callback (ffmpeg writes progress to stderr).
	// Also capture stdout/stderr fully for logging.
	done := make(chan struct{}, 2)

	go func() {
		defer func() { done <- struct{}{} }()
		tee := io.TeeReader(stdoutPipe, &stdoutBuf)
		_, _ = io.Copy(io.Discard, tee)
	}()

	go func() {
		defer func() { done <- struct{}{} }()
		tee := io.TeeReader(stderrPipe, &stderrBuf)
		sc := bufio.NewScanner(tee)
		// Allow longer lines (ffmpeg can be chatty)
		buf := make([]byte, 0, 64*1024)
		sc.Buffer(buf, 2*1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if progress != nil {
				progress(ProgressLine{Raw: line})
			}
		}
	}()

	<-done
	<-done

	waitErr := cmd.Wait()

	res := &RunResult{
		Args:   append([]string{bin}, args...),
		Stdout: stdoutBuf.String(),
		Stderr: stderrBuf.String(),
	}

	if waitErr != nil {
		res.ExitError = waitErr
		return res, fmt.Errorf("ffmpeg failed: %w", waitErr)
	}

	return res, nil
}

// ---- ffprobe helpers ----

// VideoCodec returns the codec_name for the first video stream (e.g. "h264", "hevc", "av1").
func (e *Encoder) VideoCodec(ctx context.Context, inPath string) (string, error) {
	out, err := e.ffprobe(ctx,
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=codec_name",
		"-of", "default=nokey=1:noprint_wrappers=1",
		inPath,
	)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(out)
	if s == "" {
		return "", errors.New("no video stream found")
	}
	return s, nil
}

// DurationSeconds returns the container duration if available.
func (e *Encoder) DurationSeconds(ctx context.Context, inPath string) (float64, error) {
	out, err := e.ffprobe(ctx,
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=nokey=1:noprint_wrappers=1",
		inPath,
	)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(out)
	if s == "" {
		return 0, errors.New("duration unavailable")
	}
	return strconv.ParseFloat(s, 64)
}

// VideoBitrate returns the bit_rate of the first video stream in bits/s.
func (e *Encoder) VideoBitrate(ctx context.Context, inPath string) (int64, error) {
	out, err := e.ffprobe(ctx,
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=bit_rate",
		"-of", "default=nokey=1:noprint_wrappers=1",
		inPath,
	)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(out)
	if s == "" || s == "N/A" {
		return 0, errors.New("video bitrate unavailable")
	}
	return strconv.ParseInt(s, 10, 64)
}

// Comment returns the container-level "comment" metadata tag, if any.
func (e *Encoder) Comment(ctx context.Context, inPath string) (string, error) {
	out, err := e.ffprobe(ctx,
		"-v", "error",
		"-show_entries", "format_tags=comment",
		"-of", "default=nokey=1:noprint_wrappers=1",
		inPath,
	)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// ---- hardware encoder detection ----

type hwProfile struct {
	Name      string
	InitArgs  []string // before -i (e.g. -vaapi_device)
	VideoArgs []string // encoder + quality args
}

// CQ/QP 18 is near-visually-lossless, appropriate since this is an
// intermediate that will later be re-encoded to AV1.
var hevcHWProfiles = []hwProfile{
	{Name: "hevc_nvenc", VideoArgs: []string{"-c:v", "hevc_nvenc", "-preset", "p4", "-cq", "18", "-b:v", "0"}},
	{Name: "hevc_qsv", VideoArgs: []string{"-c:v", "hevc_qsv", "-global_quality", "18"}},
	{Name: "hevc_vaapi",
		InitArgs:  []string{"-vaapi_device", "/dev/dri/renderD128"},
		VideoArgs: []string{"-vf", "format=nv12,hwupload", "-c:v", "hevc_vaapi", "-qp", "18"}},
	{Name: "hevc_amf", VideoArgs: []string{"-c:v", "hevc_amf", "-quality", "quality", "-qp_i", "18", "-qp_p", "18"}},
}

var av1HWNames = []string{"av1_nvenc", "av1_vaapi", "av1_qsv", "av1_amf"}

// HWCaps describes what hardware encoding the system supports.
type HWCaps struct {
	HEVCProfile *hwProfile // nil if no usable HEVC hw encoder
	HasAV1HW    bool
}

// UseHEVCFirst returns true when the system should encode to HEVC first
// (HEVC hw available, AV1 hw not), letting the scanner come back for AV1 later.
func (h HWCaps) UseHEVCFirst() bool {
	return h.HEVCProfile != nil && !h.HasAV1HW
}

// DetectHW probes ffmpeg for hardware encoder support.
func (e *Encoder) DetectHW(ctx context.Context) HWCaps {
	cmd := exec.CommandContext(ctx, e.FFmpegPath, "-hide_banner", "-encoders")
	out, err := cmd.Output()
	if err != nil {
		return HWCaps{}
	}
	list := string(out)

	var caps HWCaps
	for i := range hevcHWProfiles {
		if strings.Contains(list, hevcHWProfiles[i].Name) {
			caps.HEVCProfile = &hevcHWProfiles[i]
			break
		}
	}
	for _, name := range av1HWNames {
		if strings.Contains(list, name) {
			caps.HasAV1HW = true
			break
		}
	}
	return caps
}

// EncodeToHEVCHW does a fast hardware HEVC encode. The output replaces the
// original, and the scanner will later pick it up for AV1 conversion.
func (e *Encoder) EncodeToHEVCHW(ctx context.Context, inPath, outPath string, prof hwProfile, comment string, dropSubs bool, progress func(ProgressLine)) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}

	outExt := filepath.Ext(outPath)
	tmpPath := outPath[:len(outPath)-len(outExt)] + ".tmp-flsq-hevc-" + paths.Hostname() + outExt
	_ = os.Remove(tmpPath)

	args := append([]string{}, prof.InitArgs...)
	args = append(args, "-hide_banner", "-y", "-i", inPath, "-map", "0")
	args = append(args, prof.VideoArgs...)
	args = append(args, "-c:a", "copy")
	if dropSubs {
		args = append(args, "-sn")
	} else {
		args = append(args, "-c:s", "copy")
	}
	if comment != "" {
		args = append(args, "-metadata", "comment="+comment)
	}
	if f := containerMuxer("mkv"); f != "" {
		args = append(args, "-f", f)
	}
	args = append(args, tmpPath)

	_, err := runCmdStreaming(ctx, e.FFmpegPath, args, progress)
	if err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

var muxerNames = map[string]string{
	"mkv":  "matroska",
	"webm": "webm",
	"mp4":  "mp4",
	"mov":  "mov",
}

func containerMuxer(container string) string {
	return muxerNames[strings.ToLower(container)]
}

func (e *Encoder) ffprobe(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, e.FFprobePath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("ffprobe error: %w: %s", err, stderr.String())
	}
	return string(out), nil
}
