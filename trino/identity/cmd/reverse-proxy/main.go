package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"

	"github.com/metrino/metrino/trino/identity/metabase"
)

var USERID_PATTERN = regexp.MustCompile(`^-- Metabase:: userID: (\d+)`)

// errInactiveUser means the user's Metabase account is deactivated.
var errInactiveUser = errors.New("the user is deactivated in Metabase")

// Reverse proxy implementation based on:
// https://dev.to/b0r/implement-reverse-proxy-in-gogolang-2cp4
func main() {
	trinoUrl := os.Getenv("TRINO_URL")
	reverseProxyHost := os.Getenv("REVERSE_PROXY_HOST")
	reverseProxyPort := os.Getenv("REVERSE_PROXY_PORT")

	if trinoUrl == "" {
		fmt.Println("Error: TRINO_URL is not set")
		os.Exit(1)
	}

	client, err := metabase.NewClientFromEnv()
	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}

	// Localhost only by default: in Kubernetes the proxy is a sidecar and Metabase is its only client.
	if reverseProxyHost == "" {
		reverseProxyHost = "127.0.0.1"
	}

	if reverseProxyPort == "" {
		reverseProxyPort = "9090"
	}

	trinoServer, err := url.Parse(trinoUrl)
	if err != nil {
		log.Fatal("Unable to parse TRINO_URL", err)
		return
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(trinoServer)
		},
	}

	handler := newHandler(proxy, func(userId string) (string, error) {
		return activeUserEmail(client, userId)
	})

	addr := net.JoinHostPort(reverseProxyHost, reverseProxyPort)
	fmt.Println("Reverse Proxy listening on", addr)
	log.Fatal(http.ListenAndServe(addr, handler))
}

// newHandler sends every request on to trino. A query (POST) that carries Metabase's user comment (see
// queryUserID) goes as that user's email, looked up in Metabase each time so a deactivated user is rejected
// straight away. Any other request goes as sent, so a query without the comment runs as the connection's own
// user.
func newHandler(trino http.Handler, lookupEmail func(string) (string, error)) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		fmt.Println(req.Method, req.URL)
		if req.Method != "POST" {
			trino.ServeHTTP(rw, req)
			return
		}

		body, _ := io.ReadAll(req.Body)
		fmt.Println("Request Body:\n", string(body))
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))

		// Rejections below use 4xx/500, not 502/503/504, which the Trino JDBC client retries for about two
		// minutes.
		userId, err := queryUserID(body)
		if err != nil {
			fmt.Println("Rejecting query:", err)
			http.Error(rw, err.Error(), http.StatusForbidden)
			return
		}
		if userId != "" && isPrepare(body) {
			fmt.Println("Rejecting PREPARE for Metabase user", userId)
			http.Error(rw, errExplicitPrepare.Error(), http.StatusBadRequest)
			return
		}

		// Add user header. If the user can't be identified, fail closed: never
		// forward the query, so it can't run as the wrong user.
		if userId != "" {
			email, err := lookupEmail(userId)
			if err != nil {
				fmt.Println("Rejecting query for Metabase user", userId+":", err)
				status := http.StatusInternalServerError
				if errors.Is(err, metabase.ErrNotFound) || errors.Is(err, errInactiveUser) {
					status = http.StatusForbidden
				}
				http.Error(rw, "The proxy could not identify the Metabase user", status)
				return
			}
			fmt.Println("Metabase user", userId, "is", email)
			req.Header.Set("X-Trino-User", email)
			req.Header.Set("X-Trino-Original-User", email)
		}
		trino.ServeHTTP(rw, req)
	})
}

// executeImmediate is how the Trino JDBC driver starts a query with parameters when the Metabase database has
// "Optimized prepared statements" on (explicitPrepare=false): EXECUTE IMMEDIATE '<sql>' USING <values>, with
// each ' in the SQL doubled.
var executeImmediate = []byte("EXECUTE IMMEDIATE '")

// errConflictingUsers means a query names two different Metabase users, so one of them is forged.
var errConflictingUsers = errors.New("the query carries conflicting Metabase user comments")

// errExplicitPrepare explains why a Metabase user's PREPARE is rejected. Trino keeps no prepared statements:
// the EXECUTE that follows a PREPARE is a separate request without Metabase's user comment, so it would run as
// the connection's own user.
var errExplicitPrepare = errors.New(`queries with parameters need "Optimized prepared statements" turned on ` +
	"for this database in Metabase (Admin > Databases)")

// queryUserID returns the ID of the Metabase user a query runs for, or "" if it carries no user comment.
//
// Metabase appends "-- Metabase:: userID: 7 ..." as the last line of the SQL it runs. A query without
// parameters is sent as that SQL, so the comment is the body's last line. A query with parameters is sent as
// EXECUTE IMMEDIATE '<sql>' USING <values>, so the comment is the last line of the quoted SQL and the body
// ends with the values.
//
// A forged comment can only be in whichever of the two places Metabase didn't write: inside a value (a filter
// a user typed), or inside an EXECUTE IMMEDIATE the user wrote in their own SQL. Metabase's real comment is
// always in the other place, so if both places name a user they must agree. Otherwise the query is rejected.
func queryUserID(body []byte) (string, error) {
	outer := metabaseUserID(body)
	inner := ""
	if sql, ok := immediateSQL(body); ok {
		inner = metabaseUserID(sql)
	}
	if outer != "" && inner != "" && outer != inner {
		return "", errConflictingUsers
	}
	if outer != "" {
		return outer, nil
	}
	return inner, nil
}

// immediateSQL returns the SQL quoted in an EXECUTE IMMEDIATE '<sql>' ... query, with doubled quotes undone,
// and false if body doesn't start that way or the quote is never closed.
func immediateSQL(body []byte) ([]byte, bool) {
	if !bytes.HasPrefix(body, executeImmediate) {
		return nil, false
	}
	rest := body[len(executeImmediate):]
	var sql []byte
	for {
		i := bytes.IndexByte(rest, '\'')
		if i < 0 {
			return nil, false
		}
		sql = append(sql, rest[:i]...)
		if i+1 < len(rest) && rest[i+1] == '\'' {
			sql = append(sql, '\'')
			rest = rest[i+2:]
			continue
		}
		return sql, true
	}
}

// isPrepare reports whether a query is a PREPARE statement, which the Trino JDBC driver sends for a query with
// parameters when the Metabase database doesn't have "Optimized prepared statements" on.
func isPrepare(body []byte) bool {
	fields := bytes.Fields(body)
	return len(fields) > 0 && bytes.EqualFold(fields[0], []byte("PREPARE"))
}

// metabaseUserID returns the user ID from the comment Metabase appends to every query
// ("-- Metabase:: userID: 7 queryType: ..."), or "" if the query's last line isn't that comment. Only the
// last line counts: Metabase appends the comment last, and everything before it is SQL the user may have
// written, including a forged comment.
func metabaseUserID(query []byte) string {
	query = bytes.TrimRight(query, " \t\r\n")
	lastLine := query[bytes.LastIndexByte(query, '\n')+1:]
	matches := USERID_PATTERN.FindSubmatch(lastLine)
	if matches == nil {
		return ""
	}
	return string(matches[1])
}

// activeUserEmail returns the email of the Metabase user with this ID. It fails with metabase.ErrNotFound if
// there is no such user, and errInactiveUser if they're deactivated.
func activeUserEmail(client *metabase.Client, userId string) (string, error) {
	user, err := client.User(userId)
	if err != nil {
		return "", err
	}
	if user.Email == "" {
		return "", errors.New("Metabase returned a user without an email")
	}
	if !user.IsActive {
		return "", errInactiveUser
	}
	return user.Email, nil
}
