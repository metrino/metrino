package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "file.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadSchemaV2(t *testing.T) {
	s, _, err := readSchema(writeFile(t, `{
		"connections": [
			{"id": 1, "name": "trino", "catalog": "example_jdbc", "address": "trino-reverse-proxy:9090",
			 "viaProxy": true, "lastScan": "2026-11-08T10:00:00Z"},
			{"id": 2, "name": "direct", "catalog": "openapi", "address": "trino:8080", "viaProxy": false}],
		"catalogs": {"example_jdbc": {"public": {"students": [{"name": "student_id", "type": "bigint"}]}}},
		"metabase": {"example_jdbc": {"public": {"students": {"database": 1, "table": 4, "description": "Enrolled.",
			"fields": {"student_id": {"id": 16, "description": "Their ID."}}}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	scan := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	wantConnections := []metabaseConnection{
		{ID: 1, Name: "trino", Catalog: "example_jdbc", Address: "trino-reverse-proxy:9090", ViaProxy: true, LastScan: &scan},
		{ID: 2, Name: "direct", Catalog: "openapi", Address: "trino:8080"},
	}
	if !reflect.DeepEqual(s.Connections, wantConnections) {
		t.Errorf("connections %+v, want %+v", s.Connections, wantConnections)
	}
	wantCatalogs := map[string]map[string]map[string][]schemaColumn{
		"example_jdbc": {"public": {"students": {{Name: "student_id", Type: "bigint"}}}},
	}
	if !reflect.DeepEqual(s.Catalogs, wantCatalogs) {
		t.Errorf("catalogs %+v, want %+v", s.Catalogs, wantCatalogs)
	}
	wantMetabase := map[string]map[string]map[string]metabaseTableInfo{
		"example_jdbc": {"public": {"students": {Database: 1, Table: 4, Description: "Enrolled.",
			Fields: map[string]metabaseFieldInfo{"student_id": {ID: 16, Description: "Their ID."}}}}},
	}
	if !reflect.DeepEqual(s.Metabase, wantMetabase) {
		t.Errorf("metabase %+v, want %+v", s.Metabase, wantMetabase)
	}
}

func TestReadSchemaMissing(t *testing.T) {
	for _, path := range []string{"", filepath.Join(t.TempDir(), "missing.json")} {
		s, _, err := readSchema(path)
		if err != nil {
			t.Errorf("readSchema(%q): %v", path, err)
		}
		if len(s.Connections) != 0 || len(s.Catalogs) != 0 {
			t.Errorf("readSchema(%q) = %+v, want empty", path, s)
		}
	}
}

// The sync used to write the catalogs alone. Read as the current format that would look like an empty schema, so
// the error says what to update instead.
func TestReadSchemaOldFormat(t *testing.T) {
	path := writeFile(t, `{"example_jdbc": {"public": {"students": [{"name": "student_id", "type": "bigint"}]}}}`)
	_, _, err := readSchema(path)
	if err == nil || !strings.Contains(err.Error(), "older format") {
		t.Errorf("error %v, want one mentioning an older format", err)
	}
	if err != nil && !strings.Contains(err.Error(), path) {
		t.Errorf("error %v doesn't name the file", err)
	}
}

func TestReadStatus(t *testing.T) {
	s, err := readStatus(writeFile(t, `{"intervalSeconds": 30, "files": {
		"group": {},
		"schema": {"lastError": "GET /api/database: invalid status code: 401", "lastErrorAt": "2026-11-08T10:00:30Z"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if s == nil || s.IntervalSeconds != 30 {
		t.Fatalf("status %+v, want interval 30", s)
	}
	group, schema := s.Files["group"], s.Files["schema"]
	if group == nil || group.LastError != "" || group.LastErrorAt != nil {
		t.Errorf("group status %+v", group)
	}
	if schema == nil || schema.LastError != "GET /api/database: invalid status code: 401" ||
		schema.LastErrorAt == nil || !schema.LastErrorAt.Equal(time.Date(2026, 11, 8, 10, 0, 30, 0, time.UTC)) {
		t.Errorf("schema status %+v", schema)
	}
}

// The schema file's time comes with it, even from a file in an older format.
func TestReadSchemaSyncedAt(t *testing.T) {
	synced := time.Date(2026, 11, 8, 10, 0, 30, 0, time.UTC)
	for _, contents := range []string{testSchema, `{"example_jdbc": {}}`} {
		path := writeFile(t, contents)
		os.Chtimes(path, synced, synced)
		if _, got, _ := readSchema(path); got == nil || !got.Equal(synced) {
			t.Errorf("%.20s...: syncedAt %v, want %v", contents, got, synced)
		}
	}
	if _, got, err := readSchema(""); got != nil || err != nil {
		t.Errorf("no file: syncedAt %v, %v; want nil, nil", got, err)
	}
}

func TestSyncTime(t *testing.T) {
	at := time.Date(2026, 11, 8, 10, 0, 30, 0, time.FixedZone("EST", -5*3600))
	if got := syncTime(at); got == nil || !got.Equal(at) || got.Location() != time.UTC {
		t.Errorf("syncTime(%v) = %v, want it in UTC", at, got)
	}
	for _, never := range []time.Time{{}, time.Unix(0, 0)} {
		if got := syncTime(never); got != nil {
			t.Errorf("syncTime(%v) = %v, want nil", never, got)
		}
	}
}

func TestReadStatusMissing(t *testing.T) {
	for _, path := range []string{"", filepath.Join(t.TempDir(), "missing.json")} {
		s, err := readStatus(path)
		if s != nil || err != nil {
			t.Errorf("readStatus(%q) = %+v, %v, want nil, nil", path, s, err)
		}
	}
}

func TestTrinoCatalogsEnv(t *testing.T) {
	t.Setenv("TRINO_CATALOGS", " openapi,example_jdbc ")
	if got, want := trinoCatalogsFromEnv(), []string{"example_jdbc", "openapi"}; !reflect.DeepEqual(got, want) {
		t.Errorf("trinoCatalogsFromEnv() = %#v, want %#v", got, want)
	}
	t.Setenv("TRINO_CATALOGS", "")
	if got := trinoCatalogsFromEnv(); got != nil {
		t.Errorf("trinoCatalogsFromEnv() = %#v unset, want nil", got)
	}
}
