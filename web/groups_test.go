package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func writeGroupFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "group.txt")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func checkGroups(t *testing.T, path, email string, want []string) {
	t.Helper()
	data, err := readGroupFile(path)
	if err != nil {
		t.Fatalf("readGroupFile: %v", err)
	}
	if got := data.groupsOf(email); !reflect.DeepEqual(got, want) {
		t.Errorf("groupsOf(%q) = %#v, want %#v", email, got, want)
	}
}

func TestGroupsOfFindsEveryGroup(t *testing.T) {
	path := writeGroupFile(t, "Administrators:admin@example.com\nTeachers:admin@example.com,teacher@example.com\n")
	checkGroups(t, path, "admin@example.com", []string{"Administrators", "Teachers"})
	checkGroups(t, path, "teacher@example.com", []string{"Teachers"})
}

func TestGroupsOfIgnoresEmailCase(t *testing.T) {
	path := writeGroupFile(t, "Teachers:Teacher@Example.com\n")
	checkGroups(t, path, "teacher@example.COM", []string{"Teachers"})
}

func TestGroupsOfReadsTrinoFormat(t *testing.T) {
	path := writeGroupFile(t, "# Administrators:b@x.com\n\nAnalysts: a@x.com , b@x.com \nnot a group line\n")
	checkGroups(t, path, "b@x.com", []string{"Analysts"})
}

func TestGroupsOfUnknownEmail(t *testing.T) {
	checkGroups(t, writeGroupFile(t, "Teachers:a@x.com\n"), "nobody@x.com", nil)
	checkGroups(t, writeGroupFile(t, ""), "nobody@x.com", nil)
}

func TestGroupsOfEmptyEmail(t *testing.T) {
	path := writeGroupFile(t, "Teachers:\nAnalysts:a@x.com,,\n")
	checkGroups(t, path, "", nil)
}

func TestReadGroupFileMissing(t *testing.T) {
	if _, err := readGroupFile(filepath.Join(t.TempDir(), "missing.txt")); err == nil {
		t.Error("readGroupFile on a missing file returned no error")
	}
}

func TestGroupNames(t *testing.T) {
	data, err := readGroupFile(writeGroupFile(t, "Teachers:a@x.com\nAnalysts:\nTeachers:b@x.com\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := data.names(), []string{"Analysts", "Teachers"}; !reflect.DeepEqual(got, want) {
		t.Errorf("names() = %q, want %q", got, want)
	}
}

// The group file's time is when the sync last synced it; the sync's placeholder, at the epoch, never has.
func TestReadGroupFileSyncedAt(t *testing.T) {
	path := writeGroupFile(t, "Teachers:a@x.com\n")
	synced := time.Date(2026, 11, 8, 10, 0, 30, 0, time.UTC)
	os.Chtimes(path, synced, synced)
	data, err := readGroupFile(path)
	if err != nil || data.syncedAt == nil || !data.syncedAt.Equal(synced) {
		t.Errorf("syncedAt %v, %v; want %v", data.syncedAt, err, synced)
	}

	os.Chtimes(path, time.Unix(0, 0), time.Unix(0, 0))
	if data, err := readGroupFile(path); err != nil || data.syncedAt != nil {
		t.Errorf("placeholder: syncedAt %v, %v; want nil", data.syncedAt, err)
	}
}
