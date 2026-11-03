package main

import (
	"io"
	"log/slog"
)

func newLogger(output io.Writer, debug bool) *slog.Logger {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{Level: level}))
}
