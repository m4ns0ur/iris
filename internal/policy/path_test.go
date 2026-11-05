package policy

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newTestPolicy(t *testing.T, options Options) *Policy {
	t.Helper()

	root := t.TempDir()
	pol, err := New(root, options)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return pol
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink(%q, %q): %v", target, link, err)
	}
}

func TestResolveRelativePathInsideWorkspace(t *testing.T) {
	pol := newTestPolicy(t, Options{})
	mustWrite(t, filepath.Join(pol.Root(), "dir", "file.txt"), "hello")

	got, err := pol.ResolveRead("dir/file.txt")
	if err != nil {
		t.Fatalf("ResolveRead() error = %v", err)
	}

	want := filepath.Join(pol.Root(), "dir", "file.txt")
	if got != want {
		t.Fatalf("ResolveRead() = %q, want %q", got, want)
	}
}

func TestResolveRejectsParentEscape(t *testing.T) {
	pol := newTestPolicy(t, Options{})

	_, err := pol.ResolveRead("../secret.txt")
	if !errors.Is(err, ErrPathOutsideWorkspace) {
		t.Fatalf("ResolveRead() error = %v, want ErrPathOutsideWorkspace", err)
	}
}

func TestResolveRejectsAbsolutePathOutside(t *testing.T) {
	pol := newTestPolicy(t, Options{})
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	mustWrite(t, outsideFile, "secret")

	_, err := pol.ResolveRead(outsideFile)
	if !errors.Is(err, ErrPathOutsideWorkspace) {
		t.Fatalf("ResolveRead() error = %v, want ErrPathOutsideWorkspace", err)
	}
}

func TestResolveRejectsSiblingWithSharedPrefix(t *testing.T) {
	pol := newTestPolicy(t, Options{})

	sibling := pol.Root() + "-other"
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", sibling, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sibling) })
	mustWrite(t, filepath.Join(sibling, "file.txt"), "outside")

	_, err := pol.ResolveRead(filepath.Join(sibling, "file.txt"))
	if !errors.Is(err, ErrPathOutsideWorkspace) {
		t.Fatalf("ResolveRead() error = %v, want ErrPathOutsideWorkspace", err)
	}
}

func TestResolveSymlinkEscapeIsRejected(t *testing.T) {
	pol := newTestPolicy(t, Options{})

	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "secret.txt"), "secret")

	link := filepath.Join(pol.Root(), "escape")
	mustSymlink(t, outside, link)

	_, err := pol.ResolveRead(filepath.Join("escape", "secret.txt"))
	if !errors.Is(err, ErrPathOutsideWorkspace) {
		t.Fatalf("ResolveRead() error = %v, want ErrPathOutsideWorkspace", err)
	}
}

func TestResolveSymlinkInsideIsAllowed(t *testing.T) {
	pol := newTestPolicy(t, Options{})
	mustWrite(t, filepath.Join(pol.Root(), "real", "file.txt"), "ok")

	link := filepath.Join(pol.Root(), "inside")
	mustSymlink(t, filepath.Join(pol.Root(), "real"), link)

	got, err := pol.ResolveRead(filepath.Join("inside", "file.txt"))
	if err != nil {
		t.Fatalf("ResolveRead() error = %v", err)
	}

	want := filepath.Join(pol.Root(), "real", "file.txt")
	if got != want {
		t.Fatalf("ResolveRead() = %q, want %q", got, want)
	}
}

func TestResolveWriteThroughSymlinkedParentEscapeIsRejected(t *testing.T) {
	pol := newTestPolicy(t, Options{})

	outside := t.TempDir()
	link := filepath.Join(pol.Root(), "escape")
	mustSymlink(t, outside, link)

	_, err := pol.ResolveWrite(filepath.Join("escape", "new.txt"))
	if !errors.Is(err, ErrPathOutsideWorkspace) {
		t.Fatalf("ResolveWrite() error = %v, want ErrPathOutsideWorkspace", err)
	}

	if _, statErr := os.Stat(filepath.Join(outside, "new.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("outside file was created despite policy rejection: %v", statErr)
	}
}

func TestResolveDanglingSymlinkIsRejected(t *testing.T) {
	pol := newTestPolicy(t, Options{})

	link := filepath.Join(pol.Root(), "dangling")
	mustSymlink(t, filepath.Join(pol.Root(), "missing-target"), link)

	_, err := pol.ResolveWrite("dangling")
	if !errors.Is(err, ErrPathResolution) {
		t.Fatalf("ResolveWrite() error = %v, want ErrPathResolution", err)
	}
}

func TestReadOnlyModeDeniesWrites(t *testing.T) {
	pol := newTestPolicy(t, Options{Mode: ModeReadOnly})
	mustWrite(t, filepath.Join(pol.Root(), "file.txt"), "data")

	if _, err := pol.ResolveRead("file.txt"); err != nil {
		t.Fatalf("ResolveRead() error = %v", err)
	}

	_, err := pol.ResolveWrite("file.txt")
	if !errors.Is(err, ErrReadOnlyMode) {
		t.Fatalf("ResolveWrite() error = %v, want ErrReadOnlyMode", err)
	}
}

func TestDangerFullAccessAllowsOutsidePaths(t *testing.T) {
	pol := newTestPolicy(t, Options{Mode: ModeDangerFullAccess})

	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "file.txt")
	mustWrite(t, outsideFile, "data")

	if _, err := pol.ResolveRead(outsideFile); err != nil {
		t.Fatalf("ResolveRead() error = %v", err)
	}

	if _, err := pol.ResolveWrite(filepath.Join(outside, "new.txt")); err != nil {
		t.Fatalf("ResolveWrite() error = %v", err)
	}
}

func TestExtraWriteRootIsAllowed(t *testing.T) {
	outside := t.TempDir()
	pol := newTestPolicy(t, Options{WritePaths: []string{outside}})

	canonicalOutside, err := filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	target := filepath.Join(canonicalOutside, "new.txt")
	got, err := pol.ResolveWrite(filepath.Join(outside, "new.txt"))
	if err != nil {
		t.Fatalf("ResolveWrite() error = %v", err)
	}
	if got != target {
		t.Fatalf("ResolveWrite() = %q, want %q", got, target)
	}

	if _, err := pol.ResolveRead(target); err != nil {
		t.Fatalf("ResolveRead() for write root error = %v", err)
	}
}

func TestResolveNonexistentNestedPath(t *testing.T) {
	pol := newTestPolicy(t, Options{})

	got, err := pol.ResolveWrite("a/b/c.txt")
	if err != nil {
		t.Fatalf("ResolveWrite() error = %v", err)
	}

	want := filepath.Join(pol.Root(), "a", "b", "c.txt")
	if got != want {
		t.Fatalf("ResolveWrite() = %q, want %q", got, want)
	}
}

func TestResolveWorkspaceRootSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}

	parent := t.TempDir()
	realRoot := filepath.Join(parent, "real")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	linkRoot := filepath.Join(parent, "link")
	mustSymlink(t, realRoot, linkRoot)

	pol, err := New(linkRoot, Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	canonicalReal, err := filepath.EvalSymlinks(realRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if pol.Root() != canonicalReal {
		t.Fatalf("Root() = %q, want %q", pol.Root(), canonicalReal)
	}

	if _, err := pol.ResolveWrite("file.txt"); err != nil {
		t.Fatalf("ResolveWrite() error = %v", err)
	}
}

func TestResolveDotDotInsideWorkspace(t *testing.T) {
	pol := newTestPolicy(t, Options{})
	mustWrite(t, filepath.Join(pol.Root(), "sub", "keep"), "")
	mustWrite(t, filepath.Join(pol.Root(), "file.txt"), "data")

	got, err := pol.ResolveRead("sub/../file.txt")
	if err != nil {
		t.Fatalf("ResolveRead() error = %v", err)
	}

	want := filepath.Join(pol.Root(), "file.txt")
	if got != want {
		t.Fatalf("ResolveRead() = %q, want %q", got, want)
	}
}

func TestResolveRejectsNulByte(t *testing.T) {
	pol := newTestPolicy(t, Options{})

	_, err := pol.ResolveRead("file\x00.txt")
	if !errors.Is(err, ErrPathResolution) {
		t.Fatalf("ResolveRead() error = %v, want ErrPathResolution", err)
	}
}

func TestResolveRejectsFileAsIntermediateDirectory(t *testing.T) {
	pol := newTestPolicy(t, Options{})
	mustWrite(t, filepath.Join(pol.Root(), "file"), "data")

	_, err := pol.ResolveWrite("file/child.txt")
	if !errors.Is(err, ErrPathResolution) {
		t.Fatalf("ResolveWrite() error = %v, want ErrPathResolution", err)
	}
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("error = %v, want not-a-directory message", err)
	}
}

func TestAddReadRootAllowsTempFiles(t *testing.T) {
	pol := newTestPolicy(t, Options{})

	extra := t.TempDir()
	mustWrite(t, filepath.Join(extra, "output.log"), "captured")

	if _, err := pol.ResolveRead(filepath.Join(extra, "output.log")); !errors.Is(err, ErrPathOutsideWorkspace) {
		t.Fatalf("ResolveRead() before AddReadRoot error = %v, want ErrPathOutsideWorkspace", err)
	}

	if err := pol.AddReadRoot(extra); err != nil {
		t.Fatalf("AddReadRoot() error = %v", err)
	}

	if _, err := pol.ResolveRead(filepath.Join(extra, "output.log")); err != nil {
		t.Fatalf("ResolveRead() after AddReadRoot error = %v", err)
	}
}
