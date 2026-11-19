package metabase

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// fakeMetabase serves a few users and groups to requests with the API key "secret".
func fakeMetabase(t *testing.T) *httptest.Server {
	return fakeMetabaseWithUsers(t, `{"total": 2, "data": [
		{"id": 1, "email": "admin@example.com", "is_active": true, "group_ids": [1, 2]},
		{"id": 7, "email": "alice@example.com", "is_active": true, "group_ids": [1, 5]}]}`)
}

// fakeMetabaseWithUsers is fakeMetabase with userList as the body of /api/user.
func fakeMetabaseWithUsers(t *testing.T, userList string) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if req.Header.Get("X-API-Key") != "secret" {
			http.Error(rw, "Unauthenticated", http.StatusUnauthorized)
			return
		}
		switch req.URL.Path {
		case "/api/user":
			rw.Write([]byte(userList))
		case "/api/permissions/group":
			rw.Write([]byte(`[
				{"id": 1, "name": "All Users", "magic_group_type": "all-internal-users", "member_count": 2},
				{"id": 2, "name": "Administrators", "magic_group_type": "admin"},
				{"id": 5, "name": "Analysts", "magic_group_type": null}]`))
		case "/api/user/7":
			rw.Write([]byte(`{"id": 7, "email": "alice@example.com", "is_active": true, "first_name": "Alice"}`))
		case "/api/user/8":
			rw.Write([]byte(`{"id": 8, "email": "bob@example.com", "is_active": false}`))
		case "/api/user/9":
			rw.Write([]byte(`{"id": 9, "email": "carol@example.com"}`))
		case "/api/database":
			rw.Write([]byte(databasesJson))
		case "/api/task/":
			// Metabase lists its newest tasks first. A task with a null ended_at is still running.
			if req.URL.Query().Get("task") != "sync" {
				http.Error(rw, "the fake only serves ?task=sync", http.StatusBadRequest)
				return
			}
			rw.Write([]byte(tasksJson))
		case "/api/database/1/metadata":
			// Metabase leaves hidden tables out unless asked, and setup hides every Trino table.
			if req.URL.Query().Get("include_hidden") != "true" {
				rw.Write([]byte(`{"id": 1, "tables": []}`))
				return
			}
			rw.Write([]byte(metadataJson))
		default:
			http.NotFound(rw, req)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// Saved from compose's Metabase (/api/database and /api/database/1/metadata?include_hidden=true), trimmed to a
// few fields and columns.
const databasesJson = `{"total": 4, "data": [
	{"id": 1, "name": "trino", "engine": "starburst", "details": {"host": "trino-reverse-proxy", "port": 9090,
		"catalog": "example_jdbc", "schema": null, "user": "trino", "ssl": false, "prepared-optimized": true}},
	{"id": 3, "name": "uploads", "engine": "postgres", "details": {"host": "db", "dbname": "uploads"}},
	{"id": 4, "name": "direct", "engine": "starburst", "details": {"host": "localhost", "port": "9090",
		"catalog": "example_http", "prepared-optimized": false}},
	{"id": 5, "name": "options", "engine": "starburst", "details": {"host": "localhost", "port": 9090,
		"catalog": "openapi", "additional-options": "source=metabase&explicitPrepare=FALSE"}}]}`

// Shaped like compose's /api/task/?task=sync, with the logs left out and tasks added for other databases.
// The task filter is applied by Metabase, so the fake serves only sync tasks too, plus one sync-fields task
// that would be wrong to count if the filter were ignored.
const tasksJson = `{"total": 6, "limit": 1000, "offset": 0, "data": [
	{"id": 60, "task": "sync", "db_id": 1, "status": "failed", "ended_at": "2026-11-08T05:00:00.5Z"},
	{"id": 50, "task": "sync-fields", "db_id": 3, "status": "success", "ended_at": "2026-11-08T04:30:00Z"},
	{"id": 40, "task": "sync", "db_id": 3, "status": "success", "ended_at": "2026-11-08T04:00:00Z"},
	{"id": 30, "task": "sync", "db_id": 1, "status": "started", "ended_at": null},
	{"id": 20, "task": "sync", "db_id": 1, "status": "success", "ended_at": "2026-11-08T02:49:29.433201Z"},
	{"id": 10, "task": "sync", "db_id": 1, "status": "success", "ended_at": "2026-11-08T01:40:00.9Z"}]}`

const metadataJson = `{"id": 1, "name": "trino", "tables": [
	{"id": 10, "schema": "public", "name": "students", "description": "One row per enrolled student.", "active": true,
		"visibility_type": "hidden", "fields": [
		{"id": 100, "name": "student_id", "database_type": "bigint", "active": true, "parent_id": null,
			"nfc_path": null, "database_position": 0},
		{"id": 102, "name": "email", "description": "School email.", "database_type": "varchar", "active": true, "parent_id": null,
			"nfc_path": null, "database_position": 3}]},
	{"id": 11, "schema": "public", "name": "old", "description": null, "active": false, "visibility_type": null, "fields": []}]}`

func TestDatabases(t *testing.T) {
	databases, err := NewClient(fakeMetabase(t).URL, "secret", "").Databases()
	if err != nil {
		t.Fatal(err)
	}
	want := []Database{
		{ID: 1, Name: "trino", Engine: "starburst", Catalog: "example_jdbc", Address: "trino-reverse-proxy:9090",
			OptimizedPrepare: true},
		{ID: 3, Name: "uploads", Engine: "postgres"},
		{ID: 4, Name: "direct", Engine: "starburst", Catalog: "example_http", Address: "localhost:9090"},
		{ID: 5, Name: "options", Engine: "starburst", Catalog: "openapi", Address: "localhost:9090",
			OptimizedPrepare: true},
	}
	if !reflect.DeepEqual(databases, want) {
		t.Errorf("got %+v, want %+v", databases, want)
	}
}

func TestExplicitPrepareOff(t *testing.T) {
	tests := map[string]bool{
		"":                                      false,
		"explicitPrepare=false":                 true,
		"&explicitPrepare=false":                true,
		"source=metabase&explicitPrepare=False": true,
		"explicitPrepare=true":                  false,
		"explicitprepare=false":                 false, // Trino's property names are case-sensitive
		"notexplicitPrepare=false":              false,
	}
	for options, want := range tests {
		if got := explicitPrepareOff(options); got != want {
			t.Errorf("%q: got %v, want %v", options, got, want)
		}
	}
}

func TestLastScans(t *testing.T) {
	scans, err := NewClient(fakeMetabase(t).URL, "secret", "").LastScans()
	if err != nil {
		t.Fatal(err)
	}
	// Database 1's newest sync failed and the one after it is still running, so its newest success counts.
	want := map[int]time.Time{
		1: time.Date(2026, 11, 8, 2, 49, 29, 433201000, time.UTC),
		3: time.Date(2026, 11, 8, 4, 0, 0, 0, time.UTC),
	}
	if !reflect.DeepEqual(scans, want) {
		t.Errorf("got %v, want %v", scans, want)
	}
}

func TestTables(t *testing.T) {
	tables, err := NewClient(fakeMetabase(t).URL, "secret", "").Tables(1)
	if err != nil {
		t.Fatal(err)
	}
	want := []Table{
		{ID: 10, Schema: "public", Name: "students", Description: "One row per enrolled student.", Active: true,
			VisibilityType: "hidden", Fields: []Field{
				{ID: 100, Name: "student_id", DatabaseType: "bigint", Active: true},
				{ID: 102, Name: "email", Description: "School email.", DatabaseType: "varchar", Active: true, DatabasePosition: 3},
			}},
		{ID: 11, Schema: "public", Name: "old", Active: false, Fields: []Field{}},
	}
	if !reflect.DeepEqual(tables, want) {
		t.Errorf("got %+v, want %+v", tables, want)
	}
}

func TestTablesUnknownDatabase(t *testing.T) {
	if _, err := NewClient(fakeMetabase(t).URL, "secret", "").Tables(2); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestUser(t *testing.T) {
	client := NewClient(fakeMetabase(t).URL, "secret", "")
	tests := []struct {
		id   string
		want User
	}{
		{"7", User{Email: "alice@example.com", IsActive: true}},
		{"8", User{Email: "bob@example.com", IsActive: false}},
		{"9", User{Email: "carol@example.com", IsActive: false}},
	}
	for _, tt := range tests {
		got, err := client.User(tt.id)
		if err != nil {
			t.Errorf("user %s: %v", tt.id, err)
		} else if got.Email != tt.want.Email || got.IsActive != tt.want.IsActive {
			t.Errorf("user %s: got %+v, want %+v", tt.id, got, tt.want)
		}
	}

	if _, err := client.User("10"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: got %v, want ErrNotFound", err)
	}
}

func TestUserBadApiKey(t *testing.T) {
	client := NewClient(fakeMetabase(t).URL, "wrong", "")
	_, err := client.User("7")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want an error other than ErrNotFound", err)
	}
}

func TestApiKeyFile(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "metabase-api-key")
	client := NewClient(fakeMetabase(t).URL, "", keyFile)

	if _, err := client.User("7"); err == nil {
		t.Error("no key file: got no error")
	}
	if err := os.WriteFile(keyFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.User("7"); err == nil {
		t.Error("empty key file: got no error")
	}
	// Written after the client was made, as compose's setup does.
	if err := os.WriteFile(keyFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.User("7"); err != nil {
		t.Errorf("key file: %v", err)
	}
}

func TestUsers(t *testing.T) {
	users, err := NewClient(fakeMetabase(t).URL, "secret", "").Users()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 || users[1].Email != "alice@example.com" || !users[1].IsActive ||
		!reflect.DeepEqual(users[1].GroupIDs, []int{1, 5}) {
		t.Errorf("got %+v", users)
	}
}

func TestUsersPartialList(t *testing.T) {
	server := fakeMetabaseWithUsers(t, `{"total": 3, "data": [{"id": 1, "email": "admin@example.com", "is_active": true}]}`)
	if users, err := NewClient(server.URL, "secret", "").Users(); err == nil {
		t.Errorf("got %+v and no error", users)
	}
}

func TestGroups(t *testing.T) {
	groups, err := NewClient(fakeMetabase(t).URL, "secret", "").Groups()
	if err != nil {
		t.Fatal(err)
	}
	want := []Group{
		{ID: 1, Name: "All Users", MagicGroupType: AllUsers},
		{ID: 2, Name: "Administrators", MagicGroupType: "admin"},
		{ID: 5, Name: "Analysts"},
	}
	if !reflect.DeepEqual(groups, want) {
		t.Errorf("got %+v, want %+v", groups, want)
	}
}
