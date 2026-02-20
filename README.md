![FlickSqueeze](flicksqueeze.png)

**Re-encode your movie library to save disk space while you sleep.**

[![Release](https://img.shields.io/github/v/release/snadrus/flicksqueeze?style=flat-square&color=blue)](https://github.com/snadrus/flicksqueeze/releases)
[![Go](https://img.shields.io/badge/go-1.24+-00ADD8?style=flat-square&logo=go)](https://pkg.go.dev/github.com/snadrus/flicksqueeze)
[![License](https://img.shields.io/badge/license-MIT-green?style=flat-square)](LICENSE)
![Platform](https://img.shields.io/badge/platform-linux%20%7C%20macOS%20%7C%20windows-lightgrey?style=flat-square)

---

Point flicksqueeze at a movie folder (local or remote via SSH) and walk away. It finds the biggest space-wasters, re-encodes them to AV1 (or HEVC as a fast first pass when your GPU supports it), validates every output, and swaps in the smaller file — one at a time, at the lowest CPU/IO priority so it never gets in your way.

## Highlights

- **Waste-ranked queue** — scores files by `size x codec inefficiency` so the worst offenders convert first
- **Hardware HEVC pre-pass** — got a GPU with HEVC but not AV1? It does a fast HEVC pass first, AV1 later
- **Bulletproof validation** — output must be smaller, > 10 MB, and duration-matched before the original is touched
- **Multi-machine safe** — per-file lock files let you run multiple instances on the same shared folder
- **Remote mode** — encode files on an SSH server using your local GPU
- **Interactive console** — press Enter for live status, `q`+Enter to gracefully stop, Ctrl+C to abort

## Quick Start

```bash
# Install
go install github.com/snadrus/flicksqueeze@latest

# Run
flicksqueeze /path/to/movies
```

## Prerequisites

flicksqueeze requires **ffmpeg** and **ffprobe** on your `PATH`. Run `flicksqueeze` with no arguments to check:

```
$ flicksqueeze
flicksqueeze v1.0.0
  commit:  abc1234
  built:   2026-02-19T21:36:30Z
  go:      go1.24.0 linux/amd64

  ...

DEPENDENCIES
  ✓ ffmpeg       ffmpeg version 6.1.1 Copyright (c) 2000-2023 the FFmpeg developers
  ✓ ffprobe      ffprobe version 6.1.1 Copyright (c) 2007-2023 the FFmpeg developers
```

Install ffmpeg if missing:

| Platform | Command |
|----------|---------|
| Ubuntu / Debian | `sudo apt install ffmpeg` |
| macOS | `brew install ffmpeg` |
| Windows | `winget install Gyan.FFmpeg` |

## Installation

### From source (recommended)

```bash
go install github.com/snadrus/flicksqueeze@latest
```

### Build with version info

```bash
git clone https://github.com/snadrus/flicksqueeze.git
cd flicksqueeze
make build      # produces ./flicksqueeze with embedded git tag + commit + date
make install    # installs to $GOPATH/bin
```

## Usage

### Local folder

```bash
# Convert movies in-place (originals are deleted after validation)
flicksqueeze /path/to/movies

# Keep originals renamed with _deleteMe suffix
flicksqueeze --no-delete /path/to/movies
```

### Remote folder via SSH

Encode files that live on a remote server using your local GPU:

```bash
flicksqueeze ssh://andy@homeserver/home/andy/movies
flicksqueeze --no-delete ssh://user@nas.local:2222/mnt/media
```

Files are downloaded, encoded locally, uploaded back, and validated remotely. The SSH connection tries your SSH agent first, then prompts for a password.

### Flags

| Flag | Description |
|------|-------------|
| `--no-delete` | Keep originals (renamed with `_deleteMe` suffix) |
| `--version`, `-v` | Print version and exit |

### Interactive Console

While running, the terminal accepts commands:

```
─── flicksqueeze status ───
  encoding [av1]: Aladdin (1992) 1080p.mkv
  codec: h264, size: 4.4 GiB, elapsed: 57m52s
  session: 1 files converted, 1.5 GiB saved
───────────────────────────
  [q + Enter] quit after current encode
  [Enter]     refresh status
```

| Key | Action |
|-----|--------|
| Enter | Print current status |
| `q` + Enter | Finish current encode, then exit |
| Ctrl+C | Abort immediately |

### Multiple Machines

Run flicksqueeze on several machines pointing at the same folder (local or SSH). Per-file lock files prevent collisions. Machines with HEVC hardware churn through h264 files fast; machines without focus on AV1.

```bash
# Machine A (has NVIDIA GPU with HEVC)
flicksqueeze /shared/movies

# Machine B (CPU only, same shared folder)
flicksqueeze /shared/movies
```

### Running in the Background

```bash
# Simple background
nohup flicksqueeze /movies >> /var/log/flicksqueeze.log 2>&1 &

# systemd service
# flicksqueeze runs at nice 19 / ionice idle — it won't interfere with other work
```

## How It Works

```
┌─────────┐     ┌──────┐     ┌─────────┐     ┌──────────┐     ┌─────────┐
│  Scan   │────▶│ Rank │────▶│ Convert │────▶│ Validate │────▶│ Replace │
└─────────┘     └──────┘     └─────────┘     └──────────┘     └─────────┘
      ▲                                                              │
      └──────────────────────────────────────────────────────────────┘
                          (repeat / sleep 24h if idle)
```

1. **Scan** — walks the folder tree, skips files < 10 MB or modified within 3 days, probes codecs via ffprobe (cached in a per-machine index)
2. **Rank** — scores each file by `size * codec_waste_multiplier` (h264 = 2x, mpeg2 = 4x, hevc = 1.4x, ...)
3. **Convert** — after 1000 files scanned, starts encoding the worst candidate; streams more candidates as scanning continues
4. **Validate** — checks output is smaller, > 10 MB, and duration matches within 5 seconds
5. **Replace** — retires the original, renames output to the original filename
6. **Repeat** — loops back to scan; sleeps 24 hours when nothing is left to do

## Configuration

All settings are compiled in. Key values:

| Setting | Value | Source |
|---------|-------|--------|
| AV1 CRF | 28 | `internal/flsq/flsq.go` |
| AV1 preset | 5 | `internal/flsq/flsq.go` |
| HEVC CQ/QP | 18 | `internal/ffmpeglib/ffmpeg_adapter.go` |
| Min file size | 10 MB | `internal/paths/paths.go` |
| Stale age | 3 days | `internal/scanner/scanner.go` |
| Idle sleep | 24 hours | `internal/flsq/flsq.go` |

## Files Created

flicksqueeze creates a few bookkeeping files inside the movie folder:

| File | Purpose |
|------|---------|
| `.flicksqueeze-<hostname>.idx` | Codec cache — avoids re-probing unchanged files |
| `.flicksqueeze.log` | Tally of all conversions (TSV: timestamp, type, codec, before, after, paths) |
| `.flicksqueeze.failures` | Paths that failed encoding (skipped on future scans) |
| `*.flsq-lock` | Per-file lock (removed after encode completes) |

## Contributing

Pull requests welcome. Please open an issue first for larger changes.

## License

[MIT](LICENSE) © 2026 Andrew Jackson (Ajax)
