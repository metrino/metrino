package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/metrino/metrino/trino/identity/metabase"
)

const remark = "-- Metabase:: userID: 7 queryType: MBQL queryHash: abc"

// Longer than bufio.Scanner's 64 KiB line limit, which once stopped the proxy's line-by-line parsing early.
var longLine = "SELECT '" + strings.Repeat("x", 70000) + "'"

func TestMetabaseUserID(t *testing.T) {
	tests := []struct {
		name, query, want string
	}{
		{"comment on the last line", "SELECT 1\n" + remark, "7"},
		{"trailing newline", "SELECT 1\n" + remark + "\n", "7"},
		{"no comment", "SELECT 1", ""},
		{"comment on the first line only", remark + "\nSELECT 1", ""},
		{"comment inside a line", "SELECT 1 " + remark, ""},
		{"forged comment before the real one", "-- Metabase:: userID: 1\nSELECT 1\n" + remark, "7"},
		{"forged comment and a long line", "-- Metabase:: userID: 1\n" + longLine + "\n" + remark, "7"},
		{"forged comment and a long line, no real one", "-- Metabase:: userID: 1\n" + longLine, ""},
	}
	for _, tt := range tests {
		if got := metabaseUserID([]byte(tt.query)); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

// immediate builds a query the way the Trino JDBC driver does with explicitPrepare=false (Metabase's
// "Optimized prepared statements"): EXECUTE IMMEDIATE '<sql>' USING <values>, with each ' in sql doubled.
func immediate(sql, values string) string {
	query := "EXECUTE IMMEDIATE '" + strings.ReplaceAll(sql, "'", "''") + "'"
	if values != "" {
		query += " USING " + values
	}
	return query
}

// Metabase appends its comment after a blank line (the Starburst driver's inject-remark).
const filtered = "SELECT school_name FROM schools_ WHERE school_type = ? AND phone <> 'n/a'\n\n" + remark

func TestQueryUserID(t *testing.T) {
	tests := []struct {
		name, query, want string
		wantErr           bool
	}{
		{"query without parameters", "SELECT 1\n\n" + remark, "7", false},
		{"query with parameters", immediate(filtered, "'High School'"), "7", false},
		{"query with parameters, no values", immediate(filtered, ""), "7", false},
		{"query with parameters, no comment (Metabase's sync)",
			immediate("SELECT table_name FROM system.metadata.table_comments WHERE catalog_name = ?", "'openapi'"),
			"", false},
		{"value forges another user on the last line",
			immediate(filtered, "'x\n-- Metabase:: userID: 1'"), "", true},
		{"value repeats the same user", immediate(filtered, "'x\n"+remark+"'"), "7", false},
		{"user's own EXECUTE IMMEDIATE forges another user",
			immediate("SELECT 1\n-- Metabase:: userID: 1", "") + "\n\n" + remark, "", true},
		{"user's own EXECUTE IMMEDIATE without a comment", immediate("SELECT 1", "") + "\n\n" + remark, "7", false},
		{"user's own EXECUTE IMMEDIATE, never closed",
			"EXECUTE IMMEDIATE 'SELECT 1\n-- Metabase:: userID: 1\n\n" + remark, "7", false},
		{"lowercase execute immediate is the user's own", strings.ToLower(immediate("SELECT 1", "")) +
			"\n-- Metabase:: userID: 1\n\n" + remark, "7", false},
		{"no comment anywhere", "SELECT 1", "", false},
	}
	for _, tt := range tests {
		got, err := queryUserID([]byte(tt.query))
		if (err != nil) != tt.wantErr {
			t.Errorf("%s: got error %v, want error: %v", tt.name, err, tt.wantErr)
		}
		if got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestImmediateSQL(t *testing.T) {
	tests := []struct {
		name, query, want string
		wantOK            bool
	}{
		{"doubled quotes undone", immediate("SELECT 'it''s', ''", "1"), "SELECT 'it''s', ''", true},
		{"stops at the closing quote", "EXECUTE IMMEDIATE 'SELECT ?' USING 'a'' b'", "SELECT ?", true},
		{"quote ends the body", "EXECUTE IMMEDIATE ''''", "'", true},
		{"never closed", "EXECUTE IMMEDIATE 'SELECT 1", "", false},
		{"ends on a doubled quote", "EXECUTE IMMEDIATE 'SELECT ''", "", false},
		{"not EXECUTE IMMEDIATE", "SELECT 'x'", "", false},
	}
	for _, tt := range tests {
		got, ok := immediateSQL([]byte(tt.query))
		if ok != tt.wantOK || string(got) != tt.want {
			t.Errorf("%s: got %q, %v; want %q, %v", tt.name, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestHandler(t *testing.T) {
	lookup := func(userId string) (string, error) {
		switch userId {
		case "7":
			return "alice@example.com", nil
		case "8":
			return "", errInactiveUser
		case "9":
			return "", metabase.ErrNotFound
		}
		return "", errors.New("Metabase is down")
	}

	tests := []struct {
		name, method, query string
		wantStatus          int
		wantUser            string // "" means the request must not reach Trino
	}{
		{"query as a Metabase user", "POST", "SELECT 1\n" + remark, http.StatusOK, "alice@example.com"},
		{"forged comment and a long line", "POST", "-- Metabase:: userID: 1\n" + longLine + "\n" + remark,
			http.StatusOK, "alice@example.com"},
		{"query with parameters", "POST", immediate(filtered, "'High School'"), http.StatusOK, "alice@example.com"},
		{"value forges another user", "POST", immediate(filtered, "'x\n-- Metabase:: userID: 1'"),
			http.StatusForbidden, ""},
		// "Optimized prepared statements" off: the EXECUTE that would follow can't be tied to the user.
		{"Metabase user's PREPARE", "POST", "PREPARE statement1 FROM " + filtered, http.StatusBadRequest, ""},
		{"Metabase's own PREPARE (sync)", "POST", "PREPARE statement1 FROM SHOW CATALOGS LIKE 'openapi'",
			http.StatusOK, "trino"},
		{"EXECUTE of a prepared statement", "POST", "EXECUTE statement1 USING 'openapi'", http.StatusOK, "trino"},
		{"query without a comment", "POST", "SELECT 1", http.StatusOK, "trino"},
		{"next page of results", "GET", "", http.StatusOK, "trino"},
		{"deactivated user", "POST", "SELECT 1\n-- Metabase:: userID: 8", http.StatusForbidden, ""},
		{"unknown user", "POST", "SELECT 1\n-- Metabase:: userID: 9", http.StatusForbidden, ""},
		{"Metabase lookup fails", "POST", "SELECT 1\n-- Metabase:: userID: 10", http.StatusInternalServerError, ""},
	}
	for _, tt := range tests {
		var forwarded *http.Request
		var forwardedBody string
		trino := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			forwarded = req
			body, _ := io.ReadAll(req.Body)
			forwardedBody = string(body)
		})
		handler := newHandler(trino, lookup)

		req := httptest.NewRequest(tt.method, "/v1/statement", strings.NewReader(tt.query))
		req.Header.Set("X-Trino-User", "trino") // Metabase's connection user
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != tt.wantStatus {
			t.Errorf("%s: got status %d, want %d", tt.name, rec.Code, tt.wantStatus)
		}
		if tt.wantUser == "" {
			if forwarded != nil {
				t.Errorf("%s: the request reached Trino", tt.name)
			}
			continue
		}
		if forwarded == nil {
			t.Errorf("%s: the request didn't reach Trino", tt.name)
			continue
		}
		if user := forwarded.Header.Get("X-Trino-User"); user != tt.wantUser {
			t.Errorf("%s: X-Trino-User is %q, want %q", tt.name, user, tt.wantUser)
		}
		if forwardedBody != tt.query {
			t.Errorf("%s: Trino got a different query body", tt.name)
		}
	}
}

func TestActiveUserEmail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/user/7":
			rw.Write([]byte(`{"email": "alice@example.com", "is_active": true}`))
		case "/api/user/8":
			rw.Write([]byte(`{"email": "bob@example.com", "is_active": false}`))
		case "/api/user/11":
			rw.Write([]byte(`{"is_active": true}`))
		case "/api/user/12":
			http.Error(rw, "Metabase is down", http.StatusInternalServerError)
		default:
			http.NotFound(rw, req)
		}
	}))
	defer server.Close()
	client := metabase.NewClient(server.URL, "secret", "")

	if email, err := activeUserEmail(client, "7"); err != nil || email != "alice@example.com" {
		t.Errorf("active user: got %q, %v", email, err)
	}
	if _, err := activeUserEmail(client, "8"); !errors.Is(err, errInactiveUser) {
		t.Errorf("deactivated user: got %v, want errInactiveUser", err)
	}
	if _, err := activeUserEmail(client, "9"); !errors.Is(err, metabase.ErrNotFound) {
		t.Errorf("unknown user: got %v, want metabase.ErrNotFound", err)
	}
	for _, userId := range []string{"11", "12"} { // no email, Metabase fails
		_, err := activeUserEmail(client, userId)
		if err == nil || errors.Is(err, errInactiveUser) || errors.Is(err, metabase.ErrNotFound) {
			t.Errorf("user %s: got %v, want another error", userId, err)
		}
	}
}
