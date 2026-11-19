package main

import (
	"slices"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

// quietInput is a deployment with nothing to warn about: catalog c in Trino and in Metabase through the proxy,
// one rule for it naming a group in the group file, and both syncs just run.
func quietInput() warningInput {
	fresh := testNow.Add(-10 * time.Second)
	return warningInput{
		rules:         map[string][]string{"c": {"A"}},
		parentKeys:    map[string]string{},
		fileGroups:    []string{"A", "Administrators", "B"},
		trinoCatalogs: []string{"c"},
		schema: schemaFile{
			Connections: []metabaseConnection{{ID: 1, Name: "trino", Catalog: "c", ViaProxy: true}},
			Catalogs: map[string]map[string]map[string][]schemaColumn{
				"c": {"s": {"t": {{Name: "col", Type: "bigint"}}}},
			},
		},
		status:   &syncStatus{IntervalSeconds: 30, Files: map[string]*fileStatus{"group": {}, "schema": {}}},
		syncedAt: map[string]*time.Time{"group": &fresh, "schema": &fresh},
		now:      testNow,
	}
}

// expectWarnings checks that got is exactly want, in order, comparing everything but the messages, which must
// each contain every string in mention.
func expectWarnings(t *testing.T, got []warning, want []warning, mention ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("warnings %+v, want %+v", got, want)
	}
	for i := range got {
		message := got[i].Message
		got[i].Message = ""
		if got[i] != want[i] {
			t.Errorf("warning %d: %+v, want %+v", i, got[i], want[i])
		}
		if message == "" {
			t.Errorf("warning %d has no message", i)
		}
		for _, s := range mention {
			if !strings.Contains(message, s) {
				t.Errorf("message %q doesn't mention %q", message, s)
			}
		}
	}
}

func TestQuietInput(t *testing.T) {
	if got := findWarnings(quietInput()); len(got) != 0 {
		t.Errorf("warnings %+v, want none", got)
	}
}

// "Optimized prepared statements" is only warned about when it's known to be off, on a connection through the
// proxy: a connection that bypasses the proxy has its own warning.
func TestExplicitPrepareWarningOnlyWhenKnownOff(t *testing.T) {
	on, off := true, false
	for _, c := range []metabaseConnection{
		{ID: 1, Name: "trino", Catalog: "c", ViaProxy: true, OptimizedPrepare: &on},
		{ID: 1, Name: "trino", Catalog: "c", ViaProxy: true}, // a file from an older sync
	} {
		in := quietInput()
		in.schema.Connections = []metabaseConnection{c}
		if got := findWarnings(in); len(got) != 0 {
			t.Errorf("%+v: warnings %+v, want none", c, got)
		}
	}
	in := quietInput()
	in.schema.Connections = append(in.schema.Connections,
		metabaseConnection{ID: 2, Name: "direct", Catalog: "c", Address: "trino:8080", OptimizedPrepare: &off})
	if got := kinds(findWarnings(in)); !slices.Equal(got, []string{kindConnectionBypassesProxy}) {
		t.Errorf("bypassing connection: warnings %v, want only %s", got, kindConnectionBypassesProxy)
	}
}

// Each kind of warning, from a change to quietInput that should cause it alone.
func TestWarningKinds(t *testing.T) {
	tests := []struct {
		kind    string
		change  func(in *warningInput)
		want    warning
		mention []string
	}{
		{kindSyncFailing, func(in *warningInput) { in.status.Files["schema"].LastError = "connection refused" },
			warning{Severity: severityWarning, Kind: kindSyncFailing}, []string{"column list", "connection refused"}},
		{kindSyncFailing, func(in *warningInput) { in.syncedAt["group"] = nil },
			warning{Severity: severityWarning, Kind: kindSyncFailing}, []string{"groups", "never"}},
		{kindSyncStale, func(in *warningInput) {
			old := testNow.Add(-2 * time.Hour)
			in.syncedAt["group"] = &old
		}, warning{Severity: severityWarning, Kind: kindSyncStale}, []string{"groups", "2 hours", "30 seconds"}},
		{kindRulesCustom, func(in *warningInput) { in.rules, in.rulesCustom = nil, true },
			warning{Severity: severityWarning, Kind: kindRulesCustom}, nil},
		{kindOPAFailures, func(in *warningInput) { in.opaFailures = 3 },
			warning{Severity: severityWarning, Kind: kindOPAFailures}, []string{"3 lookups", "⚠"}},
		{kindCatalogNotInMetabase, func(in *warningInput) {
			in.trinoCatalogs = []string{"c", "d"}
			in.rules["d"] = []string{"A"}
		}, warning{Severity: severityWarning, Kind: kindCatalogNotInMetabase, Catalog: "d"}, []string{"d"}},
		{kindCatalogNotInTrino, func(in *warningInput) {
			in.schema.Connections = append(in.schema.Connections,
				metabaseConnection{ID: 2, Name: "other", Catalog: "gone", ViaProxy: true})
		}, warning{Severity: severityWarning, Kind: kindCatalogNotInTrino, Catalog: "gone"}, []string{"#2 other", "gone"}},
		{kindConnectionExplicitPrepare, func(in *warningInput) {
			off := false
			in.schema.Connections[0].OptimizedPrepare = &off
		}, warning{Severity: severityWarning, Kind: kindConnectionExplicitPrepare, Catalog: "c"},
			[]string{"#1 trino", "Optimized prepared statements"}},
		{kindDuplicateConnections, func(in *warningInput) {
			in.schema.Connections = append(in.schema.Connections,
				metabaseConnection{ID: 7, Name: "copy", Catalog: "c", ViaProxy: true})
		}, warning{Severity: severityWarning, Kind: kindDuplicateConnections, Catalog: "c"},
			[]string{"#1 trino", "#7 copy", "lowest ID"}},
		{kindConnectionBypassesProxy, func(in *warningInput) {
			in.schema.Connections = append(in.schema.Connections,
				metabaseConnection{ID: 3, Name: "direct", Catalog: "c", Address: "trino:8080"})
		}, warning{Severity: severityWarning, Kind: kindConnectionBypassesProxy, Catalog: "c"},
			[]string{"#3 direct", "trino:8080", "own Trino user"}},
		{kindConnectionWithoutCatalog, func(in *warningInput) {
			in.schema.Connections = append(in.schema.Connections,
				metabaseConnection{ID: 4, Name: "everything", ViaProxy: true})
		}, warning{Severity: severityWarning, Kind: kindConnectionWithoutCatalog}, []string{"#4 everything"}},
		{kindRuleUnknownCatalog, func(in *warningInput) { in.rules["nope.s"] = []string{"A"} },
			warning{Severity: severityWarning, Kind: kindRuleUnknownCatalog, Catalog: "nope", Key: "nope.s"},
			[]string{"nope.s"}},
		{kindRuleUnknownGroup, func(in *warningInput) { in.rules["c"] = []string{"A", "Ghosts"} },
			warning{Severity: severityWarning, Kind: kindRuleUnknownGroup, Catalog: "c", Key: "c"},
			[]string{"Ghosts", "grants nothing"}},
		{kindRuleNarrows, func(in *warningInput) {
			in.rules["c.s.t"] = []string{"B"}
			in.parentKeys["c.s.t"] = "c"
		}, warning{Severity: severityWarning, Kind: kindRuleNarrows, Catalog: "c", Key: "c.s.t"}, []string{"A", "c.s.t"}},
		{kindRuleNarrows, func(in *warningInput) {
			in.rules["c.s"] = []string{}
			in.parentKeys["c.s"] = "c"
		}, warning{Severity: severityWarning, Kind: kindRuleNarrows, Catalog: "c", Key: "c.s"}, []string{"A", "c.s"}},
		{kindRuleColumnWithoutTable, func(in *warningInput) {
			in.rules["c.s.t.col"] = []string{"A", "B"}
			in.parentKeys["c.s.t.col"] = "c"
		}, warning{Severity: severityWarning, Kind: kindRuleColumnWithoutTable, Catalog: "c", Key: "c.s.t.col"},
			[]string{"B", "c.s.t.col"}},
		{kindRuleColumnWithoutTable, func(in *warningInput) {
			delete(in.rules, "c")
			in.rules["c.s.t.col"] = []string{"A"}
			in.parentKeys["c.s.t.col"] = ""
		}, warning{Severity: severityWarning, Kind: kindRuleColumnWithoutTable, Catalog: "c", Key: "c.s.t.col"},
			[]string{"A", "no rule"}},
		{kindRuleNotInMetabase, func(in *warningInput) { in.rules["c.s.missing"] = []string{"A"} },
			warning{Severity: severityNote, Kind: kindRuleNotInMetabase, Catalog: "c", Key: "c.s.missing"},
			[]string{"c.s.missing", "missing"}},
		{kindRuleNotInMetabase, func(in *warningInput) { in.rules["c.other"] = []string{"A"} },
			warning{Severity: severityNote, Kind: kindRuleNotInMetabase, Catalog: "c", Key: "c.other"}, []string{"other"}},
		{kindRuleNotInMetabase, func(in *warningInput) { in.rules["c.s.t.gone"] = []string{"A"} },
			warning{Severity: severityNote, Kind: kindRuleNotInMetabase, Catalog: "c", Key: "c.s.t.gone"}, []string{"gone"}},
		{kindCatalogWithoutRules, func(in *warningInput) { delete(in.rules, "c") },
			warning{Severity: severityNote, Kind: kindCatalogWithoutRules, Catalog: "c"}, []string{"Administrators"}},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			in := quietInput()
			tt.change(&in)
			expectWarnings(t, findWarnings(in), []warning{tt.want}, tt.mention...)
		})
	}
}

// Without a status file there are no sync warnings, however old the files are or if they never synced.
func TestNoSyncWarningsWithoutStatus(t *testing.T) {
	in := quietInput()
	in.status = nil
	old := testNow.Add(-48 * time.Hour)
	in.syncedAt = map[string]*time.Time{"group": &old, "schema": nil}
	if got := findWarnings(in); len(got) != 0 {
		t.Errorf("warnings %+v, want none", got)
	}
}

// Only files both the status file has and the page reads are checked: the page doesn't know the time of a file
// it doesn't read (no SCHEMA_FILE in the web server), and a sync may not write a file the status file lacks.
func TestSyncWarningsOnlyForFilesBothKnow(t *testing.T) {
	in := quietInput()
	delete(in.syncedAt, "schema")
	in.status.Files["schema"].LastError = "connection refused"
	delete(in.status.Files, "group")
	in.syncedAt["group"] = nil
	if got := findWarnings(in); len(got) != 0 {
		t.Errorf("warnings %+v, want none", got)
	}
}

// The policy compares group names exactly, so a rule's group spelled with other case grants nothing.
func TestRuleUnknownGroupCase(t *testing.T) {
	in := quietInput()
	in.fileGroups = []string{"Administrators", "Analysts"}
	in.rules = map[string][]string{"c": {"analysts"}}
	expectWarnings(t, findWarnings(in), []warning{
		{Severity: severityWarning, Kind: kindRuleUnknownGroup, Catalog: "c", Key: "c"},
	}, "analysts")
}

func TestRuleLevel(t *testing.T) {
	for key, want := range map[string]string{
		"c": "catalog", "c.s": "schema", "c.s.t": "table", "c.s.t.col": "column", "c.s.t.a.b": "unusable",
	} {
		if got := ruleLevel(key); got != want {
			t.Errorf("ruleLevel(%q): %q, want %q", key, got, want)
		}
	}
}

func TestParentPath(t *testing.T) {
	for key, want := range map[string][]string{
		"c": nil, "c.s": {"c"}, "c.s.t": {"c", "s"}, "c.s.t.col": {"c", "s", "t"}, "c.s.t.a.b": nil,
	} {
		got, ok := parentPath(key)
		if ok != (want != nil) || !slices.Equal(got, want) {
			t.Errorf("parentPath(%q): %q, %v; want %q", key, got, ok, want)
		}
	}
}

// A key of more than four parts can't match anything, since no name the policy matches contains ".". It gets
// that warning alone, and doesn't count as a rule for its catalog.
func TestRuleTooManyParts(t *testing.T) {
	in := quietInput()
	in.rules["c.s.t.a.b"] = []string{"Nobody"}
	in.parentKeys["c.s.t.a.b"] = "c"
	expectWarnings(t, findWarnings(in), []warning{
		{Severity: severityWarning, Kind: kindRuleTooManyParts, Catalog: "c", Key: "c.s.t.a.b"},
	}, "5 parts")

	in = quietInput()
	in.rules = map[string][]string{"c.s.t.a.b": {"A"}}
	expectWarnings(t, findWarnings(in), []warning{
		{Severity: severityNote, Kind: kindCatalogWithoutRules, Catalog: "c"},
		{Severity: severityWarning, Kind: kindRuleTooManyParts, Catalog: "c", Key: "c.s.t.a.b"},
	})
}

// A table with a "." in its schema's or its own name is for Administrators only; so are columns with one.
func TestNameWithDot(t *testing.T) {
	in := quietInput()
	in.schema.Catalogs["c"]["s"]["t.a"] = []schemaColumn{{Name: "x.y"}}
	expectWarnings(t, findWarnings(in), []warning{
		{Severity: severityWarning, Kind: kindNameWithDot, Catalog: "c"},
	}, `c.s."t.a"`, "only Administrators")

	in = quietInput()
	in.schema.Catalogs["c"]["s.x"] = map[string][]schemaColumn{"t": {{Name: "col"}}}
	expectWarnings(t, findWarnings(in), []warning{
		{Severity: severityWarning, Kind: kindNameWithDot, Catalog: "c"},
	}, `c."s.x".t`)

	in = quietInput()
	in.schema.Catalogs["c"]["s"]["t"] = append(in.schema.Catalogs["c"]["s"]["t"],
		schemaColumn{Name: "a.b"}, schemaColumn{Name: "plain"}, schemaColumn{Name: "c.d"})
	expectWarnings(t, findWarnings(in), []warning{
		{Severity: severityWarning, Kind: kindNameWithDot, Catalog: "c"},
	}, `Columns "a.b" and "c.d" of table c.s.t have`, "NULL")
}

func TestDisplayPath(t *testing.T) {
	if got, want := displayPath("c", "s", `t."a"`, "col"), `c.s."t.""a""".col`; got != want {
		t.Errorf("displayPath: %s, want %s", got, want)
	}
}

// Hiding a column from some of the groups that read its table is what column keys are for.
func TestColumnNarrowingIsNotWarned(t *testing.T) {
	in := quietInput()
	in.rules = map[string][]string{"c": {"A"}, "c.s.t": {"A", "B"}, "c.s.t.col": {"A"}}
	in.parentKeys = map[string]string{"c.s.t": "c", "c.s.t.col": "c.s.t"}
	if got := findWarnings(in); len(got) != 0 {
		t.Errorf("warnings %+v, want none", got)
	}
}

// Without OPA's parent keys (a custom policy, or a failed lookup), the checks that need them are skipped.
func TestNoParentKeys(t *testing.T) {
	in := quietInput()
	in.rules["c.s.t"] = []string{"B"}
	in.rules["c.s.t.col"] = []string{"A"}
	in.parentKeys = nil
	if got := findWarnings(in); len(got) != 0 {
		t.Errorf("warnings %+v, want none", got)
	}
}

func TestNoCatalogChecksWhenUnknown(t *testing.T) {
	in := quietInput()
	in.trinoCatalogs = nil
	in.rules["elsewhere"] = []string{"A"}
	in.schema.Connections = append(in.schema.Connections,
		metabaseConnection{ID: 2, Name: "other", Catalog: "gone", ViaProxy: true})
	for _, w := range findWarnings(in) {
		if strings.HasPrefix(w.Kind, "catalog-") || w.Kind == kindRuleUnknownCatalog {
			t.Errorf("warning %+v, want no catalog checks", w)
		}
	}
}

// Trino's system catalog is always there, and needn't be in Metabase or have rules.
func TestSystemCatalog(t *testing.T) {
	in := quietInput()
	in.trinoCatalogs = []string{"c", "system"}
	in.rules["system.runtime"] = []string{"A"}
	in.parentKeys["system.runtime"] = ""
	if got := findWarnings(in); len(got) != 0 {
		t.Errorf("warnings %+v, want none", got)
	}
}

// A custom rules document skips every check of the rules, along with saying so.
func TestRulesCustomSkipsRuleChecks(t *testing.T) {
	in := quietInput()
	in.rules, in.rulesCustom = nil, true
	in.trinoCatalogs = []string{"c", "d"}
	in.schema.Connections = append(in.schema.Connections,
		metabaseConnection{ID: 2, Name: "d", Catalog: "d", ViaProxy: true})
	expectWarnings(t, findWarnings(in), []warning{{Severity: severityWarning, Kind: kindRulesCustom}})
}

// Warnings are sorted by catalog, then kind, then key, with the ones for no catalog first.
func TestWarningOrder(t *testing.T) {
	in := quietInput()
	in.opaFailures = 1
	in.rules["c.s.b"] = []string{"Ghosts"}
	in.rules["c.s.a"] = []string{"Ghosts"}
	in.rules["b"] = []string{"A"}
	in.trinoCatalogs = []string{"b", "c"}
	expectWarnings(t, findWarnings(in), []warning{
		{Severity: severityWarning, Kind: kindOPAFailures},
		{Severity: severityWarning, Kind: kindCatalogNotInMetabase, Catalog: "b"},
		{Severity: severityNote, Kind: kindRuleNotInMetabase, Catalog: "c", Key: "c.s.a"},
		{Severity: severityNote, Kind: kindRuleNotInMetabase, Catalog: "c", Key: "c.s.b"},
		{Severity: severityWarning, Kind: kindRuleUnknownGroup, Catalog: "c", Key: "c.s.a"},
		{Severity: severityWarning, Kind: kindRuleUnknownGroup, Catalog: "c", Key: "c.s.b"},
	})
}

// The compose stack's rules (opa/rules.rego), with its Metabase, warn only that openapi isn't in Metabase.
func TestCompose(t *testing.T) {
	fresh := testNow.Add(-10 * time.Second)
	columns := func(names ...string) []schemaColumn {
		var cs []schemaColumn
		for _, name := range names {
			cs = append(cs, schemaColumn{Name: name, Type: "varchar"})
		}
		return cs
	}
	in := warningInput{
		rules: map[string][]string{
			"example_jdbc":                              {"Analysts"},
			"openapi":                                   {"Analysts"},
			"example_jdbc.public.schools":               {"Analysts", "Teachers"},
			"example_jdbc.public.courses":               {"Analysts", "Teachers"},
			"example_jdbc.public.departments":           {"Analysts", "Teachers"},
			"example_jdbc.public.staff":                 {"Analysts", "Teachers"},
			"example_jdbc.public.staff.phone":           {"Analysts"},
			"example_jdbc.public.students":              {"Analysts", "Teachers"},
			"example_jdbc.public.students.first_name":   {"Analysts"},
			"example_jdbc.public.students.last_name":    {"Analysts"},
			"example_jdbc.public.students.email":        {"Analysts"},
			"example_jdbc.public.student_courses":       {"Analysts", "Teachers"},
			"example_jdbc.public.student_courses.grade": {"Analysts"},
		},
		// As deciding_keys answers for each key's parent path.
		parentKeys: map[string]string{
			"example_jdbc.public.schools":               "example_jdbc",
			"example_jdbc.public.courses":               "example_jdbc",
			"example_jdbc.public.departments":           "example_jdbc",
			"example_jdbc.public.staff":                 "example_jdbc",
			"example_jdbc.public.staff.phone":           "example_jdbc.public.staff",
			"example_jdbc.public.students":              "example_jdbc",
			"example_jdbc.public.students.first_name":   "example_jdbc.public.students",
			"example_jdbc.public.students.last_name":    "example_jdbc.public.students",
			"example_jdbc.public.students.email":        "example_jdbc.public.students",
			"example_jdbc.public.student_courses":       "example_jdbc",
			"example_jdbc.public.student_courses.grade": "example_jdbc.public.student_courses",
		},
		fileGroups:    []string{"Administrators", "Analysts", "Teachers"},
		trinoCatalogs: []string{"example_jdbc", "openapi"},
		schema: schemaFile{
			Connections: []metabaseConnection{{ID: 1, Name: "trino", Catalog: "example_jdbc",
				Address: "trino-reverse-proxy:9090", ViaProxy: true}},
			Catalogs: map[string]map[string]map[string][]schemaColumn{"example_jdbc": {"public": {
				"schools":         columns("school_id", "name"),
				"courses":         columns("course_id", "title"),
				"departments":     columns("department_id", "name"),
				"staff":           columns("staff_id", "phone"),
				"students":        columns("student_id", "first_name", "last_name", "email"),
				"student_courses": columns("student_id", "course_id", "grade"),
			}}},
		},
		status:   &syncStatus{IntervalSeconds: 30, Files: map[string]*fileStatus{"group": {}, "schema": {}}},
		syncedAt: map[string]*time.Time{"group": &fresh, "schema": &fresh},
		now:      testNow,
	}
	expectWarnings(t, findWarnings(in), []warning{
		{Severity: severityWarning, Kind: kindCatalogNotInMetabase, Catalog: "openapi"},
	}, "openapi is a Trino catalog, but no Metabase database connects to it through the reverse proxy")
}

func TestReadableDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{time.Second, "1 second"},
		{90 * time.Second, "90 seconds"},
		{5 * time.Minute, "5 minutes"},
		{3 * time.Hour, "3 hours"},
		{50 * time.Hour, "2 days"},
	}
	for _, tt := range tests {
		if got := readableDuration(tt.d); got != tt.want {
			t.Errorf("readableDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}
