package agent

import "testing"

func TestParseGitStatus(t *testing.T) {
	changes := parseGitStatus([]byte(" M a.txt\x00?? new.txt\x00R  c.txt\x00old.txt\x00"))
	if len(changes) != 3 {
		t.Fatalf("changes = %+v, want 3", changes)
	}
	if changes[0].Status != "M" || changes[0].Path != "a.txt" {
		t.Fatalf("modified entry = %+v", changes[0])
	}
	if changes[1].Status != "A" || changes[1].Path != "new.txt" {
		t.Fatalf("untracked entry = %+v", changes[1])
	}
	if changes[2].Status != "R" || changes[2].Path != "old.txt -> c.txt" {
		t.Fatalf("rename entry = %+v", changes[2])
	}
}
