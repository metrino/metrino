package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/metrino/metrino/trino/identity/metabase"
)

// The schema file tells the web server's admin page which Trino connections Metabase has and what tables and
// columns it knows through them, for the page to ask the web pod's OPA who can read each one. It's JSON:
//
//	{"connections": [{"id": 1, "name": "trino", "catalog": "example_jdbc", "address": "trino-reverse-proxy:9090",
//	                  "viaProxy": true, "optimizedPrepare": true, "lastScan": "2026-11-08T10:00:00Z"}, ...],
//	 "catalogs": {"example_jdbc": {"public": {"students": [{"name": "student_id", "type": "bigint"}, ...]}}},
//	 "metabase": {"example_jdbc": {"public": {"students": {"database": 1, "table": 4, "description": "...",
//	                                                       "fields": {"student_id": {"id": 16}, ...}}}}}}
//
// connections lists every Trino database in Metabase (the catalog is "" if it has none), with the address
// Metabase connects to, whether that is the reverse proxy (the address is one of TRINO_PROXY_ADDRESSES), whether
// it has "Optimized prepared statements" on, and when Metabase last scanned it successfully (left out if
// unknown). catalogs is catalog → schema → table → the
// table's columns in the table's order, each with its name and Trino type. It only has tables from connections
// that use the proxy: through the others Trino sees the connection's own user, not the person using Metabase,
// so the rules don't decide who reads them. metabase has, for each table in catalogs, the IDs Metabase gives it
// and its columns in the connection it was listed from, for the page to link to Metabase's admin pages, the
// descriptions an admin wrote there (left out if none), and "hidden": true if an admin hid the table there. It
// is as current as Metabase's own scan of the metadata (hourly by default), and holds names, types, addresses
// IDs and descriptions only, no data.

// trinoEngine is Metabase's engine name for Trino, whose driver is Starburst's.
const trinoEngine = "starburst"

// schemaFile is the schema file's contents.
type schemaFile struct {
	Connections []connection                               `json:"connections"`
	Catalogs    map[string]map[string]map[string][]column  `json:"catalogs"`
	Metabase    map[string]map[string]map[string]tableInfo `json:"metabase"`
}

// connection is a Trino database in Metabase.
type connection struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Catalog  string `json:"catalog"`
	Address  string `json:"address"`
	ViaProxy bool   `json:"viaProxy"`
	// Whether Metabase sends queries with parameters as one EXECUTE IMMEDIATE (metabase.Database). Always
	// written, so the page can tell "off" from a file by an older sync, which has none.
	OptimizedPrepare bool `json:"optimizedPrepare"`
	// Pointer, so that an unknown time is left out.
	LastScan *time.Time `json:"lastScan,omitempty"`
}

type column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// tableInfo is what Metabase has to say about a table: its database ID, its table ID, its description, whether
// an admin hid it, and its columns' by name.
type tableInfo struct {
	Database    int                  `json:"database"`
	Table       int                  `json:"table"`
	Description string               `json:"description,omitempty"`
	Hidden      bool                 `json:"hidden,omitempty"`
	Fields      map[string]fieldInfo `json:"fields"`
}

type fieldInfo struct {
	ID          int    `json:"id"`
	Description string `json:"description,omitempty"`
}

// syncSchemaOnce writes the schema file from Metabase's Trino databases, if it changed. A run that fails
// writes nothing. proxyAddresses are the host:port spellings of the reverse proxy.
func syncSchemaOnce(client *metabase.Client, path string, proxyAddresses []string) error {
	databases, err := client.Databases()
	if err != nil {
		return err
	}
	tables := map[int][]metabase.Table{}
	for _, database := range databases {
		if !viaProxy(database, proxyAddresses) || database.Catalog == "" {
			continue
		}
		if tables[database.ID], err = client.Tables(database.ID); err != nil {
			return fmt.Errorf("reading the tables of Metabase database %d: %w", database.ID, err)
		}
	}
	// The scan times are extra: the file is still worth writing without them.
	lastScans, err := client.LastScans()
	if err != nil {
		fmt.Println("metabase-sync: couldn't read when Metabase last scanned its databases, leaving that out of "+path+":", err)
	}

	s := toSchema(databases, tables, lastScans, proxyAddresses)
	written, err := writeIfChanged(path, s.contents())
	if err != nil {
		return err
	}
	if written {
		fmt.Println("metabase-sync: wrote", path, "("+s.summary()+")")
	}
	return nil
}

// viaProxy is whether a Trino database connects to the reverse proxy: its address is exactly one of the
// proxy's. Metabase's host and port are text, so a spelling the proxy answers to but isn't listed is not
// recognised.
func viaProxy(database metabase.Database, proxyAddresses []string) bool {
	return database.Engine == trinoEngine && database.Address != "" && slices.Contains(proxyAddresses, database.Address)
}

// toSchema returns the Trino databases as connections, and the active tables of those that use the reverse
// proxy (and have a catalog), with their real columns: active, and not fields Metabase made inside a column
// (nested or unfolded from JSON). If two such databases connect to the same catalog, the one with the lower
// ID wins for a table both have.
func toSchema(databases []metabase.Database, tables map[int][]metabase.Table, lastScans map[int]time.Time, proxyAddresses []string) schemaFile {
	databases = slices.Clone(databases)
	slices.SortFunc(databases, func(a, b metabase.Database) int { return cmp.Compare(a.ID, b.ID) })

	s := schemaFile{
		Connections: []connection{},
		Catalogs:    map[string]map[string]map[string][]column{},
		Metabase:    map[string]map[string]map[string]tableInfo{},
	}
	for _, database := range databases {
		if database.Engine != trinoEngine {
			continue
		}
		c := connection{
			ID:       database.ID,
			Name:     database.Name,
			Catalog:  database.Catalog,
			Address:  database.Address,
			ViaProxy: viaProxy(database, proxyAddresses),
			// The proxy only knows who ran a query with parameters if this is on.
			OptimizedPrepare: database.OptimizedPrepare,
		}
		if scan, ok := lastScans[database.ID]; ok {
			scan = scan.UTC()
			c.LastScan = &scan
		}
		s.Connections = append(s.Connections, c)

		if !c.ViaProxy || database.Catalog == "" {
			continue
		}
		for _, table := range tables[database.ID] {
			if !table.Active {
				continue
			}
			schemaTables := inner(inner(s.Catalogs, database.Catalog), table.Schema)
			if _, ok := schemaTables[table.Name]; ok {
				continue
			}
			info := tableInfo{
				Database: database.ID, Table: table.ID, Description: table.Description,
				Hidden: table.VisibilityType != "", Fields: map[string]fieldInfo{},
			}
			columns := []column{}
			for _, field := range columnsOf(table) {
				columns = append(columns, column{Name: field.Name, Type: field.DatabaseType})
				info.Fields[field.Name] = fieldInfo{ID: field.ID, Description: field.Description}
			}
			schemaTables[table.Name] = columns
			inner(inner(s.Metabase, database.Catalog), table.Schema)[table.Name] = info
		}
	}
	return s
}

// columnsOf returns the table's real columns, in the table's order.
func columnsOf(table metabase.Table) []metabase.Field {
	fields := slices.Clone(table.Fields)
	slices.SortStableFunc(fields, func(a, b metabase.Field) int { return cmp.Compare(a.DatabasePosition, b.DatabasePosition) })
	return slices.DeleteFunc(fields, func(field metabase.Field) bool {
		return !field.Active || field.ParentID != nil || len(field.NfcPath) > 0
	})
}

// inner returns m[key], first adding an empty map there if there is none.
func inner[V any](m map[string]map[string]V, key string) map[string]V {
	if m[key] == nil {
		m[key] = map[string]V{}
	}
	return m[key]
}

// contents is the file's text. Maps are written with their keys sorted, so the same schema is always the same
// text, and writeIfChanged only writes when something changed.
func (s schemaFile) contents() string {
	text, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		panic(err) // only strings, maps and slices: can't fail
	}
	return string(text) + "\n"
}

// summary is e.g. "3 connections, 2 catalogs, 6 tables, 34 columns", for the log.
func (s schemaFile) summary() string {
	tables, columns := 0, 0
	for _, schemas := range s.Catalogs {
		for _, schemaTables := range schemas {
			tables += len(schemaTables)
			for _, tableColumns := range schemaTables {
				columns += len(tableColumns)
			}
		}
	}
	return fmt.Sprintf("%d connections, %d catalogs, %d tables, %d columns", len(s.Connections), len(s.Catalogs), tables, columns)
}
