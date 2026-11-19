package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const testGroups = "Administrators:admin@example.com\nAnalysts:analyst@example.com\nTeachers:teacher@example.com\n"

// testSchema is a schema file as the Metabase sync writes it. notes has no type, as Metabase sometimes leaves it.
const testSchema = `{
	"connections": [{"id": 1, "name": "trino", "catalog": "example_jdbc", "address": "trino-reverse-proxy:9090",
		"viaProxy": true, "optimizedPrepare": true, "lastScan": "2026-11-08T11:30:00Z"}],
	"catalogs": {"example_jdbc": {"public": {
		"students": [{"name": "student_id", "type": "bigint"}, {"name": "email", "type": "varchar"},
			{"name": "notes", "type": ""}],
		"staff": [{"name": "staff_id", "type": "bigint"}]}}},
	"metabase": {"example_jdbc": {"public": {
		"students": {"database": 1, "table": 4, "description": "One row per enrolled student.", "hidden": true,
			"fields": {"student_id": {"id": 16}, "email": {"id": 18, "description": "School email."}}}}}}}`

// testConfig builds the fixture: a group file with the given contents, a schema file, a static dir with a docs
// page, an admin page and an empty directory, and a fake OPA (fakeOPA).
func testConfig(t *testing.T, groups string) config {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"group.txt":               groups,
		"schema.json":             testSchema,
		"static/index.html":       "docs home",
		"static/admin/index.html": "admin home",
	}
	for name, contents := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "static", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	return config{
		groupFile:  filepath.Join(dir, "group.txt"),
		schemaFile: filepath.Join(dir, "schema.json"),
		opaURL:     fakeOPA(t, fakeOPAOptions{}),
		staticDir:  filepath.Join(dir, "static"),
		now:        func() time.Time { return testNow },
	}
}

// writeTemp writes contents to a new file, and returns its path.
func writeTemp(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "file.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// get sends a GET for target, written as is, with one X-Forwarded-Email header per email.
func get(cfg config, target string, emails ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	for _, email := range emails {
		r.Header.Add(emailHeader, email)
	}
	w := httptest.NewRecorder()
	newHandler(cfg).ServeHTTP(w, r)
	return w
}

func TestAccess(t *testing.T) {
	cfg := testConfig(t, testGroups)
	tests := []struct {
		emails   []string
		path     string
		status   int
		body     string
		location string
	}{
		{nil, "/", http.StatusForbidden, "", ""},
		{[]string{"teacher@example.com", "admin@example.com"}, "/", http.StatusForbidden, "", ""},
		{[]string{""}, "/", http.StatusForbidden, "", ""},
		{[]string{"nobody@example.com"}, "/", http.StatusForbidden, "", ""},
		{[]string{"teacher@example.com"}, "/", http.StatusOK, "docs home", ""},
		{[]string{"Teacher@Example.com"}, "/", http.StatusOK, "docs home", ""},
		{[]string{"teacher@example.com"}, "/admin/", http.StatusForbidden, "", ""},
		{[]string{"teacher@example.com"}, "/admin", http.StatusMovedPermanently, "", "/admin/"},
		{[]string{"teacher@example.com"}, "/x/../admin/", http.StatusMovedPermanently, "", "/admin/"},
		{[]string{"teacher@example.com"}, "/admin%2F", http.StatusForbidden, "", ""},
		{[]string{"teacher@example.com"}, "/api/admin/rules", http.StatusForbidden, "", ""},
		{[]string{"teacher@example.com"}, "/empty/", http.StatusNotFound, "", ""},
		{[]string{"admin@example.com"}, "/admin/", http.StatusOK, "admin home", ""},
		{nil, "/healthz", http.StatusOK, "ok", ""},
	}
	for _, tt := range tests {
		w := get(cfg, tt.path, tt.emails...)
		if w.Code != tt.status {
			t.Errorf("%q %s: status %d, want %d", tt.emails, tt.path, w.Code, tt.status)
			continue
		}
		if !strings.Contains(w.Body.String(), tt.body) {
			t.Errorf("%q %s: body %q, want it to contain %q", tt.emails, tt.path, w.Body, tt.body)
		}
		if got := w.Header().Get("Location"); tt.location != "" && got != tt.location {
			t.Errorf("%q %s: Location %q, want %q", tt.emails, tt.path, got, tt.location)
		}
	}

	// An encoded ".." is redirected or refused; it never reaches the admin page.
	w := get(cfg, "/%2e%2e/admin/", "teacher@example.com")
	if w.Code != http.StatusMovedPermanently && w.Code != http.StatusForbidden {
		t.Errorf("/%%2e%%2e/admin/: status %d, want 301 or 403", w.Code)
	}
}

func checkJSON(t *testing.T, w *httptest.ResponseRecorder, want string) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200; body %q", w.Code, w.Body)
	}
	var got, wanted any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("body %q is not JSON: %v", w.Body, err)
	}
	if err := json.Unmarshal([]byte(want), &wanted); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, wanted) {
		t.Errorf("body %s, want %s", w.Body, want)
	}
}

// Every response, refusals and redirects too, carries the security headers. Only the JSON answers are no-store.
func TestSecurityHeaders(t *testing.T) {
	cfg := testConfig(t, testGroups)
	for _, tt := range []struct {
		path, email string
		noStore     bool
	}{
		{"/", "teacher@example.com", false},
		{"/admin/", "teacher@example.com", false},
		{"/admin", "teacher@example.com", false},
		{"/healthz", "", false},
		{"/api/me", "teacher@example.com", true},
		{"/api/admin/rules", "admin@example.com", true},
	} {
		w := get(cfg, tt.path, tt.email)
		for header, want := range map[string]string{
			"Content-Security-Policy":    contentSecurityPolicy,
			"X-Content-Type-Options":     "nosniff",
			"X-Frame-Options":            "DENY",
			"Referrer-Policy":            "same-origin",
			"Cross-Origin-Opener-Policy": "same-origin",
		} {
			if got := w.Header().Get(header); got != want {
				t.Errorf("%s (%d): %s %q, want %q", tt.path, w.Code, header, got, want)
			}
		}
		if noStore := w.Header().Get("Cache-Control") == "no-store"; noStore != tt.noStore {
			t.Errorf("%s: Cache-Control %q, want no-store: %v", tt.path, w.Header().Get("Cache-Control"), tt.noStore)
		}
	}
}

func TestMe(t *testing.T) {
	cfg := testConfig(t, testGroups)
	checkJSON(t, get(cfg, "/api/me", "analyst@example.com"),
		`{"email":"analyst@example.com","groups":["Analysts"],"admin":false}`)
	checkJSON(t, get(cfg, "/api/me", "admin@example.com"),
		`{"email":"admin@example.com","groups":["Administrators"],"admin":true}`)
}

func TestConfig(t *testing.T) {
	cfg := testConfig(t, testGroups)
	cfg.metabaseLinkURL = "https://metabase.example.com"
	cfg.prometheusLinkURL = "https://metrics.example.com"
	checkJSON(t, get(cfg, "/api/config", "analyst@example.com"),
		`{"metabaseUrl":"https://metabase.example.com","prometheusUrl":""}`)
	checkJSON(t, get(cfg, "/api/config", "admin@example.com"),
		`{"metabaseUrl":"https://metabase.example.com","prometheusUrl":"https://metrics.example.com"}`)
	if w := get(cfg, "/api/config", "stranger@example.com"); w.Code != http.StatusForbidden {
		t.Errorf("status %d for a non-member, want 403", w.Code)
	}
}

// The whole document. The groups are the group file's and the rules', without Administrators: Teachers is only
// in the group file. Every catalog Trino, Metabase or the rules name is listed. Tables are sorted, and columns
// keep the table's order. Each table and column has every group's state, and the key that decided it.
func TestAdminRules(t *testing.T) {
	cfg := testConfig(t, testGroups)
	cfg.trinoCatalogs = []string{"example_jdbc", "openapi"}
	cfg.metabaseLinkURL = "http://localhost:8080"
	cfg.statusFile = writeTemp(t, `{"intervalSeconds": 30, "files": {"group": {}, "schema": {}}}`)
	// The sync marks each file with when it last synced.
	setModTime(t, cfg.groupFile, "2026-11-08T11:59:50Z")
	setModTime(t, cfg.schemaFile, "2026-11-08T11:59:40Z")
	w := get(cfg, "/api/admin/rules", "admin@example.com")
	// The schema file has Metabase's IDs for students and two of its columns, as a sync might after a column
	// was added, and none for staff, as an older sync's file would have. Only students and its email column
	// have a description.
	checkJSON(t, w, `{
		"now": "2026-11-08T12:00:00Z",
		"metabaseUrl": "http://localhost:8080",
		"rules": {"example_jdbc": ["Analysts"]},
		"rulesFormat": "standard",
		"explained": true,
		"groups": ["Analysts", "Teachers"],
		"sync": {"intervalSeconds": 30, "files": {
			"group": {"syncedAt": "2026-11-08T11:59:50Z"},
			"schema": {"syncedAt": "2026-11-08T11:59:40Z"}}},
		"warnings": [
			{"severity": "warning", "kind": "catalog-not-in-metabase", "catalog": "openapi",
				"message": "openapi is a Trino catalog, but no Metabase database connects to it through the reverse proxy, so people can't reach it in Metabase."},
			{"severity": "note", "kind": "catalog-without-rules", "catalog": "openapi",
				"message": "No rule names openapi, so only Administrators can read it."}],
		"catalogs": [
			{"name": "example_jdbc", "inTrino": true,
				"connections": [{"id": 1, "name": "trino", "optimizedPrepare": true, "lastScan": "2026-11-08T11:30:00Z"}],
				"bypassing": [],
				"rules": [{"key": "example_jdbc", "level": "catalog", "groups": ["Analysts"]}],
				"tables": [
					{"catalog": "example_jdbc", "schema": "public", "name": "staff", "decidedBy": "example_jdbc",
						"access": {"Analysts": {"state": "read"}, "Teachers": {"state": "denied"}},
						"columns": [
							{"name": "staff_id", "type": "bigint", "decidedBy": "example_jdbc",
								"access": {"Analysts": {"state": "read"}, "Teachers": {"state": "denied"}}}]},
					{"catalog": "example_jdbc", "schema": "public", "name": "students", "decidedBy": "example_jdbc.public.students",
						"metabase": {"database": 1, "table": 4}, "description": "One row per enrolled student.",
						"hiddenInMetabase": true,
						"access": {"Analysts": {"state": "read"}, "Teachers": {"state": "read"}},
						"columns": [
							{"name": "student_id", "type": "bigint", "decidedBy": "example_jdbc.public.students",
								"metabaseField": 16,
								"access": {"Analysts": {"state": "read"}, "Teachers": {"state": "read"}}},
							{"name": "email", "type": "varchar", "decidedBy": "example_jdbc.public.students.email",
								"metabaseField": 18, "description": "School email.",
								"access": {"Analysts": {"state": "read"}, "Teachers": {"state": "masked"}}},
							{"name": "notes", "type": "", "decidedBy": "example_jdbc.public.students",
								"access": {"Analysts": {"state": "read"}, "Teachers": {"state": "read"}}}]}]},
			{"name": "openapi", "inTrino": true, "connections": [], "bypassing": [], "rules": [], "tables": []}]}`)
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type %q, want application/json", got)
	}
}

// setModTime sets path's modification time to at, in RFC 3339.
func setModTime(t *testing.T, path, at string) {
	t.Helper()
	when, err := time.Parse(time.RFC3339, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

// The sync warnings come from the status file's errors and the files' own times: here the group file last
// synced two hours ago, and the schema file's last sync failed, though it synced a minute ago.
func TestAdminRulesSyncWarnings(t *testing.T) {
	cfg := testConfig(t, testGroups)
	cfg.statusFile = writeTemp(t, `{"intervalSeconds": 30, "files": {"group": {},
		"schema": {"lastError": "connection refused", "lastErrorAt": "2026-11-08T11:59:30Z"}}}`)
	setModTime(t, cfg.groupFile, "2026-11-08T10:00:00Z")
	setModTime(t, cfg.schemaFile, "2026-11-08T11:59:00Z")
	doc := rulesDocument(t, get(cfg, "/api/admin/rules", "admin@example.com"))
	if got, want := kinds(doc.Warnings), []string{kindSyncFailing, kindSyncStale}; !reflect.DeepEqual(got, want) {
		t.Errorf("warnings %v, want %v", got, want)
	}
	schema := doc.Sync.Files["schema"]
	if schema.SyncedAt == nil || schema.LastError != "connection refused" || schema.LastErrorAt == nil {
		t.Errorf("schema's sync %+v, want its time and error", schema)
	}
}

// rulesDocument reads the response to a request for the admin page's document.
func rulesDocument(t *testing.T, w *httptest.ResponseRecorder) (doc struct {
	Rules       json.RawMessage
	RulesFormat string
	Explained   bool
	Groups      []string
	Sync        syncReport
	Warnings    []warning
	Catalogs    []catalogEntry
}) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200; body %q", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// kinds returns the kinds of warnings, in order.
func kinds(warnings []warning) []string {
	var ks []string
	for _, w := range warnings {
		ks = append(ks, w.Kind)
	}
	return ks
}

// Until the Metabase sync first writes its files, there are no tables, connections or schema sync time, but the
// rules still show. The group file is the sync's placeholder, which has never synced. Without a status file
// there are no sync warnings. Without TRINO_CATALOGS, whether a catalog is in Trino is unknown.
func TestAdminRulesWithoutSyncFiles(t *testing.T) {
	cfg := testConfig(t, testGroups)
	cfg.schemaFile = filepath.Join(t.TempDir(), "missing.json")
	cfg.statusFile = filepath.Join(t.TempDir(), "missing.json")
	os.Chtimes(cfg.groupFile, time.Unix(0, 0), time.Unix(0, 0))
	w := get(cfg, "/api/admin/rules", "admin@example.com")
	doc := rulesDocument(t, w)
	if wantSync := (syncReport{Files: map[string]fileSync{"group": {}, "schema": {}}}); !reflect.DeepEqual(doc.Sync, wantSync) {
		t.Errorf("sync %+v, want %+v", doc.Sync, wantSync)
	}
	if len(doc.Warnings) != 0 {
		t.Errorf("warnings %v, want none", kinds(doc.Warnings))
	}
	var body struct{ Catalogs json.RawMessage }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var got, want any
	json.Unmarshal(body.Catalogs, &got)
	json.Unmarshal([]byte(`[{"name": "example_jdbc", "inTrino": null, "connections": [], "bypassing": [],
		"rules": [{"key": "example_jdbc", "level": "catalog", "groups": ["Analysts"]}], "tables": []}]`), &want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("catalogs %s, want one with the rule alone", body.Catalogs)
	}
}

// A schema file from an older Metabase sync leaves the page without tables and connections, which a warning says,
// but the rules and groups still show, since they help to fix it.
func TestAdminRulesOldSchemaFile(t *testing.T) {
	cfg := testConfig(t, testGroups)
	cfg.schemaFile = writeTemp(t, `{"example_jdbc": {"public": {"students": []}}}`)
	doc := rulesDocument(t, get(cfg, "/api/admin/rules", "admin@example.com"))
	var found *warning
	for i, w := range doc.Warnings {
		if w.Kind == kindSchemaOldFormat {
			found = &doc.Warnings[i]
		}
	}
	if found == nil {
		t.Fatalf("warnings %v, want %s", kinds(doc.Warnings), kindSchemaOldFormat)
	}
	if found.Severity != severityWarning || found.Catalog != "" {
		t.Errorf("warning %+v, want a top-level warning", *found)
	}
	for _, want := range []string{"older format", "update the Metabase sync image", "column list and connections"} {
		if !strings.Contains(found.Message, want) {
			t.Errorf("message %q lacks %q", found.Message, want)
		}
	}
	if string(doc.Rules) != `{"example_jdbc":["Analysts"]}` {
		t.Errorf("rules %s, want the rules still", doc.Rules)
	}
	if !slices.Contains(doc.Groups, "Analysts") {
		t.Errorf("groups %v, want Analysts", doc.Groups)
	}
	for _, c := range doc.Catalogs {
		if len(c.Tables) != 0 || len(c.Connections) != 0 {
			t.Errorf("catalog %+v, want no tables or connections", c)
		}
	}
}

// Any other trouble reading the schema file is still an error.
func TestAdminRulesBrokenSchemaFile(t *testing.T) {
	cfg := testConfig(t, testGroups)
	cfg.schemaFile = writeTemp(t, `{not json`)
	if w := get(cfg, "/api/admin/rules", "admin@example.com"); w.Code != http.StatusInternalServerError {
		t.Errorf("status %d, want 500", w.Code)
	}
}

// Failed lookups don't fail the page: their cells are errors, a warning counts them, and the rest still show. A
// policy without deciding_keys leaves the cells unexplained.
func TestAdminRulesFailedLookups(t *testing.T) {
	cfg := testConfig(t, testGroups)
	cfg.opaURL = fakeOPA(t, fakeOPAOptions{failGroup: "Teachers", noDecidingKeys: true})
	doc := rulesDocument(t, get(cfg, "/api/admin/rules", "admin@example.com"))
	if !slices.Contains(kinds(doc.Warnings), kindOPAFailures) {
		t.Errorf("warnings %v, want %s", kinds(doc.Warnings), kindOPAFailures)
	}
	if len(doc.Catalogs) != 1 || len(doc.Catalogs[0].Tables) != 2 {
		t.Fatalf("catalogs %+v, want example_jdbc with two tables", doc.Catalogs)
	}
	students := doc.Catalogs[0].Tables[1]
	if students.DecidedBy != "" {
		t.Errorf("students decided by %q, want none", students.DecidedBy)
	}
	if doc.Explained {
		t.Error("explained, want not explained without deciding_keys")
	}
	if got := students.Access["Analysts"].State; got != "read" {
		t.Errorf("Analysts on students: %q, want read", got)
	}
	if got := students.Access["Teachers"]; got.State != "error" || got.Error == "" {
		t.Errorf("Teachers on students: %+v, want an error with a message", got)
	}
}

// When deciding_keys fails, the page says it can't explain the cells, rather than that no rule decides them.
func TestAdminRulesDecidingKeysFails(t *testing.T) {
	cfg := testConfig(t, testGroups)
	cfg.opaURL = fakeOPA(t, fakeOPAOptions{failDecidingKeys: true})
	doc := rulesDocument(t, get(cfg, "/api/admin/rules", "admin@example.com"))
	if doc.Explained {
		t.Error("explained, want not explained when deciding_keys fails")
	}
	if !slices.Contains(kinds(doc.Warnings), kindOPAFailures) {
		t.Errorf("warnings %v, want %s", kinds(doc.Warnings), kindOPAFailures)
	}
}

// A policy whose rules document isn't {key: [groups]} still gets its cells, with a warning that the rules
// can't be checked.
func TestAdminRulesCustomRulesFormat(t *testing.T) {
	cfg := testConfig(t, testGroups)
	cfg.opaURL = fakeOPA(t, fakeOPAOptions{rules: `{"rules": "elsewhere"}`})
	doc := rulesDocument(t, get(cfg, "/api/admin/rules", "admin@example.com"))
	if doc.RulesFormat != "custom" {
		t.Errorf("rulesFormat %q, want custom", doc.RulesFormat)
	}
	if got, want := kinds(doc.Warnings), []string{kindRulesCustom}; !reflect.DeepEqual(got, want) {
		t.Errorf("warnings %v, want %v", got, want)
	}
	if len(doc.Catalogs) != 1 || len(doc.Catalogs[0].Rules) != 0 || len(doc.Catalogs[0].Tables) != 2 {
		t.Fatalf("catalogs %+v, want example_jdbc with no rules and two tables", doc.Catalogs)
	}
	if got := doc.Catalogs[0].Tables[1].Access["Teachers"].State; got != "read" {
		t.Errorf("Teachers on students: %q, want read", got)
	}
}

// Whether a rule narrows its parent's groups is checked against the key OPA says decides the parent, and not
// at all when OPA can't say.
func TestAdminRulesParentKeys(t *testing.T) {
	rules := `{"example_jdbc": ["Analysts"], "example_jdbc.public.students": ["Teachers"]}`
	cfg := testConfig(t, testGroups)
	cfg.opaURL = fakeOPA(t, fakeOPAOptions{rules: rules})
	doc := rulesDocument(t, get(cfg, "/api/admin/rules", "admin@example.com"))
	var narrows []string
	for _, w := range doc.Warnings {
		if w.Kind == kindRuleNarrows {
			narrows = append(narrows, w.Key)
		}
	}
	if want := []string{"example_jdbc.public.students"}; !reflect.DeepEqual(narrows, want) {
		t.Errorf("rule-narrows for %v, want %v", narrows, want)
	}

	cfg.opaURL = fakeOPA(t, fakeOPAOptions{rules: rules, noDecidingKeys: true})
	doc = rulesDocument(t, get(cfg, "/api/admin/rules", "admin@example.com"))
	if len(doc.Warnings) != 0 {
		t.Errorf("warnings %v without deciding_keys, want none", kinds(doc.Warnings))
	}
}

func TestAdminRulesOPADown(t *testing.T) {
	cfg := testConfig(t, testGroups)
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	cfg.opaURL = closed.URL
	if w := get(cfg, "/api/admin/rules", "admin@example.com"); w.Code != http.StatusBadGateway {
		t.Errorf("status %d, want 502", w.Code)
	}
}

func TestEmptyGroupFile(t *testing.T) {
	if w := get(testConfig(t, ""), "/", "teacher@example.com"); w.Code != http.StatusForbidden {
		t.Errorf("status %d, want 403", w.Code)
	}
}

func TestMissingGroupFile(t *testing.T) {
	cfg := testConfig(t, testGroups)
	cfg.groupFile = filepath.Join(t.TempDir(), "missing.txt")
	if w := get(cfg, "/", "teacher@example.com"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", w.Code)
	}
}

// A connection that bypasses the reverse proxy is listed apart from those that use it, so the page can link to it
// for fixing; one without a catalog belongs to no catalog.
func TestCatalogEntriesBypassing(t *testing.T) {
	entries := catalogEntries(nil, []metabaseConnection{
		{ID: 2, Name: "direct", Catalog: "openapi", Address: "trino:8080"},
		{ID: 1, Name: "trino", Catalog: "openapi", Address: "127.0.0.1:9090", ViaProxy: true},
		{ID: 3, Name: "no catalog", Address: "trino:8080"},
	}, nil, nil)
	want := []catalogEntry{{
		Name:        "openapi",
		Connections: []connectionEntry{{ID: 1, Name: "trino"}},
		Bypassing:   []connectionEntry{{ID: 2, Name: "direct"}},
		Rules:       []ruleEntry{},
		Tables:      []tableAccess{},
	}}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("got %+v, want %+v", entries, want)
	}
}
