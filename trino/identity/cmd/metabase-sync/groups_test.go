package main

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/metrino/metrino/trino/identity/metabase"
)

// Saved from Metabase's /api/permissions/group and /api/user, trimmed to the fields the sync reads.
const groupsJson = `[
	{"id": 2, "name": "Administrators", "magic_group_type": "admin"},
	{"id": 1, "name": "All Users", "magic_group_type": "all-internal-users"},
	{"id": 5, "name": "Analysts", "magic_group_type": null},
	{"id": 6, "name": "Teachers", "magic_group_type": null},
	{"id": 7, "name": "Empty", "magic_group_type": null}
]`

const usersJson = `{"total": 5, "data": [
	{"id": 1, "email": "admin@example.com", "is_active": true, "group_ids": [1, 2]},
	{"id": 4, "email": "Analyst@Example.com", "is_active": true, "group_ids": [1, 5]},
	{"id": 3, "email": "teacher@example.com", "is_active": true, "group_ids": [1, 6]},
	{"id": 8, "email": "both@example.com", "is_active": true, "group_ids": [1, 5, 6]},
	{"id": 9, "email": "gone@example.com", "is_active": false, "group_ids": [1, 2]}
]}`

const wantGroupFile = `Administrators:admin@example.com
Analysts:Analyst@Example.com,both@example.com
Teachers:both@example.com,teacher@example.com
`

func TestToTrinoGroups(t *testing.T) {
	groups := []metabase.Group{
		{ID: 1, Name: "All Users", MagicGroupType: metabase.AllUsers},
		{ID: 2, Name: "Teachers"},
		{ID: 3, Name: "Administrators", MagicGroupType: "admin"},
		{ID: 4, Name: "Empty"},
		{ID: 5, Name: "Sales: East"},
		{ID: 6, Name: "Sales, West"},
		{ID: 7, Name: "Pilot\nAdministrators:mallory@example.com"},
		{ID: 8, Name: "Teachers "},
		{ID: 9, Name: "#Pilot"},
	}
	users := []metabase.User{
		{Email: "b@example.com", IsActive: true, GroupIDs: []int{1, 2}},
		// Spelled as in Metabase, because the proxy sends Trino that spelling.
		{Email: "A@Example.com", IsActive: true, GroupIDs: []int{1, 2, 3, 5, 6, 7, 8, 9}},
		{Email: "gone@example.com", IsActive: false, GroupIDs: []int{1, 2, 4}},
	}

	trinoGroups, skipped := toTrinoGroups(groups, users)

	want := []trinoGroup{
		{"Administrators", []string{"A@Example.com"}},
		{"Teachers", []string{"A@Example.com", "b@example.com"}},
	}
	if !reflect.DeepEqual(trinoGroups, want) {
		t.Errorf("got %+v, want %+v", trinoGroups, want)
	}
	if !reflect.DeepEqual(skipped, []string{"#Pilot", "Pilot\nAdministrators:mallory@example.com", "Sales, West", "Sales: East", "Teachers "}) {
		t.Errorf("skipped %q", skipped)
	}
	if summary(trinoGroups) != "Administrators: 1, Teachers: 2" {
		t.Errorf("summary is %q", summary(trinoGroups))
	}
}

func TestNoMembersIsAnEmptyFile(t *testing.T) {
	groups := []metabase.Group{{ID: 1, Name: "All Users", MagicGroupType: metabase.AllUsers}, {ID: 2, Name: "Teachers"}}
	users := []metabase.User{{Email: "a@example.com", IsActive: true, GroupIDs: []int{1}}}

	trinoGroups, _ := toTrinoGroups(groups, users)
	if contents := groupFileContents(trinoGroups); contents != "" {
		t.Errorf("got %q", contents)
	}
	if summary(trinoGroups) != "no groups" {
		t.Errorf("summary is %q", summary(trinoGroups))
	}
}

func TestSyncGroupsOnce(t *testing.T) {
	groupFile := filepath.Join(t.TempDir(), "group.txt")
	if err := syncGroupsOnce(fakeMetabase(t, "", 0), groupFile); err != nil {
		t.Fatal(err)
	}
	checkFile(t, groupFile, wantGroupFile)
}

func TestFailedGroupSyncKeepsTheFile(t *testing.T) {
	tests := []struct {
		name   string
		client *metabase.Client
	}{
		{"partial user list", fakeMetabase(t, `{"total": 6, "data": [
			{"id": 1, "email": "admin@example.com", "is_active": true, "group_ids": [1, 2]}]}`, 0)},
		{"HTTP error", fakeMetabase(t, "", http.StatusInternalServerError)},
	}
	for _, tt := range tests {
		groupFile := filepath.Join(t.TempDir(), "group.txt")
		if err := os.WriteFile(groupFile, []byte("Teachers:old@example.com\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := syncGroupsOnce(tt.client, groupFile); err == nil {
			t.Errorf("%s: got no error", tt.name)
		}
		checkFile(t, groupFile, "Teachers:old@example.com\n")
	}
}
