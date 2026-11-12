package policy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/m4ns0ur/iris/internal/agentdir"
)

type access int

const (
	accessRead access = iota
	accessWrite
)

// ResolveRead validates a path for reading and returns its canonical
// absolute form. Symlinks are resolved before the containment check.
func (p *Policy) ResolveRead(path string) (string, error) {
	return p.resolve(path, accessRead)
}

// ResolveWrite validates a path for writing and returns its canonical
// absolute form. In read-only mode every write is rejected.
func (p *Policy) ResolveWrite(path string) (string, error) {
	return p.resolve(path, accessWrite)
}

func (p *Policy) resolve(path string, kind access) (string, error) {
	if p == nil {
		return "", fmt.Errorf("%w: nil policy", ErrInvalidPolicy)
	}

	if path == "" || strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%w: empty path", ErrPathResolution)
	}
	if strings.ContainsRune(path, '\x00') {
		return "", fmt.Errorf("%w: path contains a NUL byte", ErrPathResolution)
	}

	mode, root, readRoots, writeRoots := p.rootsSnapshot()

	if kind == accessWrite && mode == ModeReadOnly {
		settings := "settings.toml"
		if dir, err := agentdir.Dir(); err == nil {
			settings = filepath.Join(dir, "settings.toml")
		}
		return "", fmt.Errorf(
			"%w: cannot modify %q; set security.mode = %q in %s to enable writes",
			ErrReadOnlyMode,
			path,
			ModeWorkspaceWrite,
			settings,
		)
	}

	absolute := path
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(root, absolute)
	}
	absolute = filepath.Clean(absolute)

	// danger-full-access still resolves relative paths against the workspace
	// but does not confine them.
	if mode == ModeDangerFullAccess {
		return absolute, nil
	}

	resolved, err := canonicalize(absolute)
	if err != nil {
		return "", err
	}

	allowedRoots := readRoots
	accessName := "read"
	if kind == accessWrite {
		allowedRoots = writeRoots
		accessName = "write"
	}

	for _, allowed := range allowedRoots {
		if isWithin(allowed, resolved) {
			return resolved, nil
		}
	}

	return "", fmt.Errorf(
		"%w: %s access to %q resolves to %q, which is outside the allowed roots (%s) under security.mode = %q; "+
			"use a path inside the workspace or configure security.read_paths/security.write_paths",
		ErrPathOutsideWorkspace,
		accessName,
		path,
		resolved,
		strings.Join(allowedRoots, ", "),
		mode,
	)
}

func (p *Policy) rootsSnapshot() (Mode, string, []string, []string) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	readRoots := append([]string(nil), p.readRoots...)
	writeRoots := append([]string(nil), p.writeRoots...)
	return p.mode, p.root, readRoots, writeRoots
}

// canonicalize resolves symlinks in every existing component of an absolute
// path and preserves components that do not exist yet.
//
// It deliberately rejects dangling symlinks instead of treating them as
// ordinary missing paths: writing through a dangling link would let the link
// target escape the workspace after the policy check.
func canonicalize(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%w: %q: %v", ErrPathResolution, path, err)
	}
	absolute = filepath.Clean(absolute)

	volume := filepath.VolumeName(absolute)
	rest := strings.TrimPrefix(absolute, volume)
	rest = strings.TrimPrefix(rest, string(filepath.Separator))

	var parts []string
	if rest != "" {
		parts = strings.Split(rest, string(filepath.Separator))
	}

	resolved := volume + string(filepath.Separator)

	for i, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			resolved = filepath.Dir(resolved)
			continue
		}

		next := filepath.Join(resolved, part)

		info, err := os.Lstat(next)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// The rest of the path does not exist. No further component
				// can be a symlink, so joining it lexically is safe.
				tail := append([]string{next}, parts[i+1:]...)
				return filepath.Join(tail...), nil
			}
			return "", fmt.Errorf("%w: %q: %v", ErrPathResolution, next, err)
		}

		if info.Mode()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(next)
			if err != nil {
				return "", fmt.Errorf(
					"%w: symlink %q cannot be resolved (dangling link or loop): %v",
					ErrPathResolution,
					next,
					err,
				)
			}

			resolved = target

			if i < len(parts)-1 {
				targetInfo, err := os.Stat(resolved)
				if err != nil {
					return "", fmt.Errorf("%w: %q: %v", ErrPathResolution, resolved, err)
				}
				if !targetInfo.IsDir() {
					return "", fmt.Errorf("%w: %q is not a directory", ErrPathResolution, resolved)
				}
			}
			continue
		}

		if i < len(parts)-1 && !info.IsDir() {
			return "", fmt.Errorf("%w: %q is not a directory", ErrPathResolution, next)
		}

		resolved = next
	}

	return filepath.Clean(resolved), nil
}

// isWithin reports whether target is root or a descendant of root. It is a
// lexical check performed after both paths have been canonicalised.
func isWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}

	if rel == "." {
		return true
	}

	return rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(rel)
}
