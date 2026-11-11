package tui

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "iris-tui-logs-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create test log directory: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("IRIS_LOG_DIR", dir); err != nil {
		fmt.Fprintf(os.Stderr, "set test log directory: %v\n", err)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
