package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// The admin page's table of who can read what. For each group, it asks the web pod's OPA, which runs Trino's
// policy, the same questions Trino asks for a query. Each table and column gets one of four states per group:
//
//   - read: a table the group may SelectFromColumns on, or one of its columns with no mask.
//   - masked: a column of a table the group can read that has a mask, which Trino returns as NULL. Only columns
//     are masked.
//   - denied: a table the group can't read, and all its columns.
//   - error: a lookup failed, so the page doesn't know. It is never shown as read or denied.
//
// Asking OPA, rather than reading the rules here, means the page can't disagree with Trino. For the same reason,
// the rules key that decided each table and column comes from OPA too (deciding_keys in the policy).
//
// There is a request for every group and table, so they go to OPA several at a time, and the handler gives them
// all one deadline: a slow OPA makes the cells it hasn't answered errors, rather than hold the page.
//
// Administrators read everything, so the page leaves them out. A person in several groups reads what any one
// of them can.

// pageUser is the user in the requests: no one in particular. The policy decides from the groups, except for
// Metabase's connection user, "trino", which this must not be.
const pageUser = "web-admin-page"

// placeholderType stands in for a column type the schema file doesn't have. The policy writes the type into
// the mask's expression, and leaves out the mask of a column without one, so that column would look readable.
// Only the expression depends on the type, and the page only asks whether there is a mask.
const placeholderType = "varchar"

// maxOPAAnswer bounds what is read from OPA. The rules and the answers about one table or group are a few
// kilobytes.
const maxOPAAnswer = 1 << 20

// maxDecidingKeysAnswer bounds deciding_keys's answer, which has an entry for every table and column in the
// schema and passes maxOPAAnswer at a few tens of thousands of columns. Cutting it off would fail the lookup,
// and the page would say "unknown" in every row, so the limit is far above any real schema. OPA is on
// loopback, and the answer is read once per request.
const maxDecidingKeysAnswer = 64 << 20

// opaConcurrency is how many requests the page has in flight to OPA at once.
const opaConcurrency = 8

// accessTimeout is how long the handler gives OPA to answer all of a page's requests.
const accessTimeout = 20 * time.Second

// The states of a cell, explained at the top of this file.
const (
	stateRead   = "read"
	stateMasked = "masked"
	stateDenied = "denied"
	stateError  = "error"
)

// cell is what one group can do with one table or column. Error says why a lookup failed.
type cell struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

// tableAccess is a table, with each group's cell, keyed by group. DecidedBy is the rules key that decides it,
// or "" if none does or OPA couldn't say. Metabase is its IDs in Metabase, nil if the schema file doesn't have
// them, and the same goes for a column's MetabaseField. Description is what an admin wrote about it in Metabase.
type tableAccess struct {
	Catalog     string         `json:"catalog"`
	Schema      string         `json:"schema"`
	Name        string         `json:"name"`
	DecidedBy   string         `json:"decidedBy"`
	Metabase    *metabaseTable `json:"metabase,omitempty"`
	Description string         `json:"description,omitempty"`
	// HiddenInMetabase is whether an admin hid the table in Metabase, where people then can't browse it or start a
	// question from it. Trino's rules still decide who reads it, as the cells say.
	HiddenInMetabase bool            `json:"hiddenInMetabase,omitempty"`
	Access           map[string]cell `json:"access"`
	Columns          []columnAccess  `json:"columns"`
}

type metabaseTable struct {
	Database int `json:"database"`
	Table    int `json:"table"`
}

type columnAccess struct {
	Name          string          `json:"name"`
	Type          string          `json:"type"`
	DecidedBy     string          `json:"decidedBy"`
	MetabaseField int             `json:"metabaseField,omitempty"`
	Description   string          `json:"description,omitempty"`
	Access        map[string]cell `json:"access"`
}

func (t *tableAccess) path() []string {
	return []string{t.Catalog, t.Schema, t.Name}
}

// pageGroups is the groups the page shows: fileGroups (the group file's) and those rules names, without
// Administrators. rules is nil for a deployment whose policy keeps its rules in another shape; then only the
// group file's groups are shown.
func pageGroups(fileGroups []string, rules map[string][]string) []string {
	groups := slices.Clone(fileGroups)
	for _, ruleGroups := range rules {
		groups = append(groups, ruleGroups...)
	}
	groups = slices.DeleteFunc(groups, func(group string) bool { return group == adminGroup })
	slices.Sort(groups)
	return slices.Compact(groups)
}

// sortedTables returns every table in catalogs, sorted, with its columns in the schema file's order.
func sortedTables(catalogs map[string]map[string]map[string][]schemaColumn) []tableAccess {
	tables := []tableAccess{}
	for catalog, schemas := range catalogs {
		for schemaName, schemaTables := range schemas {
			for name, columns := range schemaTables {
				table := tableAccess{Catalog: catalog, Schema: schemaName, Name: name, Columns: []columnAccess{}}
				for _, c := range columns {
					table.Columns = append(table.Columns, columnAccess{Name: c.Name, Type: c.Type})
				}
				tables = append(tables, table)
			}
		}
	}
	slices.SortFunc(tables, func(a, b tableAccess) int {
		return cmp.Or(cmp.Compare(a.Catalog, b.Catalog), cmp.Compare(a.Schema, b.Schema), cmp.Compare(a.Name, b.Name))
	})
	return tables
}

// schemaPaths returns the path of every table and column in catalogs, in the page's order, to ask deciding_keys
// about.
func schemaPaths(catalogs map[string]map[string]map[string][]schemaColumn) [][]string {
	paths := [][]string{}
	for _, table := range sortedTables(catalogs) {
		paths = append(paths, table.path())
		for _, c := range table.Columns {
			paths = append(paths, append(table.path(), c.Name))
		}
	}
	return paths
}

// access returns every table in catalogs, sorted, with what each group can do with it and each of its columns,
// and how many OPA lookups failed. decided is deciding_keys's answer for schemaPaths, and may be nil.
func access(ctx context.Context, o opa, groups []string, catalogs map[string]map[string]map[string][]schemaColumn, decided decidedKeys) (tables []tableAccess, failures int) {
	tables = sortedTables(catalogs)
	slots := make(chan struct{}, opaConcurrency)
	var failed atomic.Int64
	// Each group's goroutine writes only its own results, which are copied into the tables once all are done.
	results := make([][]tableCells, len(groups))
	var wg sync.WaitGroup
	for i, group := range groups {
		wg.Go(func() { results[i] = o.groupAccess(ctx, slots, &failed, group, tables) })
	}
	wg.Wait()

	for i := range tables {
		table := &tables[i]
		table.DecidedBy = decided.of(table.path())
		table.Access = map[string]cell{}
		for g, group := range groups {
			table.Access[group] = results[g][i].table
		}
		for j := range table.Columns {
			c := &table.Columns[j]
			c.DecidedBy = decided.of(append(table.path(), c.Name))
			c.Access = map[string]cell{}
			for g, group := range groups {
				c.Access[group] = results[g][i].columns[j]
			}
		}
	}
	return tables, int(failed.Load())
}

// tableCells is one group's cells for a table and its columns.
type tableCells struct {
	table   cell
	columns []cell
}

// groupAccess asks OPA what group can do with each of tables: first whether it can read each table, several at
// a time, then which columns of those it can read are masked, in one batchColumnMasks request, as Trino asks
// for a query. It holds one of slots for each request, and adds each failed lookup to failed (one that gave up waiting for a slot counts too).
func (o opa) groupAccess(ctx context.Context, slots chan struct{}, failed *atomic.Int64, group string, tables []tableAccess) []tableCells {
	cells := make([]tableCells, len(tables))
	// set gives table i's cell, and its columns the same one.
	set := func(i int, c cell) {
		cells[i].table = c
		cells[i].columns = make([]cell, len(tables[i].Columns))
		for j := range cells[i].columns {
			cells[i].columns[j] = c
		}
	}

	var wg sync.WaitGroup
	for i := range tables {
		if err := acquire(ctx, slots); err != nil {
			failed.Add(1)
			set(i, errorCell(ctx, err))
			continue
		}
		wg.Go(func() {
			defer release(slots)
			allowed, err := o.canSelect(ctx, group, &tables[i])
			switch {
			case err != nil:
				failed.Add(1)
				set(i, errorCell(ctx, err))
			case allowed:
				set(i, cell{State: stateRead}) // until the masks say otherwise
			default:
				set(i, cell{State: stateDenied})
			}
		})
	}
	wg.Wait()

	// The columns of the tables this group can read.
	var resources []any
	var readable []*cell
	for i := range tables {
		if cells[i].table.State != stateRead {
			continue
		}
		table := &tables[i]
		for j, c := range table.Columns {
			resources = append(resources, map[string]any{"column": map[string]any{
				"catalogName": table.Catalog, "schemaName": table.Schema, "tableName": table.Name,
				"columnName": c.Name, "columnType": cmp.Or(c.Type, placeholderType),
			}})
			readable = append(readable, &cells[i].columns[j])
		}
	}
	if len(resources) == 0 {
		return cells
	}
	err := acquire(ctx, slots)
	var masked map[int]bool
	if err == nil {
		masked, err = o.maskedColumns(ctx, group, resources)
		release(slots)
	}
	if err != nil {
		failed.Add(1)
	}
	for i, c := range readable {
		switch {
		case err != nil:
			*c = errorCell(ctx, err)
		case masked[i]:
			*c = cell{State: stateMasked}
		}
	}
	return cells
}

// acquire takes one of slots, waiting until one is free or ctx is done.
func acquire(ctx context.Context, slots chan struct{}) error {
	select {
	case slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func release(slots chan struct{}) {
	<-slots
}

// errorCell is the cell of a failed lookup. A request cut off by the deadline fails with an error about the
// request, which doesn't say why, so this does.
func errorCell(ctx context.Context, err error) cell {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return cell{State: stateError, Error: "OPA didn't answer in time"}
	}
	return cell{State: stateError, Error: err.Error()}
}

// opa asks the web pod's OPA about Trino's policy.
type opa struct {
	url    string
	client *http.Client
}

// rules returns the "rules" document, the per-deployment data rules, or {} if the policy has none.
func (o opa) rules(ctx context.Context) (json.RawMessage, error) {
	var rules json.RawMessage
	defined, err := o.ask(ctx, http.MethodGet, "rules", nil, &rules, maxOPAAnswer)
	if err != nil || !defined {
		return json.RawMessage("{}"), err
	}
	return rules, nil
}

// decidedKeys is deciding_keys's answer: the rules key that decides each path asked about, by pathID.
type decidedKeys map[string]string

// of returns the key that decides path, or "" if none does or it wasn't asked about.
func (d decidedKeys) of(path []string) string {
	return d[pathID(path)]
}

// pathID identifies a path (a list of names from the catalog down) as a map key. Joining the names with "."
// would make a table "t.a" and column a of table t the same; JSON keeps them apart.
func pathID(path []string) string {
	id, _ := json.Marshal(path) // a []string always encodes
	return string(id)
}

// decidingKeys asks which rules key decides each of paths (lists of names from the catalog down). A path no
// key covers is left out of keys. ok is false if the policy has no deciding_keys, as a deployment's own policy
// may not.
func (o opa) decidingKeys(ctx context.Context, paths [][]string) (keys decidedKeys, ok bool, err error) {
	if paths == nil {
		paths = [][]string{} // null would make OPA's answer undefined, not empty
	}
	// In the order of paths, with null for a path no key covers.
	var answer []*string
	defined, err := o.ask(ctx, http.MethodPost, "deciding_keys", map[string]any{"paths": paths}, &answer, maxDecidingKeysAnswer)
	if err != nil || !defined {
		return nil, false, err
	}
	if len(answer) != len(paths) {
		return nil, false, fmt.Errorf("OPA answered deciding_keys for %d paths, asked about %d", len(answer), len(paths))
	}
	keys = decidedKeys{}
	for i, key := range answer {
		if key != nil {
			keys[pathID(paths[i])] = *key
		}
	}
	return keys, true, nil
}

// canSelect asks whether a member of group may read table, as Trino asks for each table a query reads.
func (o opa) canSelect(ctx context.Context, group string, table *tableAccess) (bool, error) {
	columns := []string{}
	for _, c := range table.Columns {
		columns = append(columns, c.Name)
	}
	var allowed bool
	_, err := o.ask(ctx, http.MethodPost, "allow", trinoRequest(group, map[string]any{
		"operation": "SelectFromColumns",
		"resource": map[string]any{"table": map[string]any{
			"catalogName": table.Catalog, "schemaName": table.Schema, "tableName": table.Name, "columns": columns,
		}},
	}), &allowed, maxOPAAnswer)
	return allowed, err // undefined is false, as Trino reads it
}

// maskedColumns asks which of resources (columns, as Trino sends them) a member of group gets masked, and
// returns their indexes. Trino fails a query on an undefined answer, so this fails too, rather than show
// every column as readable.
func (o opa) maskedColumns(ctx context.Context, group string, resources []any) (map[int]bool, error) {
	var masks []struct {
		Index int `json:"index"`
	}
	defined, err := o.ask(ctx, http.MethodPost, "batchColumnMasks", trinoRequest(group, map[string]any{
		"operation":       "GetColumnMask",
		"filterResources": resources,
	}), &masks, maxOPAAnswer)
	if err != nil {
		return nil, err
	}
	if !defined {
		return nil, errors.New("OPA has no batchColumnMasks decision")
	}
	masked := map[int]bool{}
	for _, mask := range masks {
		masked[mask.Index] = true
	}
	return masked, nil
}

// trinoRequest is the input of a request like Trino's, from pageUser in group alone.
func trinoRequest(group string, action map[string]any) map[string]any {
	return map[string]any{
		"context": map[string]any{
			"identity":      map[string]any{"user": pageUser, "groups": []string{group}},
			"softwareStack": map[string]any{"trinoVersion": "web-admin-page"},
		},
		"action": action,
	}
}

// ask reads the document or decision data.trino.<name>, with input if it isn't nil, into result, reading at
// most limit bytes of OPA's answer. defined is false if OPA has no value for it.
func (o opa) ask(ctx context.Context, method, name string, input any, result any, limit int64) (defined bool, err error) {
	var body io.Reader
	if input != nil {
		text, err := json.Marshal(map[string]any{"input": input})
		if err != nil {
			return false, err
		}
		body = bytes.NewReader(text)
	}
	req, err := http.NewRequestWithContext(ctx, method, o.url+"/v1/data/trino/"+name, body)
	if err != nil {
		return false, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("OPA answered %s for %s", resp.Status, name)
	}
	var answer struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, limit)).Decode(&answer); err != nil {
		return false, fmt.Errorf("reading OPA's answer for %s: %w", name, err)
	}
	if answer.Result == nil {
		return false, nil
	}
	if err := json.Unmarshal(answer.Result, result); err != nil {
		return false, fmt.Errorf("reading OPA's answer for %s: %w", name, err)
	}
	return true, nil
}

// withMetabase sets each table's and column's Metabase IDs and description, where the schema file has them.
func withMetabase(tables []tableAccess, metabase map[string]map[string]map[string]metabaseTableInfo) {
	for i := range tables {
		t := &tables[i]
		info, ok := metabase[t.Catalog][t.Schema][t.Name]
		if !ok {
			continue
		}
		t.Metabase = &metabaseTable{Database: info.Database, Table: info.Table}
		t.Description = info.Description
		t.HiddenInMetabase = info.Hidden
		for j := range t.Columns {
			field := info.Fields[t.Columns[j].Name]
			t.Columns[j].MetabaseField = field.ID
			t.Columns[j].Description = field.Description
		}
	}
}
