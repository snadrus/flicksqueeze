package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/snadrus/flicksqueeze/internal/flsq"
)

func main() {
	var cfg flsq.Config

	args := os.Args[1:]
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "--no-delete":
			cfg.NoDelete = true
		default:
			fmt.Fprintf(os.Stderr, "unknown flag: %s\n", args[0])
			os.Exit(1)
		}
		args = args[1:]
	}
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "usage: %s [--no-delete] <movie-folder>\n", os.Args[0])
		os.Exit(1)
	}
	cfg.RootPath = args[0]

	info, err := os.Stat(cfg.RootPath)
	if err != nil || !info.IsDir() {
		log.Fatalf("path %q is not an accessible directory", cfg.RootPath)
	}

	cfg.QuitCh = flsq.ListenForQuit()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := flsq.Run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
	log.Println("shutting down")
}
