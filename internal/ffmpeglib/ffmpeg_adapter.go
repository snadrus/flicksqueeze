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

var ErrAlreadyAV1 = errors.New("input already AV1")

type ExecFunc func(ctx context.Context, name string, args ...string) (stdout []byte, stderr []byte, err error)

type Encoder struct {
	FFmpegPath  string
	FFprobePath string
	ProbeExec   ExecFunc
}

func New() *Encoder {
	return &Encoder{
		FFmpegPath:  "ffmpeg",
		FFprobePath: "ffprobe",
	}
}

type AV1Options struct {
	CRF         int
	Preset      int
	Threads     int
	PixFmt      string
	Container   string
	MetaComment string

	SkipIfAlreadyAV1 bool
	DropSubtitles    bool
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

func (e *Encoder) EnsureAvailable(ctx context.Context) error {
	for _, bin := range []string{e.FFmpegPath, e.FFprobePath} {
		cmd := exec.CommandContext(ctx, bin, "-version")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s not runnable: %w", bin, err)
		}
	}
	return nil
}

func (e *Encoder) EncodeToAV1SVT(ctx context.Context, inPath, outPath string, opt AV1Options, progress func(ProgressLine)) error {
	opt = opt.withDefaults()

	if opt.SkipIfAlreadyAV1 {
		vcodec, err := e.VideoCodec(ctx, inPath)
		if err == nil && strings.EqualFold(vcodec, "av1") {
			return ErrAlreadyAV1
		}
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}

	outExt := filepath.Ext(outPath)
	tmpPath := outPath[:len(outPath)-len(outExt)] + ".tmp-flsq-av1-" + paths.Hostname() + outExt
	_ = os.Remove(tmpPath)

	args := []string{
		"-nostdin",
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

	if err := runCmdStreaming(ctx, e.FFmpegPath, args, progress); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	if err := os.Rename(tmpPath, outPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// runCmdStreaming executes a command, streaming stderr lines to the progress
// callback. Stdout is drained and discarded. Nothing is buffered in RAM.
func runCmdStreaming(ctx context.Context, bin string, args []string, progress func(ProgressLine)) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	configureCmd(cmd, bin, args)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	done := make(chan struct{}, 2)

	go func() {
		defer func() { done <- struct{}{} }()
		_, _ = io.Copy(io.Discard, stdoutPipe)
	}()

	go func() {
		defer func() { done <- struct{}{} }()
		sc := bufio.NewScanner(stderrPipe)
		buf := make([]byte, 0, 64*1024)
		sc.Buffer(buf, 2*1024*1024)
		for sc.Scan() {
			if progress != nil {
				progress(ProgressLine{Raw: sc.Text()})
			}
		}
	}()

	<-done
	<-done

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("ffmpeg failed: %w", err)
	}
	return nil
}

// ---- ffprobe helpers ----

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
	InitArgs  []string
	VideoArgs []string
}

var hevcHWProfiles = []hwProfile{
	{Name: "hevc_nvenc", VideoArgs: []string{"-c:v", "hevc_nvenc", "-preset", "p4", "-cq", "18", "-b:v", "0"}},
	{Name: "hevc_qsv", VideoArgs: []string{"-c:v", "hevc_qsv", "-global_quality", "18"}},
	{Name: "hevc_vaapi",
		InitArgs:  []string{"-vaapi_device", "/dev/dri/renderD128"},
		VideoArgs: []string{"-vf", "format=nv12,hwupload", "-c:v", "hevc_vaapi", "-qp", "18"}},
	{Name: "hevc_amf", VideoArgs: []string{"-c:v", "hevc_amf", "-quality", "quality", "-qp_i", "18", "-qp_p", "18"}},
}

var av1HWNames = []string{"av1_nvenc", "av1_vaapi", "av1_qsv", "av1_amf"}

type HWCaps struct {
	HEVCProfile *hwProfile
	HasAV1HW    bool
}

func (h HWCaps) UseHEVCFirst() bool {
	return h.HEVCProfile != nil && !h.HasAV1HW
}

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

func (e *Encoder) EncodeToHEVCHW(ctx context.Context, inPath, outPath string, prof hwProfile, comment string, dropSubs bool, progress func(ProgressLine)) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}

	outExt := filepath.Ext(outPath)
	tmpPath := outPath[:len(outPath)-len(outExt)] + ".tmp-flsq-hevc-" + paths.Hostname() + outExt
	_ = os.Remove(tmpPath)

	args := append([]string{}, prof.InitArgs...)
	args = append(args, "-nostdin", "-hide_banner", "-y", "-i", inPath, "-map", "0")
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

	if err := runCmdStreaming(ctx, e.FFmpegPath, args, progress); err != nil {
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
	if e.ProbeExec != nil {
		out, stderr, err := e.ProbeExec(ctx, e.FFprobePath, args...)
		if err != nil {
			return "", fmt.Errorf("ffprobe error: %w: %s", err, string(stderr))
		}
		return string(out), nil
	}
	cmd := exec.CommandContext(ctx, e.FFprobePath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("ffprobe error: %w: %s", err, stderr.String())
	}
	return string(out), nil
}
