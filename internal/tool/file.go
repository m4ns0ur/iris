package tool

import (
	"context"
	"path/filepath"
)

func resolvePath(ctx context.Context, path string) (string, error) {
	workdir, hasWorkdir := FromWorkdir(ctx)

	if !hasWorkdir || workdir == "" {
		workdir = "."
	}

	if filepath.IsAbs(path) {
		return path, nil
	}

	return filepath.Join(workdir, path), nil
}
