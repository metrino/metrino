package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/metrino/metrino/trino/identity/metabase"
)

// Saved from Metabase's /api/database and /api/database/1/metadata?include_hidden=true, trimmed to the fields
// the sync reads. Database 2 isn't Trino.
const databasesJson = `{"total": 2, "data": [
	{"id": 1, "name": "trino", "engine": "starburst",
		"details": {"host": "trino-reverse-proxy", "port": 9090, "catalog": "example_jdbc", "prepared-optimized": true}},
	{"id": 2, "name": "uploads", "engine": "postgres", "details": {"dbname": "uploads"}}]}`

// Shaped like Metabase's /api/task/?task=sync: database 1 last scanned at 10:00.
const tasksJson = `{"total": 1, "limit": 1000, "offset": 0, "data": [
	{"id": 10, "task": "sync", "db_id": 1, "status": "success", "ended_at": "2026-11-08T10:00:00Z"}]}`

const metadataJson = `{"id": 1, "tables": [
	{"id": 4, "schema": "public", "name": "students", "description": "One row per enrolled student.", "active": true, "fields": [
		{"id": 18, "name": "email", "description": "School email.", "database_type": "varchar", "active": true,
			"database_position": 2},
		{"id": 16, "name": "student_id", "database_type": "bigint", "active": true, "database_position": 0}]},
	{"id": 2, "schema": "public", "name": "courses", "active": true, "visibility_type": "hidden", "fields": [
		{"id": 7, "name": "course_id", "database_type": "uuid", "active": true, "database_position": 0}]}]}`

const wantSchemaFile = `{
  "connections": [
    {
      "id": 1,
      "name": "trino",
      "catalog": "example_jdbc",
      "address": "trino-reverse-proxy:9090",
      "viaProxy": true,
      "optimizedPrepare": true,
      "lastScan": "2026-11-08T10:00:00Z"
    }
  ],
  "catalogs": {
    "example_jdbc": {
      "public": {
        "courses": [
          {
            "name": "course_id",
            "type": "uuid"
          }
        ],
        "students": [
          {
            "name": "student_id",
            "type": "bigint"
          },
          {
            "name": "email",
            "type": "varchar"
          }
        ]
      }
    }
  },
  "metabase": {
    "example_jdbc": {
      "public": {
        "courses": {
          "database": 1,
          "table": 2,
          "hidden": true,
          "fields": {
            "course_id": {
              "id": 7
            }
          }
        },
        "students": {
          "database": 1,
          "table": 4,
          "description": "One row per enrolled student.",
          "fields": {
            "email": {
              "id": 18,
              "description": "School email."
            },
            "student_id": {
              "id": 16
            }
          }
        }
      }
    }
  }
}
`

func TestToSchema(t *testing.T) {
	parent := 1
	databases := []metabase.Database{
		{ID: 5, Name: "second", Engine: "starburst", Catalog: "example_jdbc", Address: "localhost:9090"}, // the same catalog again
		{ID: 1, Name: "first", Engine: "starburst", Catalog: "example_jdbc", Address: "127.0.0.1:9090", OptimizedPrepare: true},
		{ID: 2, Name: "uploads", Engine: "postgres", Address: "db:5432"}, // not Trino
		{ID: 3, Name: "no catalog", Engine: "starburst", Address: "127.0.0.1:9090"},
		{ID: 4, Name: "direct", Engine: "starburst", Catalog: "openapi", Address: "trino:8080"}, // bypasses the proxy
	}
	scanned := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	tables := map[int][]metabase.Table{
		1: {{ID: 40, Schema: "public", Name: "students", Description: "Enrolled.", Active: true, Fields: []metabase.Field{
			{ID: 402, Name: "email", Description: "School email.", DatabaseType: "varchar", Active: true, DatabasePosition: 2},
			{ID: 400, Name: "student_id", DatabaseType: "bigint", Active: true, DatabasePosition: 0},
			{Name: "dropped", DatabaseType: "varchar", Active: false, DatabasePosition: 1},
			{Name: "nested", DatabaseType: "varchar", Active: true, ParentID: &parent},
			{Name: "unfolded", DatabaseType: "varchar", Active: true, NfcPath: []string{"profile", "unfolded"}},
		}}, {Schema: "public", Name: "gone", Active: false, Fields: []metabase.Field{
			{Name: "id", DatabaseType: "bigint", Active: true},
		}}},
		2: {{Schema: "public", Name: "uploads", Active: true, Fields: []metabase.Field{{Name: "id", Active: true}}}},
		3: {{Schema: "public", Name: "anything", Active: true, Fields: []metabase.Field{{Name: "id", Active: true}}}},
		4: {{Schema: "default", Name: "people", Active: true, Fields: []metabase.Field{
			{Name: "id", DatabaseType: "integer", Active: true},
		}}},
		5: {{ID: 50, Schema: "public", Name: "students", Active: true, Fields: []metabase.Field{
			{ID: 500, Name: "other", DatabaseType: "varchar", Active: true},
		}}, {ID: 51, Schema: "public", Name: "staff", Active: true, VisibilityType: "technical", Fields: []metabase.Field{
			{ID: 510, Name: "staff_id", DatabaseType: "bigint", Active: true},
		}}},
	}

	got := toSchema(databases, tables, map[int]time.Time{1: scanned}, []string{"127.0.0.1:9090", "localhost:9090"})

	want := schemaFile{
		Connections: []connection{
			{ID: 1, Name: "first", Catalog: "example_jdbc", Address: "127.0.0.1:9090", ViaProxy: true, OptimizedPrepare: true,
				LastScan: &scanned},
			{ID: 3, Name: "no catalog", Address: "127.0.0.1:9090", ViaProxy: true},
			{ID: 4, Name: "direct", Catalog: "openapi", Address: "trino:8080"},
			{ID: 5, Name: "second", Catalog: "example_jdbc", Address: "localhost:9090", ViaProxy: true},
		},
		// The bypassing connection's tables aren't listed. The lower ID's copy of a shared table wins.
		Catalogs: map[string]map[string]map[string][]column{
			"example_jdbc": {"public": {
				"students": {{Name: "student_id", Type: "bigint"}, {Name: "email", Type: "varchar"}},
				"staff":    {{Name: "staff_id", Type: "bigint"}},
			}},
		},
		// Each table's IDs and descriptions come from the connection whose copy of it is listed, and only its
		// listed columns have an entry.
		Metabase: map[string]map[string]map[string]tableInfo{
			"example_jdbc": {"public": {
				"students": {Database: 1, Table: 40, Description: "Enrolled.", Fields: map[string]fieldInfo{
					"student_id": {ID: 400}, "email": {ID: 402, Description: "School email."}}},
				"staff": {Database: 5, Table: 51, Hidden: true, Fields: map[string]fieldInfo{"staff_id": {ID: 510}}},
			}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := got.summary(); got != "4 connections, 1 catalogs, 2 tables, 3 columns" {
		t.Errorf("summary is %q", got)
	}
}

func TestSyncSchemaOnce(t *testing.T) {
	schemaFile := filepath.Join(t.TempDir(), "schema.json")
	if err := syncSchemaOnce(fakeMetabase(t, "", 0), schemaFile, []string{"trino-reverse-proxy:9090"}); err != nil {
		t.Fatal(err)
	}
	checkFile(t, schemaFile, wantSchemaFile)
}

// A Metabase that can't list its tasks still gets its schema file, only without scan times.
func TestSchemaLastScansFailureIsNotFatal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/database":
			rw.Write([]byte(databasesJson))
		case "/api/database/1/metadata":
			rw.Write([]byte(metadataJson))
		default:
			http.Error(rw, "failed", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)

	schemaFile := filepath.Join(t.TempDir(), "schema.json")
	client := metabase.NewClient(server.URL, "secret", "")
	if err := syncSchemaOnce(client, schemaFile, []string{"trino-reverse-proxy:9090"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(schemaFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"viaProxy": true`) || strings.Contains(string(got), "lastScan") {
		t.Errorf("got %s", got)
	}
}

func TestFailedSchemaSyncKeepsTheFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/database":
			rw.Write([]byte(databasesJson))
		default:
			http.Error(rw, "failed", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)

	schemaFile := filepath.Join(t.TempDir(), "schema.json")
	if err := os.WriteFile(schemaFile, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syncSchemaOnce(metabase.NewClient(server.URL, "secret", ""), schemaFile, []string{"trino-reverse-proxy:9090"}); err == nil {
		t.Error("got no error")
	}
	checkFile(t, schemaFile, "{}\n")
}
