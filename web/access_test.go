package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// opaInput is the part of a request to OPA that fakeOPA reads: a Trino request, or deciding_keys's paths.
type opaInput struct {
	Input struct {
		Paths   [][]string `json:"paths"`
		Context struct {
			Identity struct {
				User   string   `json:"user"`
				Groups []string `json:"groups"`
			} `json:"identity"`
		} `json:"context"`
		Action struct {
			Operation string `json:"operation"`
			Resource  struct {
				Table struct {
					CatalogName string `json:"catalogName"`
					SchemaName  string `json:"schemaName"`
					TableName   string `json:"tableName"`
				} `json:"table"`
			} `json:"resource"`
			FilterResources []struct {
				Column struct {
					TableName  string `json:"tableName"`
					ColumnName string `json:"columnName"`
					ColumnType string `json:"columnType"`
				} `json:"column"`
			} `json:"filterResources"`
		} `json:"action"`
	} `json:"input"`
}

// fakeOPAOptions changes how fakeOPA answers. The zero value answers everything.
type fakeOPAOptions struct {
	rules            string        // the rules document, if not {"example_jdbc": ["Analysts"]}
	noMasks          bool          // batchColumnMasks is undefined
	noDecidingKeys   bool          // deciding_keys is undefined, as with a custom policy
	failDecidingKeys bool          // deciding_keys fails
	failGroup        string        // allow fails for this group
	keys             []string      // the keys deciding_keys explains with, if not fakeKeys
	shortKeys        bool          // deciding_keys answers for one path fewer than it was asked about
	delay            time.Duration // how long each request takes
	inFlight         *inFlight     // counts the requests in flight, if not nil
}

// inFlight counts requests in flight, and the most seen at once.
type inFlight struct {
	mu       sync.Mutex
	now, max int
}

func (f *inFlight) start() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now++
	f.max = max(f.max, f.now)
}

func (f *inFlight) end() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now--
}

// fakeKeys are the keys fakeOPA's deciding_keys explains with, as if the rules had them.
var fakeKeys = []string{"example_jdbc", "example_jdbc.public.students", "example_jdbc.public.students.email"}

// fakeOPA serves the rules document, and decisions from a made-up policy, not the real one: Analysts read
// everything, Teachers read only the students table, and only without its email column. deciding_keys answers
// with the longest of fakeKeys that each path starts with, in order and null for none, as the real policy does
// with the rules' keys (it doesn't refuse names with "."; the real policy's tests cover that). It
// refuses requests not shaped like Trino's, with other than one group, or with a column without a type (the
// real policy would leave such a column out of its masks, so it would look readable).
func fakeOPA(t *testing.T, opts fakeOPAOptions) string {
	opa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if opts.inFlight != nil {
			opts.inFlight.start()
			defer opts.inFlight.end()
		}
		var body opaInput
		var bodyErr error
		if r.Method == http.MethodPost {
			bodyErr = json.NewDecoder(r.Body).Decode(&body)
		}
		// Return as soon as the client gives up, so closing the server doesn't wait for the delay. The server
		// only notices once the body has been read.
		select {
		case <-time.After(opts.delay):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v1/data/trino/rules" {
			rules := cmp.Or(opts.rules, `{"example_jdbc":["Analysts"]}`)
			w.Write([]byte(`{"result":` + rules + `}`))
			return
		}
		if r.Method != http.MethodPost || bodyErr != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		in := body.Input
		if r.URL.Path == "/v1/data/trino/deciding_keys" {
			if opts.failDecidingKeys {
				http.Error(w, "failing on purpose", http.StatusInternalServerError)
				return
			}
			if opts.noDecidingKeys {
				w.Write([]byte(`{}`))
				return
			}
			keys := opts.keys
			if keys == nil {
				keys = fakeKeys
			}
			result := []any{}
			for _, path := range in.Paths {
				var decided any // null
				for _, key := range keys {
					// Name by name, as the real policy matches, not by the joined text.
					parts := strings.Split(key, ".")
					if len(parts) <= len(path) && slices.Equal(parts, path[:len(parts)]) {
						decided = key
					}
				}
				result = append(result, decided)
			}
			if opts.shortKeys && len(result) > 0 {
				result = result[1:]
			}
			json.NewEncoder(w).Encode(map[string]any{"result": result})
			return
		}
		groups := in.Context.Identity.Groups
		if len(groups) != 1 || in.Context.Identity.User == "" {
			http.Error(w, "want one group and a user", http.StatusBadRequest)
			return
		}
		analyst, teacher := groups[0] == "Analysts", groups[0] == "Teachers"
		switch r.URL.Path {
		case "/v1/data/trino/allow":
			table := in.Action.Resource.Table
			if in.Action.Operation != "SelectFromColumns" || table.CatalogName == "" || table.SchemaName == "" {
				http.Error(w, "bad allow request", http.StatusBadRequest)
				return
			}
			if groups[0] == opts.failGroup {
				http.Error(w, "failing on purpose", http.StatusInternalServerError)
				return
			}
			allowed := analyst || teacher && table.TableName == "students"
			json.NewEncoder(w).Encode(map[string]any{"result": allowed})
		case "/v1/data/trino/batchColumnMasks":
			if in.Action.Operation != "GetColumnMask" {
				http.Error(w, "bad masks request", http.StatusBadRequest)
				return
			}
			if opts.noMasks {
				w.Write([]byte(`{}`))
				return
			}
			result := []any{}
			for i, resource := range in.Action.FilterResources {
				c := resource.Column
				if c.ColumnType == "" {
					http.Error(w, "a column without a type", http.StatusBadRequest)
					return
				}
				if teacher && c.TableName == "students" && c.ColumnName == "email" {
					result = append(result, map[string]any{"index": i, "viewExpression": map[string]any{
						"expression": "CAST(NULL AS " + c.ColumnType + ")"}})
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"result": result})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(opa.Close)
	return opa.URL
}

func testOPA(t *testing.T, opts fakeOPAOptions) opa {
	return opa{url: fakeOPA(t, opts), client: &http.Client{}}
}

// testCatalogs is testSchema's catalogs.
func testCatalogs(t *testing.T) map[string]map[string]map[string][]schemaColumn {
	t.Helper()
	var s schemaFile
	if err := json.Unmarshal([]byte(testSchema), &s); err != nil {
		t.Fatal(err)
	}
	return s.Catalogs
}

var testPageGroups = []string{"Analysts", "Teachers"}

// testDecided asks the fake's deciding_keys about testSchema, as the handler does.
func testDecided(t *testing.T, o opa) decidedKeys {
	t.Helper()
	decided, ok, err := o.decidingKeys(context.Background(), schemaPaths(testCatalogs(t)))
	if err != nil || !ok {
		t.Fatalf("decidingKeys: %v, %v", ok, err)
	}
	return decided
}

// cellStates is every cell's state, keyed by group and then by path joined with ".".
func cellStates(tables []tableAccess) map[string]map[string]string {
	states := map[string]map[string]string{}
	set := func(path string, access map[string]cell) {
		for group, c := range access {
			if states[group] == nil {
				states[group] = map[string]string{}
			}
			states[group][path] = c.State
		}
	}
	for _, table := range tables {
		path := table.Catalog + "." + table.Schema + "." + table.Name
		set(path, table.Access)
		for _, c := range table.Columns {
			set(path+"."+c.Name, c.Access)
		}
	}
	return states
}

func TestAccessStates(t *testing.T) {
	o := testOPA(t, fakeOPAOptions{})
	tables, failures := access(context.Background(), o, testPageGroups, testCatalogs(t), testDecided(t, o))
	if failures != 0 {
		t.Errorf("%d failures, want 0", failures)
	}
	want := []tableAccess{
		{Catalog: "example_jdbc", Schema: "public", Name: "staff", DecidedBy: "example_jdbc",
			Access: map[string]cell{"Analysts": {State: "read"}, "Teachers": {State: "denied"}},
			Columns: []columnAccess{{Name: "staff_id", Type: "bigint", DecidedBy: "example_jdbc",
				Access: map[string]cell{"Analysts": {State: "read"}, "Teachers": {State: "denied"}}}}},
		{Catalog: "example_jdbc", Schema: "public", Name: "students", DecidedBy: "example_jdbc.public.students",
			Access: map[string]cell{"Analysts": {State: "read"}, "Teachers": {State: "read"}},
			Columns: []columnAccess{
				{Name: "student_id", Type: "bigint", DecidedBy: "example_jdbc.public.students",
					Access: map[string]cell{"Analysts": {State: "read"}, "Teachers": {State: "read"}}},
				{Name: "email", Type: "varchar", DecidedBy: "example_jdbc.public.students.email",
					Access: map[string]cell{"Analysts": {State: "read"}, "Teachers": {State: "masked"}}},
				{Name: "notes", Type: "", DecidedBy: "example_jdbc.public.students",
					Access: map[string]cell{"Analysts": {State: "read"}, "Teachers": {State: "read"}}}}},
	}
	if !reflect.DeepEqual(tables, want) {
		t.Errorf("access:\n%+v\nwant\n%+v", tables, want)
	}
}

// A group whose lookups fail gets error cells, with why, and the other groups' cells are unaffected.
func TestAccessGroupFailure(t *testing.T) {
	o := testOPA(t, fakeOPAOptions{failGroup: "Teachers"})
	tables, failures := access(context.Background(), o, testPageGroups, testCatalogs(t), nil)
	if failures == 0 {
		t.Error("no failures counted")
	}
	states := cellStates(tables)
	for path, state := range states["Analysts"] {
		if state != "read" {
			t.Errorf("Analysts %s: %q, want read", path, state)
		}
	}
	for _, table := range tables {
		cells := []cell{table.Access["Teachers"]}
		for _, c := range table.Columns {
			cells = append(cells, c.Access["Teachers"])
		}
		for _, c := range cells {
			if c.State != "error" || c.Error == "" {
				t.Errorf("Teachers in %s: %+v, want an error with a message", table.Name, c)
			}
		}
	}
}

// Without a mask decision the page can't tell a masked column from a readable one, so the columns of the
// tables a group can read are errors, never read.
func TestAccessUndefinedMasks(t *testing.T) {
	o := testOPA(t, fakeOPAOptions{noMasks: true})
	tables, failures := access(context.Background(), o, testPageGroups, testCatalogs(t), nil)
	if failures != len(testPageGroups) {
		t.Errorf("%d failures, want one per group", failures)
	}
	for group, paths := range cellStates(tables) {
		for path, state := range paths {
			isColumn := strings.Count(path, ".") == 3
			want := "read"
			switch {
			case group == "Teachers" && strings.Contains(path, "staff"):
				want = "denied"
			case isColumn:
				want = "error"
			}
			if state != want {
				t.Errorf("%s %s: %q, want %q", group, path, state, want)
			}
		}
	}
}

// A slow OPA can't hold the page past the deadline: what isn't answered by then is an error.
func TestAccessDeadline(t *testing.T) {
	o := testOPA(t, fakeOPAOptions{delay: 2 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	tables, failures := access(ctx, o, testPageGroups, testCatalogs(t), nil)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v, want under a second", elapsed)
	}
	if failures == 0 {
		t.Error("no failures counted")
	}
	for group, paths := range cellStates(tables) {
		for path, state := range paths {
			if state != "error" {
				t.Errorf("%s %s: %q, want error", group, path, state)
			}
		}
	}
}

func TestDecidingKeys(t *testing.T) {
	o := testOPA(t, fakeOPAOptions{})
	decided, ok, err := o.decidingKeys(context.Background(), [][]string{
		{"example_jdbc"}, {"example_jdbc", "public", "students", "email"}, {"other"},
	})
	want := decidedKeys{`["example_jdbc"]`: "example_jdbc",
		`["example_jdbc","public","students","email"]`: "example_jdbc.public.students.email"}
	if err != nil || !ok || !reflect.DeepEqual(decided, want) {
		t.Errorf("decidingKeys: %v, %v, %v; want %v, true, nil", decided, ok, err, want)
	}
}

// A table "students.email" and column email of table students join to the same text, but are different paths.
func TestDecidingKeysPathsThatJoinAlike(t *testing.T) {
	o := testOPA(t, fakeOPAOptions{})
	table := []string{"example_jdbc", "public", "students.email"}
	column := []string{"example_jdbc", "public", "students", "email"}
	decided, ok, err := o.decidingKeys(context.Background(), [][]string{table, column})
	if err != nil || !ok {
		t.Fatalf("decidingKeys: %v, %v", ok, err)
	}
	// The real policy would decide nothing for the table, whose name has a "."; the fake doesn't refuse those.
	if got := decided.of(table); got != "example_jdbc" {
		t.Errorf("table decided by %q, want example_jdbc", got)
	}
	if got := decided.of(column); got != "example_jdbc.public.students.email" {
		t.Errorf("column decided by %q, want example_jdbc.public.students.email", got)
	}
}

// An answer that doesn't match the paths one to one can't be matched to them, so it is a failure.
func TestDecidingKeysWrongLength(t *testing.T) {
	o := testOPA(t, fakeOPAOptions{shortKeys: true})
	decided, ok, err := o.decidingKeys(context.Background(), schemaPaths(testCatalogs(t)))
	if decided != nil || ok || err == nil {
		t.Errorf("decidingKeys: %v, %v, %v; want nil, false and an error", decided, ok, err)
	}
}

// A custom policy may have no deciding_keys. That isn't a failure: the page just can't explain its cells.
func TestDecidingKeysCustomPolicy(t *testing.T) {
	o := testOPA(t, fakeOPAOptions{noDecidingKeys: true})
	decided, ok, err := o.decidingKeys(context.Background(), schemaPaths(testCatalogs(t)))
	if decided != nil || ok || err != nil {
		t.Errorf("decidingKeys: %v, %v, %v; want nil, false, nil", decided, ok, err)
	}
}

func TestAccessWithoutDecided(t *testing.T) {
	o := testOPA(t, fakeOPAOptions{})
	tables, failures := access(context.Background(), o, testPageGroups, testCatalogs(t), nil)
	if failures != 0 {
		t.Errorf("%d failures, want 0", failures)
	}
	if got := cellStates(tables)["Teachers"]["example_jdbc.public.students.email"]; got != "masked" {
		t.Errorf("Teachers' email: %q, want masked", got)
	}
	for _, table := range tables {
		if table.DecidedBy != "" {
			t.Errorf("%s decided by %q, want none", table.Name, table.DecidedBy)
		}
		for _, c := range table.Columns {
			if c.DecidedBy != "" {
				t.Errorf("%s.%s decided by %q, want none", table.Name, c.Name, c.DecidedBy)
			}
		}
	}
}

// Requests go to OPA several at a time, but never more than opaConcurrency.
func TestAccessParallel(t *testing.T) {
	tables := map[string][]schemaColumn{}
	for i := range 20 {
		tables[fmt.Sprintf("table_%02d", i)] = []schemaColumn{{Name: "id", Type: "bigint"}}
	}
	catalogs := map[string]map[string]map[string][]schemaColumn{"example_jdbc": {"public": tables}}
	counter := &inFlight{}
	o := testOPA(t, fakeOPAOptions{delay: 20 * time.Millisecond, inFlight: counter})
	_, failures := access(context.Background(), o, testPageGroups, catalogs, nil)
	if failures != 0 {
		t.Errorf("%d failures, want 0", failures)
	}
	counter.mu.Lock()
	defer counter.mu.Unlock()
	if counter.max <= 1 || counter.max > opaConcurrency {
		t.Errorf("at most %d requests in flight, want 2 to %d", counter.max, opaConcurrency)
	}
}

func TestSchemaPaths(t *testing.T) {
	want := [][]string{
		{"example_jdbc", "public", "staff"}, {"example_jdbc", "public", "staff", "staff_id"},
		{"example_jdbc", "public", "students"}, {"example_jdbc", "public", "students", "student_id"},
		{"example_jdbc", "public", "students", "email"}, {"example_jdbc", "public", "students", "notes"},
	}
	if got := schemaPaths(testCatalogs(t)); !reflect.DeepEqual(got, want) {
		t.Errorf("schemaPaths: %v, want %v", got, want)
	}
}

// deciding_keys has an entry for every table and column, so its answer outgrows the limit on the other answers
// in a big schema, and must still be read. Long keys make a big answer from fewer paths.
func TestDecidingKeysAnswerLargerThanOtherAnswers(t *testing.T) {
	catalog := strings.Repeat("c", 1000)
	o := testOPA(t, fakeOPAOptions{keys: []string{catalog}})
	var paths [][]string
	for i := range 2 * maxOPAAnswer / 1000 {
		paths = append(paths, []string{catalog, "public", fmt.Sprintf("table_%d", i)})
	}
	decided, ok, err := o.decidingKeys(context.Background(), paths)
	if err != nil || !ok {
		t.Fatalf("decidingKeys: %v, %v", ok, err)
	}
	if len(decided) != len(paths) || decided.of(paths[len(paths)-1]) != catalog {
		t.Errorf("%d entries, want %d, all decided by the catalog's key", len(decided), len(paths))
	}
}
