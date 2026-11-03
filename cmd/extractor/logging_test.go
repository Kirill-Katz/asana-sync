package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewLoggerTextFormatAndLevels(t *testing.T) {
	for _, debug := range []bool{false, true} {
		var output bytes.Buffer
		logger := newLogger(&output, debug)
		logger.Debug("page fetched", "count", 2)
		logger.Info("retry scheduled", "delay", "1s")
		logger.Error("sync failed", "job", "projects")

		logs := output.String()
		if strings.Contains(logs, "level=DEBUG") != debug {
			t.Errorf("debug=%v: unexpected debug visibility", debug)
		}
		for _, expected := range []string{
			`level=INFO msg="retry scheduled" delay=1s`,
			`level=ERROR msg="sync failed" job=projects`,
		} {
			if !strings.Contains(logs, expected) {
				t.Errorf("missing %q in logs: %s", expected, logs)
			}
		}
		if strings.HasPrefix(logs, "{") || !strings.HasPrefix(logs, "time=") {
			t.Fatalf("expected space-separated text output, got %s", logs)
		}
	}
}
