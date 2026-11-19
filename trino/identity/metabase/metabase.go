// Package metabase calls Metabase's API with an admin API key. It only reads from Metabase.
package metabase

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// ErrNotFound means Metabase has nothing at the requested path, e.g. no user with the requested ID.
var ErrNotFound = errors.New("not found in Metabase")

// Client calls Metabase at url. The API key is apiKey, or if that's empty the contents of apiKeyFile, read
// on every request because the file may only be written after the program starts.
type Client struct {
	url        string
	apiKey     string
	apiKeyFile string
	http       *http.Client
}

// NewClientFromEnv makes a Client from METABASE_URL, and METABASE_API_KEY or METABASE_KEY_FILE.
func NewClientFromEnv() (*Client, error) {
	metabaseUrl := os.Getenv("METABASE_URL")
	apiKey := os.Getenv("METABASE_API_KEY")
	apiKeyFile := os.Getenv("METABASE_KEY_FILE")
	if metabaseUrl == "" {
		return nil, errors.New("METABASE_URL is not set")
	}
	if apiKey == "" && apiKeyFile == "" {
		return nil, errors.New("set METABASE_API_KEY or METABASE_KEY_FILE")
	}
	return NewClient(metabaseUrl, apiKey, apiKeyFile), nil
}

func NewClient(metabaseUrl, apiKey, apiKeyFile string) *Client {
	return &Client{
		url:        metabaseUrl,
		apiKey:     apiKey,
		apiKeyFile: apiKeyFile,
		http:       &http.Client{Timeout: 30 * time.Second},
	}
}

// User is a Metabase user, with only the fields we use.
type User struct {
	Email    string `json:"email"`
	IsActive bool   `json:"is_active"` // missing decodes as false, so the user counts as deactivated
	GroupIDs []int  `json:"group_ids"` // only in the user list (Users)
}

// Group is a Metabase permission group.
type Group struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	// "all-internal-users" for All Users, "admin" for Administrators, empty for groups an admin made.
	MagicGroupType string `json:"magic_group_type"`
}

// AllUsers is the MagicGroupType of All Users, the group every user is in.
const AllUsers = "all-internal-users"

// User returns the user with this ID, or ErrNotFound if there is none. Deactivated users are returned too.
func (c *Client) User(id string) (User, error) {
	user := User{}
	err := c.get("/api/user/"+url.PathEscape(id), &user)
	return user, err
}

// Users returns every active user (API keys' users aren't listed) with their groups. It fails if Metabase
// returns only part of the list, so a caller never mistakes a partial list for the whole one.
func (c *Client) Users() ([]User, error) {
	list := struct {
		Total int    `json:"total"`
		Data  []User `json:"data"`
	}{}
	if err := c.get("/api/user", &list); err != nil {
		return nil, err
	}
	if list.Total != len(list.Data) {
		return nil, fmt.Errorf("Metabase returned only part of its user list (%d of %d)", len(list.Data), list.Total)
	}
	return list.Data, nil
}

// Groups returns every permission group, including All Users and Administrators.
func (c *Client) Groups() ([]Group, error) {
	groups := []Group{}
	err := c.get("/api/permissions/group", &groups)
	return groups, err
}

// Database is a database connected to Metabase, with only the fields we use.
type Database struct {
	ID     int
	Name   string
	Engine string // "starburst" for Trino
	// The Trino catalog, for a Trino database: Metabase connects to one catalog per database. Empty otherwise.
	Catalog string
	// "host:port" that Metabase connects to, or empty if the connection has no host or no port.
	Address string
	// Whether a query with parameters goes to Trino as one EXECUTE IMMEDIATE request rather than a PREPARE and a
	// separate EXECUTE: the Starburst driver's "Optimized prepared statements" setting (prepared-optimized), or
	// explicitPrepare=false in its additional JDBC options. The reverse proxy only knows who ran such a query if
	// it's on.
	OptimizedPrepare bool
}

// Databases returns every database connected to Metabase. It fails if Metabase returns only part of the
// list, as Users does.
func (c *Client) Databases() ([]Database, error) {
	list := struct {
		Total int `json:"total"`
		Data  []struct {
			ID      int    `json:"id"`
			Name    string `json:"name"`
			Engine  string `json:"engine"`
			Details struct {
				Catalog string `json:"catalog"`
				Host    string `json:"host"`
				// A number for Trino, but some drivers store it as a string.
				Port              json.RawMessage `json:"port"`
				PreparedOptimized bool            `json:"prepared-optimized"`
				AdditionalOptions string          `json:"additional-options"`
			} `json:"details"`
		} `json:"data"`
	}{}
	if err := c.get("/api/database", &list); err != nil {
		return nil, err
	}
	if list.Total != len(list.Data) {
		return nil, fmt.Errorf("Metabase returned only part of its database list (%d of %d)", len(list.Data), list.Total)
	}
	databases := []Database{}
	for _, database := range list.Data {
		address := ""
		if port := portString(database.Details.Port); database.Details.Host != "" && port != "" {
			address = database.Details.Host + ":" + port
		}
		databases = append(databases, Database{
			ID:      database.ID,
			Name:    database.Name,
			Engine:  database.Engine,
			Catalog: database.Details.Catalog,
			Address: address,
			OptimizedPrepare: database.Details.PreparedOptimized ||
				explicitPrepareOff(database.Details.AdditionalOptions),
		})
	}
	return databases, nil
}

// explicitPrepareOff says whether a connection's additional JDBC options (the URL parameters Metabase adds to the
// connection string, "a=1&b=2") set explicitPrepare=false, as "Optimized prepared statements" does.
func explicitPrepareOff(options string) bool {
	for _, option := range strings.Split(strings.TrimLeft(options, "?&"), "&") {
		key, value, _ := strings.Cut(option, "=")
		if strings.TrimSpace(key) == "explicitPrepare" && strings.EqualFold(strings.TrimSpace(value), "false") {
			return true
		}
	}
	return false
}

// portString returns a port that is a JSON number or a numeric string as text, or "" if it is missing, null
// or anything else.
func portString(raw json.RawMessage) string {
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		if _, err := strconv.Atoi(number.String()); err == nil {
			return number.String()
		}
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if _, err := strconv.Atoi(text); err == nil {
			return text
		}
	}
	return ""
}

// LastScans returns, for each database ID, when its newest successful full sync (Metabase's scan of the
// database's tables and columns) ended. A database that has never synced successfully is left out.
//
// Checked against Metabase v0.63: /api/task/ returns {"total", "limit", "offset", "data"}, newest task first.
// Each task has "task" (the step name), "db_id" (null for tasks that belong to no database), "status"
// ("success" when it worked) and "ended_at" (null while it runs). A full sync is the task named "sync"; its
// steps, such as "sync-fields", are tasks of their own. The "task" query parameter filters by name, so we ask
// only for "sync" tasks, up to 1000 of the newest, and take the latest per database rather than relying on
// the order or the page being complete.
func (c *Client) LastScans() (map[int]time.Time, error) {
	list := struct {
		Data []struct {
			Task    string     `json:"task"`
			DbID    *int       `json:"db_id"`
			Status  string     `json:"status"`
			EndedAt *time.Time `json:"ended_at"`
		} `json:"data"`
	}{}
	if err := c.get("/api/task/?task=sync&limit=1000", &list); err != nil {
		return nil, err
	}
	scans := map[int]time.Time{}
	for _, task := range list.Data {
		if task.Task != "sync" || task.Status != "success" || task.DbID == nil || task.EndedAt == nil {
			continue
		}
		if task.EndedAt.After(scans[*task.DbID]) {
			scans[*task.DbID] = *task.EndedAt
		}
	}
	return scans, nil
}

// Table is a table as Metabase last synced it, with only the fields we use.
type Table struct {
	ID     int    `json:"id"`
	Schema string `json:"schema"`
	// What an admin wrote about it in Metabase's table metadata, "" if nothing.
	Description string `json:"description"`
	Name        string `json:"name"`
	Active      bool   `json:"active"` // false once the table is gone from the database
	// "" for a table people can browse and query in Metabase. "hidden", or "technical" or "cruft" (which Metabase
	// also hides), for one an admin hid in its table metadata.
	VisibilityType string  `json:"visibility_type"`
	Fields         []Field `json:"fields"`
}

// Field is a column of a Table, or a field Metabase made inside one (ParentID or NfcPath set).
type Field struct {
	ID               int      `json:"id"`
	Name             string   `json:"name"`
	Description      string   `json:"description"`   // as for a Table
	DatabaseType     string   `json:"database_type"` // the type as the database names it, e.g. "varchar"
	Active           bool     `json:"active"`
	DatabasePosition int      `json:"database_position"`
	ParentID         *int     `json:"parent_id"`
	NfcPath          []string `json:"nfc_path"` // set on fields Metabase unfolds from a JSON column
}

// Tables returns the tables of the database with this ID, hidden ones included, or ErrNotFound if there is no
// such database.
func (c *Client) Tables(databaseID int) ([]Table, error) {
	metadata := struct {
		Tables []Table `json:"tables"`
	}{}
	err := c.get("/api/database/"+strconv.Itoa(databaseID)+"/metadata?include_hidden=true", &metadata)
	return metadata.Tables, err
}

func (c *Client) getApiKey() (string, error) {
	if c.apiKey != "" {
		return c.apiKey, nil
	}

	contents, err := os.ReadFile(c.apiKeyFile)
	if err != nil {
		return "", fmt.Errorf("unable to read the Metabase API key: %w", err)
	}
	if len(contents) == 0 {
		return "", errors.New("the Metabase API key file is empty")
	}
	return string(contents), nil
}

// get decodes the JSON that Metabase returns for a GET of path into v.
func (c *Client) get(path string, v any) error {
	apiKey, err := c.getApiKey()
	if err != nil {
		return err
	}

	req, err := http.NewRequest("GET", c.url+path, nil)
	if err != nil {
		return err
	}
	req.Header.Add("X-API-Key", apiKey)

	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: invalid status code: %d", path, res.StatusCode)
	}
	return json.NewDecoder(res.Body).Decode(v)
}
