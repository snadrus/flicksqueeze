package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/snadrus/flicksqueeze/internal/flsq"
	"github.com/snadrus/flicksqueeze/internal/vfs"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	var cfg flsq.Config

	args := os.Args[1:]
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "--no-delete":
			cfg.NoDelete = true
		case "--version", "-v":
			fmt.Printf("flicksqueeze %s (commit %s, built %s)\n", version, commit, buildDate)
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown flag: %s\n", args[0])
			os.Exit(1)
		}
		args = args[1:]
	}
	if len(args) < 1 {
		printHelp()
		return
	}

	rawPath := strings.TrimSpace(args[0])
	rawPath = strings.Trim(rawPath, `"'`)
	rawPath = filepath.Clean(rawPath)
	fmt.Fprintf(os.Stderr, "flicksqueeze %s\n", version)

	if strings.HasPrefix(rawPath, "ssh://") {
		sftpFS, remotePath, err := vfs.DialSSH(rawPath)
		if err != nil {
			log.Fatalf("ssh connect failed: %v", err)
		}
		defer sftpFS.Close()
		cfg.FS = sftpFS
		cfg.RootPath = remotePath
	} else {
		info, err := os.Stat(rawPath)
		if err != nil || !info.IsDir() {
			log.Fatalf("path %q is not an accessible directory", rawPath)
		}
		cfg.FS = vfs.Local{}
		cfg.RootPath = rawPath
	}

	if err := ensureFFmpegInPath(); err != nil {
		log.Fatal(err)
	}

	sigs := []os.Signal{os.Interrupt}
	if runtime.GOOS != "windows" {
		sigs = append(sigs, syscall.SIGTERM)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), sigs...)
	defer cancel()

	if err := flsq.Run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
	log.Println("shutting down")
}

func printHelp() {
	fmt.Println("flicksqueeze " + version)
	if commit != "unknown" {
		fmt.Printf("  commit:  %s\n", commit)
	}
	if buildDate != "unknown" {
		fmt.Printf("  built:   %s\n", buildDate)
	}
	fmt.Printf("  go:      %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	fmt.Println()

	fmt.Println("Re-encode your movie library to AV1/HEVC,")
	fmt.Println("saving disk space while you sleep.")
	fmt.Println()

	fmt.Println("USAGE")
	fmt.Println("  flicksqueeze [flags] <movie-folder | ssh://user@host/path>")
	fmt.Println()
	fmt.Println("FLAGS")
	fmt.Println("  --no-delete   Keep originals (renamed with _deleteMe suffix)")
	fmt.Println("  --version     Print version and exit")
	fmt.Println()
	fmt.Println("EXAMPLES")
	fmt.Println("  flicksqueeze /path/to/movies")
	fmt.Println("  flicksqueeze --no-delete /path/to/movies")
	fmt.Println("  flicksqueeze ssh://username@homeserver/home/username/movies")
	fmt.Println()
	fmt.Println("INTERACTIVE")
	fmt.Println("  [Enter]       Show status while running")
	fmt.Println("  [q + Enter]   Quit after current encode finishes")
	fmt.Println("  [Ctrl+C]      Abort immediately")
	fmt.Println()

	fmt.Println("DEPENDENCIES")
	checkBin("ffmpeg")
	checkBin("ffprobe")
	fmt.Println()
}

// winget exit codes that mean "already installed" or "no update" (not a real failure)
const (
	wingetUpdateNotApplicable   = -1978335189 // 0x8A15002B: No applicable update found
	wingetPackageAlreadyInstalled = -1978335135 // 0x8A150061: Package already installed
	wingetInstallAlreadyInstalled = -1978334963 // 0x8A15010D: Another version already installed
)

// ensureFFmpegInPath checks that ffmpeg and ffprobe are available. On Windows, looks for the binary (PATH, then WinGet packages) and only runs winget if not found.
func ensureFFmpegInPath() error {
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		if _, err := exec.LookPath("ffprobe"); err == nil {
			return nil
		}
	}
	if runtime.GOOS != "windows" {
		if _, err := exec.LookPath("ffmpeg"); err != nil {
			return fmt.Errorf("ffmpeg not found on PATH: %w", err)
		}
		return fmt.Errorf("ffprobe not found on PATH")
	}
	// Windows: look for binary in WinGet packages first (no winget call)
	if binDir := findFFmpegInWinGetPackages(); binDir != "" {
		pathEnv := os.Getenv("PATH")
		os.Setenv("PATH", binDir+string(filepath.ListSeparator)+pathEnv)
		if _, err := exec.LookPath("ffmpeg"); err == nil {
			if _, err := exec.LookPath("ffprobe"); err == nil {
				return nil
			}
		}
	}
	// Binary not found anywhere; install via winget
	log.Println("ffmpeg/ffprobe not found; installing via winget (Gyan.FFmpeg)...")
	cmd := exec.Command("winget", "install", "Gyan.FFmpeg", "--accept-package-agreements", "--accept-source-agreements")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		// Treat "already installed" / "no update" as success (Windows may report as signed or unsigned)
		if exitErr, ok := err.(*exec.ExitError); ok {
			code := exitErr.ExitCode()
			codeU := uint32(code)
			if code == wingetUpdateNotApplicable || code == wingetPackageAlreadyInstalled || code == wingetInstallAlreadyInstalled ||
				codeU == 0x8A15002B || codeU == 0x8A150061 || codeU == 0x8A15010D {
				err = nil
			}
		}
		if err != nil {
			return fmt.Errorf("winget install Gyan.FFmpeg failed: %w (install manually: winget install Gyan.FFmpeg)", err)
		}
	}
	// After install, PATH may only apply to new processes; try WinGet location again
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		return nil
	}
	if binDir := findFFmpegInWinGetPackages(); binDir != "" {
		pathEnv := os.Getenv("PATH")
		os.Setenv("PATH", binDir+string(filepath.ListSeparator)+pathEnv)
		if _, err := exec.LookPath("ffmpeg"); err == nil {
			return nil
		}
	}
	return fmt.Errorf("ffmpeg was installed; please run flicksqueeze again in a new terminal so PATH is updated")
}

// findFFmpegInWinGetPackages returns the bin directory containing ffmpeg.exe under %LOCALAPPDATA%\Microsoft\WinGet\Packages, or "".
func findFFmpegInWinGetPackages() string {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		return ""
	}
	packagesDir := filepath.Join(localAppData, "Microsoft", "WinGet", "Packages")
	entries, err := os.ReadDir(packagesDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "Gyan.FFmpeg") {
			continue
		}
		pkgDir := filepath.Join(packagesDir, e.Name())
		// Gyan.FFmpeg installs as .../ffmpeg-<version>-full_build/bin
		subs, _ := os.ReadDir(pkgDir)
		for _, s := range subs {
			if !s.IsDir() || !strings.HasPrefix(s.Name(), "ffmpeg-") {
				continue
			}
			binDir := filepath.Join(pkgDir, s.Name(), "bin")
			ffmpegExe := filepath.Join(binDir, "ffmpeg.exe")
			if _, err := os.Stat(ffmpegExe); err == nil {
				return binDir
			}
		}
	}
	return ""
}

func checkBin(name string) {
	path, err := exec.LookPath(name)
	if err != nil {
		fmt.Printf("  ✗ %-12s NOT FOUND\n", name)
		switch runtime.GOOS {
		case "linux":
			fmt.Printf("    → sudo apt install %s\n", name)
		case "darwin":
			fmt.Printf("    → brew install %s\n", name)
		case "windows":
			fmt.Printf("    → winget install Gyan.FFmpeg\n")
		}
		return
	}
	out, err := exec.Command(path, "-version").Output()
	if err != nil {
		fmt.Printf("  ✓ %-12s %s (could not read version)\n", name, path)
		return
	}
	first := strings.SplitN(string(out), "\n", 2)[0]
	fmt.Printf("  ✓ %-12s %s\n", name, first)
}
