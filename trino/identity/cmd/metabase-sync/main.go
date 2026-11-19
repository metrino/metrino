// The Metabase sync copies what other programs need to know from Metabase into files, every SYNC_INTERVAL. It
// only ever reads from Metabase. It writes each file whose variable is set, at least one of:
//
//   - GROUP_FILE: the group file Trino's file group provider reads, from Metabase's users and groups (see
//     groups.go). The coordinator's sync writes only this one.
//
//   - SCHEMA_FILE: Metabase's Trino connections, and every column of the tables they know (see schema.go), for
//     the web pod's admin page. The web pod's sync writes both. It also needs TRINO_PROXY_ADDRESSES, the
//     reverse proxy's host:port spellings, comma-separated: the connections that use one of them are the ones
//     whose tables are listed.
//
//   - STATUS_FILE: the sync interval and each file's last error, as JSON (see syncStatus), for the admin page
//     to warn when a sync is failing or stale. Optional: the coordinator's sync has none.
//
// Each file is synced on its own: a run that fails writes nothing to that file, so its readers keep the last
// one, and doesn't stop the other file's run. A run that succeeds sets the file's modification time to when
// the run began, even if the file didn't change, so the time says when the file was last synced, and
// survives a restart of the sync. The empty group file written at startup has the Unix epoch as its time,
// meaning "never synced" (see neverSynced).
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/metrino/metrino/trino/identity/metabase"
)

// syncedFile is one file the sync writes, and the function that writes it once from Metabase. The function
// closes over the file's path and anything else the file needs. The name is the file's key in the status file.
type syncedFile struct {
	name     string
	path     string
	syncOnce func(client *metabase.Client) error
}

func main() {
	groupFile := os.Getenv("GROUP_FILE")
	schemaFile := os.Getenv("SCHEMA_FILE")
	statusFile := os.Getenv("STATUS_FILE")
	syncInterval := os.Getenv("SYNC_INTERVAL")
	proxyAddresses := splitAddresses(os.Getenv("TRINO_PROXY_ADDRESSES"))

	var files []syncedFile
	if groupFile != "" {
		files = append(files, syncedFile{"group", groupFile, func(client *metabase.Client) error {
			return syncGroupsOnce(client, groupFile)
		}})
	}
	if schemaFile != "" {
		if len(proxyAddresses) == 0 {
			fmt.Println("Error: SCHEMA_FILE is set, so TRINO_PROXY_ADDRESSES must be too: the reverse proxy's host:port, comma-separated")
			os.Exit(1)
		}
		files = append(files, syncedFile{"schema", schemaFile, func(client *metabase.Client) error {
			return syncSchemaOnce(client, schemaFile, proxyAddresses)
		}})
	}
	if len(files) == 0 {
		fmt.Println("Error: neither GROUP_FILE nor SCHEMA_FILE is set")
		os.Exit(1)
	}

	interval := 30 * time.Second
	if syncInterval != "" {
		var err error
		interval, err = time.ParseDuration(syncInterval)
		if err != nil || interval <= 0 {
			fmt.Println("Error: SYNC_INTERVAL must be a positive duration such as 30s, got", strconv.Quote(syncInterval))
			os.Exit(1)
		}
	}

	client, err := metabase.NewClientFromEnv()
	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}

	// Trino won't start without the group file.
	if groupFile != "" {
		created, err := createEmptyFile(groupFile)
		if err != nil {
			fmt.Println("Error:", err)
			os.Exit(1)
		}
		if created {
			fmt.Println("metabase-sync: wrote an empty", groupFile)
		}
	}

	var status *syncStatus
	if statusFile != "" {
		status = &syncStatus{path: statusFile, IntervalSeconds: int(interval.Seconds())}
	}

	for {
		runOnce(client, files, status, time.Now())
		time.Sleep(interval)
	}
}

// neverSynced is the modification time of a file the sync made but hasn't synced yet.
var neverSynced = time.Unix(0, 0)

// fileStatus is how one file's last sync went: its error, if it failed. The next success clears it. When the
// file last synced successfully is its modification time.
type fileStatus struct {
	LastError   string     `json:"lastError,omitempty"`
	LastErrorAt *time.Time `json:"lastErrorAt,omitempty"`
}

// syncStatus is the status file's contents: how the sync is going, for the web server to show. Files is keyed
// by syncedFile.name, and has an entry for each file the sync writes, once it has tried. The path is where
// runOnce writes it.
type syncStatus struct {
	path            string
	IntervalSeconds int                    `json:"intervalSeconds"`
	Files           map[string]*fileStatus `json:"files"`
}

// record notes the sync of the file called name, with err if it failed. The time is when the run began, not
// when this file's sync finished.
// Times are kept in UTC, to the second, so the status file reads the same wherever the sync runs.
func (s *syncStatus) record(name string, err error, at time.Time) {
	if s.Files == nil {
		s.Files = map[string]*fileStatus{}
	}
	file := s.Files[name]
	if file == nil {
		file = &fileStatus{}
		s.Files[name] = file
	}
	if err == nil {
		file.LastError = ""
		file.LastErrorAt = nil
		return
	}
	at = at.UTC().Truncate(time.Second)
	file.LastError = err.Error()
	file.LastErrorAt = &at
}

// runOnce syncs each file once, marks each that succeeded as synced at now, then rewrites the status file
// unless status is nil. A failure is logged and recorded, and doesn't stop the other files' runs.
func runOnce(client *metabase.Client, files []syncedFile, status *syncStatus, now time.Time) {
	for _, file := range files {
		err := file.syncOnce(client)
		if err == nil {
			err = markSynced(file.path, now)
		}
		if err != nil {
			fmt.Println("metabase-sync: run failed, keeping the last "+file.path+":", err)
		}
		if status != nil {
			status.record(file.name, err, now)
		}
	}
	if status == nil {
		return
	}
	contents, err := json.Marshal(status)
	if err == nil {
		err = writeFile(status.path, string(contents)+"\n")
	}
	if err != nil {
		fmt.Println("metabase-sync: couldn't write the status file "+status.path+":", err)
	}
}

// splitAddresses splits a comma-separated list, ignoring spaces and empty entries.
func splitAddresses(list string) []string {
	var addresses []string
	for _, address := range strings.Split(list, ",") {
		if address = strings.TrimSpace(address); address != "" {
			addresses = append(addresses, address)
		}
	}
	return addresses
}

// markSynced sets path's modification time to at, to the second, which is when it was last synced.
func markSynced(path string, at time.Time) error {
	at = at.Truncate(time.Second)
	if err := os.Chtimes(path, at, at); err != nil {
		return fmt.Errorf("marking %s as synced: %w", path, err)
	}
	return nil
}

// createEmptyFile creates path as an empty file, readable by everyone, unless it exists already. It hasn't
// synced, so its modification time is neverSynced.
func createEmptyFile(path string) (bool, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// The mode above is reduced by the umask.
	if err := file.Chmod(0o644); err != nil {
		file.Close()
		return false, err
	}
	if err := file.Close(); err != nil {
		return false, err
	}
	return true, os.Chtimes(path, neverSynced, neverSynced)
}

// writeIfChanged replaces path with contents (see writeFile), unless it already has them.
func writeIfChanged(path, contents string) (bool, error) {
	current, err := os.ReadFile(path)
	if err == nil && string(current) == contents {
		return false, nil
	}

	if err := writeFile(path, contents); err != nil {
		return false, err
	}
	return true, nil
}

// writeFile replaces path with contents, readable by everyone. The new file is written next to path and
// renamed over it, so a reader never sees a half-written file.
func writeFile(path, contents string) error {
	file, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, err = file.WriteString(contents)
	if err == nil {
		err = file.Chmod(0o644)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), path)
	}
	if err != nil {
		os.Remove(file.Name())
	}
	return err
}
