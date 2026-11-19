package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/metrino/metrino/trino/identity/metabase"
)

// fakeMetabase serves groupsJson, usersJson unless users is set, databasesJson, tasksJson and metadataJson. A non-zero status fails /api/user.
func fakeMetabase(t *testing.T, users string, status int) *metabase.Client {
	if users == "" {
		users = usersJson
	}
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/permissions/group":
			rw.Write([]byte(groupsJson))
		case "/api/database":
			rw.Write([]byte(databasesJson))
		case "/api/task/":
			rw.Write([]byte(tasksJson))
		case "/api/database/1/metadata":
			rw.Write([]byte(metadataJson))
		case "/api/user":
			if status != 0 {
				http.Error(rw, "failed", status)
				return
			}
			rw.Write([]byte(users))
		default:
			http.NotFound(rw, req)
		}
	}))
	t.Cleanup(server.Close)
	return metabase.NewClient(server.URL, "secret", "")
}

func TestSplitAddresses(t *testing.T) {
	for list, want := range map[string][]string{
		"":                                   nil,
		" , ":                                nil,
		"127.0.0.1:9090":                     {"127.0.0.1:9090"},
		" 127.0.0.1:9090, localhost:9090 ,,": {"127.0.0.1:9090", "localhost:9090"},
	} {
		if got := splitAddresses(list); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %q, want %q", list, got, want)
		}
	}
}

func TestCreateEmptyFile(t *testing.T) {
	groupFile := filepath.Join(t.TempDir(), "group.txt")
	if created, err := createEmptyFile(groupFile); !created || err != nil {
		t.Fatalf("got %v, %v", created, err)
	}
	checkFile(t, groupFile, "")
	checkModTime(t, groupFile, neverSynced)

	os.WriteFile(groupFile, []byte("Teachers:a@example.com\n"), 0o644)
	if created, err := createEmptyFile(groupFile); created || err != nil {
		t.Errorf("existing file: got %v, %v", created, err)
	}
	checkFile(t, groupFile, "Teachers:a@example.com\n")
}

func TestWriteIfChanged(t *testing.T) {
	dir := t.TempDir()
	groupFile := filepath.Join(dir, "group.txt")
	os.WriteFile(groupFile, nil, 0o600)

	if written, err := writeIfChanged(groupFile, "Teachers:a@example.com\n"); !written || err != nil {
		t.Fatalf("got %v, %v", written, err)
	}
	checkFile(t, groupFile, "Teachers:a@example.com\n")

	before, _ := os.Stat(groupFile)
	if written, err := writeIfChanged(groupFile, "Teachers:a@example.com\n"); written || err != nil {
		t.Errorf("unchanged: got %v, %v", written, err)
	}
	if after, _ := os.Stat(groupFile); !os.SameFile(before, after) {
		t.Error("unchanged: the file was replaced")
	}

	// No temporary files left behind.
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("the directory has %d entries", len(entries))
	}
}

func TestStatusRecord(t *testing.T) {
	first := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	status := &syncStatus{IntervalSeconds: 30}

	status.record("schema", nil, first)
	got := status.Files["schema"]
	if got == nil || got.LastError != "" || got.LastErrorAt != nil {
		t.Fatalf("after a success: %+v", got)
	}

	second := first.Add(30 * time.Second)
	status.record("schema", errors.New("GET /api/database: invalid status code: 401"), second)
	if got.LastError != "GET /api/database: invalid status code: 401" || got.LastErrorAt == nil || !got.LastErrorAt.Equal(second) {
		t.Errorf("after a failure: %+v", got)
	}

	status.record("schema", nil, second.Add(30*time.Second))
	if got.LastError != "" || got.LastErrorAt != nil {
		t.Errorf("a success should clear the error, got %+v", got)
	}

	if len(status.Files) != 1 {
		t.Errorf("only the recorded file should have a status, got %v", status.Files)
	}
}

func TestRunOnceWritesStatus(t *testing.T) {
	// The schema file's sync fails, since Metabase answers 500 to its first request; the group file's doesn't.
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/permissions/group":
			rw.Write([]byte(groupsJson))
		case "/api/user":
			rw.Write([]byte(usersJson))
		default:
			http.Error(rw, "failed", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)
	client := metabase.NewClient(server.URL, "secret", "")

	dir := t.TempDir()
	groupFile := filepath.Join(dir, "group.txt")
	schemaFile := filepath.Join(dir, "schema.json")
	statusFile := filepath.Join(dir, "status.json")
	// An old copy of the schema file, which the failing sync must leave alone.
	old := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	os.WriteFile(schemaFile, []byte("{}\n"), 0o644)
	os.Chtimes(schemaFile, old, old)
	files := []syncedFile{
		{"group", groupFile, func(client *metabase.Client) error { return syncGroupsOnce(client, groupFile) }},
		{"schema", schemaFile, func(client *metabase.Client) error {
			return syncSchemaOnce(client, schemaFile, []string{"127.0.0.1:9090"})
		}},
	}
	status := &syncStatus{path: statusFile, IntervalSeconds: 30}

	now := time.Date(2026, 11, 8, 10, 0, 30, 0, time.UTC)
	runOnce(client, files, status, now)

	got, err := os.ReadFile(statusFile)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		IntervalSeconds int                          `json:"intervalSeconds"`
		Files           map[string]map[string]string `json:"files"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("%v in %s", err, got)
	}
	if parsed.IntervalSeconds != 30 {
		t.Errorf("intervalSeconds is %d", parsed.IntervalSeconds)
	}
	if group, ok := parsed.Files["group"]; !ok || len(group) != 0 {
		t.Errorf("group: %v, want an entry without an error", group)
	}
	schema := parsed.Files["schema"]
	if len(schema) != 2 || schema["lastError"] == "" || schema["lastErrorAt"] != "2026-11-08T10:00:30Z" {
		t.Errorf("schema: %v, want lastError and lastErrorAt only", schema)
	}
	checkFile(t, statusFile, string(got))
	// The file that synced is marked with the run's time; the one that failed keeps its own.
	checkModTime(t, groupFile, now)
	checkModTime(t, schemaFile, old)

	// The next run marks the group file again, though its contents are the same.
	later := now.Add(30 * time.Second)
	runOnce(client, files, status, later)
	checkModTime(t, groupFile, later)

	// A nil status writes nothing, as in the coordinator's sync.
	runOnce(client, files, nil, now)
}

// checkModTime checks that path was last modified at want.
func checkModTime(t *testing.T, path string, want time.Time) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(want) {
		t.Errorf("%s was modified at %v, want %v", path, info.ModTime().UTC(), want)
	}
}

// checkFile checks that path has contents and mode 0644.
func checkFile(t *testing.T, path, contents string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != contents {
		t.Errorf("%s has %q, want %q", path, got, contents)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o644 {
		t.Errorf("%s has mode %v, want 0644", path, info.Mode().Perm())
	}
}
