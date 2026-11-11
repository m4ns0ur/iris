package agentdir

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func setHome(t *testing.T, home string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
		return
	}
	t.Setenv("HOME", home)
}

func TestDir(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	dir, err := Dir()
	if err != nil {
		t.Fatalf("Dir() error = %v", err)
	}
	if want := filepath.Join(home, Name); dir != want {
		t.Fatalf("Dir() = %q, want %q", dir, want)
	}
}

func TestSubdirectories(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	logs, err := Logs()
	if err != nil {
		t.Fatalf("Logs() error = %v", err)
	}
	if want := filepath.Join(home, Name, "logs"); logs != want {
		t.Fatalf("Logs() = %q, want %q", logs, want)
	}

	sessions, err := Sessions()
	if err != nil {
		t.Fatalf("Sessions() error = %v", err)
	}
	if want := filepath.Join(home, Name, "sessions"); sessions != want {
		t.Fatalf("Sessions() = %q, want %q", sessions, want)
	}
}

func TestEnsureCreatesOwnerOnlyDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "logs")
	if err := Ensure(dir); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat created directory: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%q is not a directory", dir)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("directory permissions = %o, want 700", perm)
		}
	}
}

func TestEnsureTightensExistingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not preserve POSIX permission bits")
	}

	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create directory: %v", err)
	}
	if err := Ensure(dir); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat directory: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("directory permissions = %o, want 700", perm)
	}
}
