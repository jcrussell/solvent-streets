package db

import "testing"

func TestParseSnapshotID(t *testing.T) {
	if id, err := ParseSnapshotID("42"); err != nil || id != 42 {
		t.Errorf(`ParseSnapshotID("42") = %d, %v; want 42`, id, err)
	}
	for _, bad := range []string{"", "abc", "4.2", "0", "-3", "99999999999999999999"} {
		if id, err := ParseSnapshotID(bad); err == nil {
			t.Errorf("ParseSnapshotID(%q) = %d, want an error", bad, id)
		}
	}
}
