package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

// The files the Metabase sync writes for the admin page, and the list of Trino's catalogs, which only the
// deployment knows.

// schemaFile is the schema file (trino/identity/cmd/metabase-sync/schema.go): Metabase's Trino connections, and
// the catalogs' tables and columns, each catalog → schema → table → columns. Metabase has Metabase's IDs and
// descriptions for the same tables; a file from a sync before it had them has none.
type schemaFile struct {
	Connections []metabaseConnection                               `json:"connections"`
	Catalogs    map[string]map[string]map[string][]schemaColumn    `json:"catalogs"`
	Metabase    map[string]map[string]map[string]metabaseTableInfo `json:"metabase"`
}

// metabaseConnection is one of Metabase's Trino connections. ViaProxy says whether it goes through the reverse
// proxy, which the page needs because only then do Metabase's queries get Trino's rules. OptimizedPrepare says
// whether it has "Optimized prepared statements" on, without which the proxy can't tell who ran a query with
// parameters; it is nil in a file from a sync that didn't record it. LastScan is when Metabase last scanned it
// successfully, and is nil if Metabase hasn't said.
type metabaseConnection struct {
	ID               int        `json:"id"`
	Name             string     `json:"name"`
	Catalog          string     `json:"catalog"`
	Address          string     `json:"address"`
	ViaProxy         bool       `json:"viaProxy"`
	OptimizedPrepare *bool      `json:"optimizedPrepare"`
	LastScan         *time.Time `json:"lastScan"`
}

type schemaColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// metabaseTableInfo is what Metabase has to say about a table: its database ID, its table ID, the description an
// admin wrote, whether an admin hid it, and its columns' by name.
type metabaseTableInfo struct {
	Database    int                          `json:"database"`
	Table       int                          `json:"table"`
	Description string                       `json:"description"`
	Hidden      bool                         `json:"hidden"`
	Fields      map[string]metabaseFieldInfo `json:"fields"`
}

type metabaseFieldInfo struct {
	ID          int    `json:"id"`
	Description string `json:"description"`
}

// schemaOldFormatError is readSchema's error for a schema file from an older sync, a type so that the admin page
// can tell it from a file it can't read at all (with errors.As) and still show its message as it is.
type schemaOldFormatError struct{ path string }

func (e *schemaOldFormatError) Error() string {
	return e.path + " is in an older format: update the Metabase sync image"
}

// readSchema reads the schema file, and when the Metabase sync last synced it (nil if never). No file
// (SCHEMA_FILE unset, or before the sync's first successful run) is an empty schemaFile. A file from an older
// sync, which wrote the catalogs alone, is an error that says so, since read as the current format it would
// look like an empty schema; its time is still returned.
func readSchema(path string) (schemaFile, *time.Time, error) {
	contents, modTime, ok, err := readOptional(path)
	if err != nil || !ok {
		return schemaFile{}, nil, err
	}
	syncedAt := syncTime(modTime)
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(contents, &keys); err != nil {
		return schemaFile{}, syncedAt, fmt.Errorf("%s: %w", path, err)
	}
	if _, ok := keys["catalogs"]; !ok {
		return schemaFile{}, syncedAt, &schemaOldFormatError{path}
	}
	var s schemaFile
	if err := json.Unmarshal(contents, &s); err != nil {
		return schemaFile{}, syncedAt, fmt.Errorf("%s: %w", path, err)
	}
	return s, syncedAt, nil
}

// syncStatus is the status file (trino/identity/cmd/metabase-sync/main.go): the sync's interval, and how each
// file's last sync went. Files is keyed by the file the sync writes, "group" or "schema". When each file last
// synced successfully is its modification time, not in here.
type syncStatus struct {
	IntervalSeconds int                    `json:"intervalSeconds"`
	Files           map[string]*fileStatus `json:"files"`
}

// fileStatus is how one file's last sync went: LastError is set if it failed, and cleared by the next success.
type fileStatus struct {
	LastError   string     `json:"lastError"`
	LastErrorAt *time.Time `json:"lastErrorAt"`
}

// readStatus reads the status file. It returns nil if there is no file: STATUS_FILE is unset, or the sync
// hasn't finished its first loop.
func readStatus(path string) (*syncStatus, error) {
	contents, _, ok, err := readOptional(path)
	if err != nil || !ok {
		return nil, err
	}
	var s syncStatus
	if err := json.Unmarshal(contents, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &s, nil
}

// readFile reads the file at path, and when it was last modified, both from one open file, so that they agree
// even if the sync renames a new file over it in between.
func readFile(path string) (contents []byte, modTime time.Time, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, time.Time{}, err
	}
	contents, err = io.ReadAll(file)
	if err != nil {
		return nil, time.Time{}, err
	}
	return contents, info.ModTime(), nil
}

// readOptional reads the file at path, as readFile does. ok is false if path is empty or the file doesn't exist.
func readOptional(path string) (contents []byte, modTime time.Time, ok bool, err error) {
	if path == "" {
		return nil, time.Time{}, false, nil
	}
	contents, modTime, err = readFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, time.Time{}, false, nil
	}
	return contents, modTime, err == nil, err
}

// syncTime is when the Metabase sync last synced a file, from its modification time, which the sync sets after
// each successful run. It is nil for a file the sync made but hasn't synced, whose time is the Unix epoch, and
// for the zero time.
func syncTime(modTime time.Time) *time.Time {
	if !modTime.After(time.Unix(0, 0)) {
		return nil
	}
	t := modTime.UTC()
	return &t
}

// catalogInTrino says whether catalog is one of trinoCatalogs, or Trino's built-in system catalog, which every
// Trino has. It is only meaningful when trinoCatalogs is known (not nil).
func catalogInTrino(trinoCatalogs []string, catalog string) bool {
	return catalog == systemCatalog || slices.Contains(trinoCatalogs, catalog)
}

// prometheusURLFromEnv returns PROMETHEUS_URL, the Prometheus server to show Administrators, or nil if it is
// unset. It must be http(s)://host[:port] with no path: requests keep their own /prometheus/ path.
func prometheusURLFromEnv() (*url.URL, error) {
	value := os.Getenv("PROMETHEUS_URL")
	if value == "" {
		return nil, nil
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.Trim(u.Path, "/") != "" ||
		u.RawQuery != "" || u.User != nil {
		return nil, fmt.Errorf("PROMETHEUS_URL %q is not http(s)://host[:port] without a path", value)
	}
	return u, nil
}

// trinoCatalogsFromEnv returns the names in TRINO_CATALOGS, comma-separated, sorted. Unset is nil, meaning the
// deployment's catalogs are unknown, which is not the same as having none.
func trinoCatalogsFromEnv() []string {
	value := strings.TrimSpace(os.Getenv("TRINO_CATALOGS"))
	if value == "" {
		return nil
	}
	var names []string
	for _, name := range strings.Split(value, ",") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}
