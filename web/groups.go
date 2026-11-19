package main

import (
	"slices"
	"strings"
	"time"
)

// groupFileData is one read of the group file, the file trino/identity/cmd/metabase-sync writes. The server
// reads it on every request, because the sync replaces the file by renaming a new one over it, and reads it
// once: the group check and the admin page use the same copy.
type groupFileData struct {
	lines []groupLine
	// syncedAt is when the Metabase sync last synced the file, nil if it never has (see syncTime).
	syncedAt *time.Time
}

type groupLine struct {
	name    string
	members []string
}

// readGroupFile reads the group file. A missing file is an error: the sync writes an empty one before anything
// else starts.
//
// The file is read the way Trino reads it: each line is "group:member,member", split at the first ':' and then
// at each ',', with spaces trimmed from the names and members. Blank lines, lines starting with '#' and lines
// without a ':' are skipped.
func readGroupFile(path string) (groupFileData, error) {
	contents, modTime, err := readFile(path)
	if err != nil {
		return groupFileData{}, err
	}
	data := groupFileData{syncedAt: syncTime(modTime)}
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, members, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		group := groupLine{name: strings.TrimSpace(name)}
		for _, member := range strings.Split(members, ",") {
			group.members = append(group.members, strings.TrimSpace(member))
		}
		data.lines = append(data.lines, group)
	}
	return data, nil
}

// groupsOf returns the sorted names of the groups that list email, compared without regard to case, or nil if
// there are none. An empty email matches nothing, even though empty members can appear in the file.
func (d groupFileData) groupsOf(email string) []string {
	var groups []string
	for _, line := range d.lines {
		for _, member := range line.members {
			if member != "" && strings.EqualFold(member, email) {
				groups = append(groups, line.name)
				break
			}
		}
	}
	slices.Sort(groups)
	return groups
}

// names returns the sorted names of every group in the file.
func (d groupFileData) names() []string {
	names := []string{}
	for _, line := range d.lines {
		names = append(names, line.name)
	}
	slices.Sort(names)
	return slices.Compact(names)
}
