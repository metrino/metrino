package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/metrino/metrino/trino/identity/metabase"
)

// The group file is what Trino's file group provider reads. It has one "group:email,email" line per Metabase
// group, under the group's Metabase name, listing the group's active users by email, spelled exactly as in
// Metabase (the proxy sends Trino the same spelling). All Users is left out, so a person with no other group
// gets no groups in Trino.
//
// Besides HTTP errors, a run fails if Metabase returns only part of its user list.

// syncGroupsOnce writes the group file from Metabase's current users and groups, if it changed.
func syncGroupsOnce(client *metabase.Client, groupFile string) error {
	groups, err := client.Groups()
	if err != nil {
		return err
	}
	users, err := client.Users()
	if err != nil {
		return err
	}

	trinoGroups, skipped := toTrinoGroups(groups, users)
	for _, name := range skipped {
		fmt.Println("metabase-sync: skipped the Metabase group", strconv.Quote(name)+
			": Trino's group file can't hold it (a name with ':', ',' or a line break, starting with '#', or "+
			"starting or ending with a space)")
	}

	written, err := writeIfChanged(groupFile, groupFileContents(trinoGroups))
	if err != nil {
		return err
	}
	if written {
		fmt.Println("metabase-sync: wrote", groupFile, "("+summary(trinoGroups)+")")
	}
	return nil
}

// trinoGroup is one line of the group file.
type trinoGroup struct {
	name   string
	emails []string
}

// toTrinoGroups returns the groups for the group file, sorted by name, each with its active users' emails,
// sorted and without duplicates. All Users and groups without active users are left out. Groups whose name
// the file can't hold are left out and returned in skipped.
func toTrinoGroups(groups []metabase.Group, users []metabase.User) (trinoGroups []trinoGroup, skipped []string) {
	emails := map[int][]string{}
	for _, user := range users {
		if !user.IsActive || user.Email == "" {
			continue
		}
		for _, groupId := range user.GroupIDs {
			emails[groupId] = append(emails[groupId], user.Email)
		}
	}

	for _, group := range groups {
		if group.MagicGroupType == metabase.AllUsers {
			continue
		}
		if !fitsGroupFile(group.Name) {
			skipped = append(skipped, group.Name)
			continue
		}
		members := emails[group.ID]
		if len(members) == 0 {
			continue
		}
		slices.Sort(members)
		trinoGroups = append(trinoGroups, trinoGroup{group.Name, slices.Compact(members)})
	}
	slices.SortFunc(trinoGroups, func(a, b trinoGroup) int { return strings.Compare(a.name, b.name) })
	slices.Sort(skipped)
	return trinoGroups, skipped
}

// fitsGroupFile reports whether Trino would read name back unchanged from a "group:user,user" line. Trino
// splits the line at the first ':' and the users at ',', trims spaces, and skips lines starting with '#'.
// A line break would start a new line, which could add someone to another group.
func fitsGroupFile(name string) bool {
	return !strings.ContainsAny(name, ":,\r\n") && strings.TrimSpace(name) == name && !strings.HasPrefix(name, "#")
}

func groupFileContents(trinoGroups []trinoGroup) string {
	contents := ""
	for _, group := range trinoGroups {
		contents += group.name + ":" + strings.Join(group.emails, ",") + "\n"
	}
	return contents
}

// summary is e.g. "Administrators: 1, Teachers: 12", for the log.
func summary(trinoGroups []trinoGroup) string {
	if len(trinoGroups) == 0 {
		return "no groups"
	}
	counts := []string{}
	for _, group := range trinoGroups {
		counts = append(counts, fmt.Sprintf("%s: %d", group.name, len(group.emails)))
	}
	return strings.Join(counts, ", ")
}
