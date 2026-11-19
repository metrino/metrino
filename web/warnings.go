package main

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// The admin page's warnings: things about the rules, Metabase's connections and the syncs that an administrator
// should fix, or at least know, because the page can't show them in its table of who reads what.
//
// The rule checks compare the rules' group lists, but which key decides a rule's parent comes from OPA
// (parentKeys), as every other decision on the page does.

// A warning's severity. A warning is something wrong or missing; a note is worth knowing but may be intended.
const (
	severityWarning = "warning"
	severityNote    = "note"
)

// The kinds of warning, each explained where findWarnings adds it.
const (
	kindSyncFailing               = "sync-failing"
	kindSyncStale                 = "sync-stale"
	kindRulesCustom               = "rules-custom"
	kindOPAFailures               = "opa-failures"
	kindSchemaOldFormat           = "schema-old-format"
	kindCatalogNotInMetabase      = "catalog-not-in-metabase"
	kindCatalogNotInTrino         = "catalog-not-in-trino"
	kindDuplicateConnections      = "duplicate-connections"
	kindConnectionBypassesProxy   = "connection-bypasses-proxy"
	kindConnectionWithoutCatalog  = "connection-without-catalog"
	kindConnectionExplicitPrepare = "connection-explicit-prepare"
	kindRuleUnknownCatalog        = "rule-unknown-catalog"
	kindRuleUnknownGroup          = "rule-unknown-group"
	kindRuleNarrows               = "rule-narrows"
	kindRuleColumnWithoutTable    = "rule-column-without-table"
	kindRuleNotInMetabase         = "rule-not-in-metabase"
	kindRuleTooManyParts          = "rule-too-many-parts"
	kindCatalogWithoutRules       = "catalog-without-rules"
	kindNameWithDot               = "name-with-dot"
)

// maxKeyParts is the most names a rules key can have: catalog.schema.table.column. The policy never matches a
// longer key, since no name it matches may contain "." (plain in policy.rego).
const maxKeyParts = 4

// systemCatalog is Trino's built-in catalog. It is in every Trino, so it needn't be in TRINO_CATALOGS, and holds
// no data, so it needn't be in Metabase or have rules.
const systemCatalog = "system"

// warning is one thing the page warns about. Catalog is the catalog it is about, and Key the rules key, each
// empty if it isn't about one; the page shows a warning with a catalog in that catalog's section.
type warning struct {
	Severity string `json:"severity"`
	Kind     string `json:"kind"`
	Message  string `json:"message"`
	Catalog  string `json:"catalog,omitempty"`
	Key      string `json:"key,omitempty"`
}

// warningInput is everything findWarnings looks at.
type warningInput struct {
	rules       map[string][]string // nil when rulesCustom
	rulesCustom bool                // the rules document isn't {key: [groups]}
	// parentKeys is, for each rules key of two or more parts, the key OPA says decides the path without the
	// key's last part, or "" if none does. nil if OPA couldn't say; a key that is missing is skipped too.
	parentKeys    map[string]string
	fileGroups    []string // the group file's groups
	trinoCatalogs []string // nil when unknown, which skips the checks that need them
	schema        schemaFile
	// schemaOldFormat is the error for a schema file from an older sync, which the page went on without; ""
	// if the file was fine or missing.
	schemaOldFormat string
	status          *syncStatus // nil when there is no status file
	// syncedAt is when each file the page reads last synced, from its modification time; nil if never.
	syncedAt    map[string]*time.Time
	now         time.Time
	opaFailures int // how many of the page's lookups in OPA failed
}

// findWarnings returns what the page warns about, sorted by catalog (those with none first), then kind, then
// key.
func findWarnings(in warningInput) []warning {
	f := &finder{in: in, warnings: []warning{}}
	f.sync()
	f.opa()
	f.schemaFormat()
	f.dottedNames()
	f.catalogs()
	f.connections()
	if in.rulesCustom {
		f.add(severityWarning, kindRulesCustom, "", "",
			"OPA's rules document isn't the standard {key: [groups]} form, so the page doesn't list or check the rules, or say which rule decides each table.")
	} else {
		f.rules()
	}
	// Stable, so that warnings alike in all three keep the order they were found in, which is by file name,
	// connection ID or group.
	slices.SortStableFunc(f.warnings, func(a, b warning) int {
		return cmp.Or(cmp.Compare(a.Catalog, b.Catalog), cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Key, b.Key))
	})
	return f.warnings
}

type finder struct {
	in       warningInput
	warnings []warning
}

func (f *finder) add(severity, kind, catalog, key, format string, args ...any) {
	f.warnings = append(f.warnings, warning{
		Severity: severity, Kind: kind, Catalog: catalog, Key: key, Message: fmt.Sprintf(format, args...),
	})
}

// syncedFiles names the files in the status file as the page does.
var syncedFiles = map[string]string{"group": "the groups", "schema": "the column list"}

// sync warns about each file the status file has whose last sync failed, which has never synced, or which
// hasn't synced for three intervals, which means the sync has stopped or is failing. Without a status file
// there are no warnings: a deployment may run the sync without one, and the page still shows the sync times.
// A file the status file doesn't have isn't checked, since a sync may not write it, and neither is one the
// page doesn't read, whose time it doesn't know.
func (f *finder) sync() {
	status := f.in.status
	if status == nil {
		return
	}
	interval := time.Duration(status.IntervalSeconds) * time.Second
	names := slices.Sorted(maps.Keys(status.Files))
	for _, name := range names {
		file := status.Files[name]
		syncedAt, read := f.in.syncedAt[name]
		if file == nil || !read {
			continue
		}
		label := cmp.Or(syncedFiles[name], name)
		switch {
		case file.LastError != "":
			f.add(severityWarning, kindSyncFailing, "", "", "Syncing %s from Metabase is failing: %s", label, file.LastError)
		case syncedAt == nil:
			f.add(severityWarning, kindSyncFailing, "", "", "Syncing %s from Metabase has never succeeded.", label)
		}
		if syncedAt == nil || interval <= 0 {
			continue
		}
		if age := f.in.now.Sub(*syncedAt); age > 3*interval {
			f.add(severityWarning, kindSyncStale, "", "",
				"%s last synced from Metabase %s ago, though the sync runs every %s; it may have stopped.",
				capitalise(label), readableDuration(age), readableDuration(interval))
		}
	}
}

func (f *finder) opa() {
	if n := f.in.opaFailures; n > 0 {
		f.add(severityWarning, kindOPAFailures, "", "", "%s in OPA failed; cells marked ⚠ are unknown.",
			plural(n, "lookup"))
	}
}

// schemaFormat warns when the schema file was from an older sync, whose catalogs the page can't tell from an
// empty schema. Without it the page has no tables, columns or connections.
func (f *finder) schemaFormat() {
	if f.in.schemaOldFormat != "" {
		f.add(severityWarning, kindSchemaOldFormat, "", "",
			"%s. The column list and connections are unavailable until the Metabase sync image is updated.",
			f.in.schemaOldFormat)
	}
}

// inTrino says whether catalog is one of Trino's. It is only meaningful when trinoCatalogs is known.
func (in *warningInput) inTrino(catalog string) bool {
	return catalogInTrino(in.trinoCatalogs, catalog)
}

// proxied returns the connections that go through the reverse proxy, and so get Trino's rules.
func (in *warningInput) proxied() []metabaseConnection {
	return slices.DeleteFunc(slices.Clone(in.schema.Connections), func(c metabaseConnection) bool { return !c.ViaProxy })
}

// inMetabase says whether a connection through the proxy uses catalog.
func (in *warningInput) inMetabase(catalog string) bool {
	return slices.ContainsFunc(in.proxied(), func(c metabaseConnection) bool { return c.Catalog == catalog })
}

// catalogs compares Trino's catalogs with Metabase's, when Trino's are known.
func (f *finder) catalogs() {
	if f.in.trinoCatalogs == nil {
		return
	}
	for _, catalog := range f.in.trinoCatalogs {
		if catalog != systemCatalog && !f.in.inMetabase(catalog) {
			f.add(severityWarning, kindCatalogNotInMetabase, catalog, "",
				"%s is a Trino catalog, but no Metabase database connects to it through the reverse proxy, so people can't reach it in Metabase.",
				catalog)
		}
	}
	for _, c := range f.in.proxied() {
		if c.Catalog != "" && !f.in.inTrino(c.Catalog) {
			f.add(severityWarning, kindCatalogNotInTrino, c.Catalog, "",
				"Metabase database %s uses catalog %s, which isn't one of Trino's catalogs, so its queries fail.",
				connectionName(c), c.Catalog)
		}
	}
}

// connections warns about Metabase connections whose tables the page can't list, or lists from only one of
// them.
func (f *finder) connections() {
	connections := slices.SortedFunc(slices.Values(f.in.schema.Connections), func(a, b metabaseConnection) int {
		return cmp.Compare(a.ID, b.ID)
	})
	byCatalog := map[string][]string{}
	for _, c := range connections {
		if c.ViaProxy && c.Catalog != "" {
			byCatalog[c.Catalog] = append(byCatalog[c.Catalog], connectionName(c))
		}
		if !c.ViaProxy {
			f.add(severityWarning, kindConnectionBypassesProxy, c.Catalog, "",
				"Metabase database %s connects to Trino at %s, not through the reverse proxy, so queries run as its own Trino user, not the signed-in person; its tables aren't listed.",
				connectionName(c), cmp.Or(c.Address, "an unknown address"))
		}
		if c.Catalog == "" {
			f.add(severityWarning, kindConnectionWithoutCatalog, "", "",
				"Metabase database %s has no Trino catalog set, so its tables aren't listed.", connectionName(c))
		}
		// Through the proxy only: a connection that bypasses it is warned about already. Unknown (nil) isn't
		// warned about, since a file from an older sync doesn't say.
		if c.ViaProxy && c.OptimizedPrepare != nil && !*c.OptimizedPrepare {
			f.add(severityWarning, kindConnectionExplicitPrepare, c.Catalog, "",
				`Metabase database %s has "Optimized prepared statements" off, so its queries with parameters (dashboard filters and SQL variables) fail: the reverse proxy can't tell who ran them. Turn it on in the database's settings in Metabase.`,
				connectionName(c))
		}
	}
	for _, catalog := range slices.Sorted(maps.Keys(byCatalog)) {
		names := byCatalog[catalog]
		if len(names) < 2 {
			continue
		}
		f.add(severityWarning, kindDuplicateConnections, catalog, "",
			"Metabase databases %s connect to %s through the reverse proxy; the columns shown are from %s, the lowest ID.",
			listOf(names), catalog, names[0])
	}
}

// dottedNames warns about each table that has a "." in its schema's or its own name, or in a column's. A rules
// key joins names with ".", so it can't name them, and the policy lets only Administrators read them.
func (f *finder) dottedNames() {
	for _, catalog := range slices.Sorted(maps.Keys(f.in.schema.Catalogs)) {
		schemas := f.in.schema.Catalogs[catalog]
		for _, schemaName := range slices.Sorted(maps.Keys(schemas)) {
			for _, name := range slices.Sorted(maps.Keys(schemas[schemaName])) {
				table := displayPath(catalog, schemaName, name)
				if strings.Contains(schemaName, ".") || strings.Contains(name, ".") {
					f.add(severityWarning, kindNameWithDot, catalog, "",
						`Table %s has a "." in its name, which no rules key can name, so only Administrators can read it. Rename it to share it.`,
						table)
					continue
				}
				var dotted []string
				for _, c := range schemas[schemaName][name] {
					if strings.Contains(c.Name, ".") {
						dotted = append(dotted, displayPath(c.Name))
					}
				}
				if n := len(dotted); n > 0 {
					f.add(severityWarning, kindNameWithDot, catalog, "",
						`%s %s of table %s %s a "." in the name, which no rules key can name, so everyone but Administrators gets NULL. Rename %s to share %s.`,
						pick(n, "Column", "Columns"), listOf(dotted), table, pick(n, "has", "have"),
						pick(n, "it", "them"), pick(n, "it", "them"))
				}
			}
		}
	}
}

// rules checks each rules key, in order.
func (f *finder) rules() {
	ruled := map[string]bool{}
	for _, key := range slices.Sorted(maps.Keys(f.in.rules)) {
		groups := f.in.rules[key]
		parts := strings.Split(key, ".")
		catalog := parts[0]
		if len(parts) > maxKeyParts {
			f.add(severityWarning, kindRuleTooManyParts, catalog, key,
				`Rule %s has %d parts, but a key names at most catalog.schema.table.column, and none of the names can contain "."; it grants nothing.`,
				key, len(parts))
			continue
		}
		ruled[catalog] = true

		if f.in.trinoCatalogs != nil && !f.in.inTrino(catalog) {
			f.add(severityWarning, kindRuleUnknownCatalog, catalog, key,
				"Rule %s names catalog %s, which isn't one of Trino's catalogs, so it grants nothing.", key, catalog)
		}
		for _, group := range groups {
			if !slices.Contains(f.in.fileGroups, group) {
				f.add(severityWarning, kindRuleUnknownGroup, catalog, key,
					"Rule %s lists %s, but no active Metabase user is in %s, or it doesn't exist; this grants nothing.",
					key, group, group)
			}
		}
		f.parent(key, parts, groups)
		if f.in.inMetabase(catalog) && len(parts) > 1 {
			if missing := f.missingFromSchema(parts); missing != "" {
				f.add(severityNote, kindRuleNotInMetabase, catalog, key,
					"Rule %s names %s, which Metabase hasn't found in %s. Check the spelling, or wait for Metabase's next scan.",
					key, missing, catalog)
			}
		}
	}
	if f.in.trinoCatalogs == nil {
		return
	}
	for _, catalog := range f.in.trinoCatalogs {
		if catalog != systemCatalog && !ruled[catalog] {
			f.add(severityNote, kindCatalogWithoutRules, catalog, "",
				"No rule names %s, so only Administrators can read it.", catalog)
		}
	}
}

// parent compares a rule's groups with those of the key that decides its parent. A schema or table rule that
// leaves out a group its parent lists takes that group's access away, which may not be meant. A column rule
// that lists a group its table's rule doesn't grants nothing, since reading a column needs its table.
func (f *finder) parent(key string, parts []string, groups []string) {
	parentKey, ok := f.in.parentKeys[key]
	if !ok {
		return
	}
	parentGroups := f.in.rules[parentKey] // nil if parentKey is ""
	switch level := ruleLevel(key); level {
	case "schema", "table":
		for _, group := range parentGroups {
			if !slices.Contains(groups, group) {
				f.add(severityWarning, kindRuleNarrows, parts[0], key,
					"Rule %s doesn't list %s, though %s does, so %s can't read this %s.",
					key, group, parentKey, group, level)
			}
		}
	case "column":
		for _, group := range groups {
			if slices.Contains(parentGroups, group) {
				continue
			}
			why := fmt.Sprintf("%s doesn't", parentKey)
			if parentKey == "" {
				why = "no rule covers its table"
			}
			f.add(severityWarning, kindRuleColumnWithoutTable, parts[0], key,
				"Rule %s lists %s, but %s, so %s can't read the table and this grants nothing.",
				key, group, why, group)
		}
	}
}

// missingFromSchema returns what a rules key of two to four parts names that the schema file lacks, such as
// "table public.students", or "" if it has it all.
func (f *finder) missingFromSchema(parts []string) string {
	schemas := f.in.schema.Catalogs[parts[0]]
	tables, ok := schemas[parts[1]]
	if !ok {
		return "schema " + parts[1]
	}
	if len(parts) == 2 {
		return ""
	}
	columns, ok := tables[parts[2]]
	if !ok {
		return "table " + strings.Join(parts[1:3], ".")
	}
	if len(parts) == 3 {
		return ""
	}
	if !slices.ContainsFunc(columns, func(c schemaColumn) bool { return c.Name == parts[3] }) {
		return "column " + strings.Join(parts[1:4], ".")
	}
	return ""
}

// ruleLevel is what a rules key names, by how many parts it has: "catalog", "schema", "table" or "column", or
// "unusable" for a key of more than four parts, which matches nothing.
func ruleLevel(key string) string {
	switch strings.Count(key, ".") {
	case 0:
		return "catalog"
	case 1:
		return "schema"
	case 2:
		return "table"
	case 3:
		return "column"
	default:
		return "unusable"
	}
}

// parentPath returns the path whose rule a rules key narrows: the key's names without the last. ok is false
// for a catalog's key, which has no parent, and for a key of more than four parts, which matches nothing.
func parentPath(key string) (path []string, ok bool) {
	parts := strings.Split(key, ".")
	if len(parts) < 2 || len(parts) > maxKeyParts {
		return nil, false
	}
	return parts[:len(parts)-1], true
}

// displayPath joins names with ".", quoting a name that contains one, as Trino's SQL would: c.s."t.a".
func displayPath(names ...string) string {
	shown := make([]string, len(names))
	for i, name := range names {
		shown[i] = name
		if strings.Contains(name, ".") {
			shown[i] = `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
		}
	}
	return strings.Join(shown, ".")
}

func connectionName(c metabaseConnection) string {
	return fmt.Sprintf("#%d %s", c.ID, c.Name)
}

// listOf joins items as "a, b and c".
func listOf(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// readableDuration is d in whole seconds, minutes, hours or days, whichever reads best.
func readableDuration(d time.Duration) string {
	switch {
	case d < 2*time.Minute:
		return plural(int(d/time.Second), "second")
	case d < 2*time.Hour:
		return plural(int(d/time.Minute), "minute")
	case d < 48*time.Hour:
		return plural(int(d/time.Hour), "hour")
	default:
		return plural(int(d/(24*time.Hour)), "day")
	}
}

// pick returns one if n is 1, and other otherwise.
func pick(n int, one, other string) string {
	if n == 1 {
		return one
	}
	return other
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
