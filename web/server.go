package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
)

// emailHeader is set by oauth2-proxy to the signed-in person's email.
const emailHeader = "X-Forwarded-Email"

// adminGroup is the Metabase group whose members may see the admin pages.
const adminGroup = "Administrators"

type config struct {
	groupFile string // the Trino group file, read on every request
	opaURL    string // the web pod's own OPA, which holds a copy of Trino's rules
	staticDir string // the docs, with the admin pages under admin/
	// schemaFile lists Metabase's Trino connections and every column of the Trino tables it knows, written by
	// the pod's Metabase sync (trino/identity/cmd/metabase-sync/schema.go). The admin page shows who can read
	// each column.
	schemaFile string
	// statusFile is how the Metabase sync's last runs went, written by the same sync. It may not exist yet.
	statusFile string
	// trinoCatalogs is the names of the deployment's Trino catalogs (never their properties, which hold
	// passwords). nil means unknown, which is not the same as none.
	trinoCatalogs []string
	// metabaseLinkURL is where the header's Metabase tab points, and where the admin page's links to Metabase's
	// admin pages start. Empty means no tab and no links. Metabase is on its own host, so only the deployment
	// knows it.
	metabaseLinkURL string
	// prometheusURL is the Prometheus server the web server shows Administrators at prometheusPath. nil means
	// no Prometheus, and no tab.
	prometheusURL *url.URL
	// now is the time the admin page measures ages from, time.Now if nil.
	now func() time.Time
}

// newHandler serves the docs to anyone in at least one group, and /admin/, /api/admin/ and Prometheus only to
// members of adminGroup. Every route but /healthz checks the person on every request, so a change to the group
// file takes effect at once.
func newHandler(cfg config) http.Handler {
	files := http.FileServer(indexOnly{http.Dir(cfg.staticDir)})
	member := memberCheck(cfg.groupFile)

	// Everything under /admin/ and /api/admin/ passes the admin check before this mux sees it, so a path it
	// doesn't know is a 404 for Administrators and a 403 for everyone else.
	admin := http.NewServeMux()
	admin.Handle("GET /admin/", files)
	admin.Handle("GET /api/admin/rules", adminRules(cfg))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	})
	mux.Handle("GET /", member(anyGroup(files)))
	mux.Handle("GET /api/me", member(me))
	mux.Handle("GET /api/config", member(siteConfig(cfg)))
	// ServeMux would redirect this too, but with a 307; a 301 matches the redirects canonical sends.
	mux.Handle("GET /admin", http.RedirectHandler("/admin/", http.StatusMovedPermanently))
	mux.Handle("GET /admin/", member(adminOnly(admin)))
	mux.Handle("GET /api/admin/", member(adminOnly(admin)))
	if cfg.prometheusURL != nil {
		prometheus := member(adminOnly(prometheusProxy(cfg.prometheusURL)))
		mux.Handle("GET /prometheus", http.RedirectHandler(prometheusPath, http.StatusMovedPermanently))
		mux.Handle("GET "+prometheusPath, prometheus)
		// The UI sends queries as POSTs. POST goes no further than the query API: not to /-/quit or /-/reload,
		// which the Prometheus chart turns on (web.enable-lifecycle). The admin API under api/v1/admin/ would
		// take POSTs, but it is off unless Prometheus runs with web.enable-admin-api.
		mux.Handle("POST "+prometheusPath+"api/v1/", prometheus)
	}
	return securityHeaders(canonical(mux))
}

// prometheusPath is where Administrators see Prometheus. Requests keep their path on the way to Prometheus, so
// Prometheus must serve under the same prefix (prometheus.server.prefixURL and baseURL in helm/values.yaml).
const prometheusPath = "/prometheus/"

// prometheusContentSecurityPolicy replaces contentSecurityPolicy on Prometheus's responses. Its UI needs an
// inline script, which Prometheus fills in on every request, so no hash fits it, and its components set inline
// styles. Only Administrators get these pages, from Prometheus itself.
const prometheusContentSecurityPolicy = "default-src 'self'; script-src 'self' 'unsafe-inline'; " +
	"style-src 'self' 'unsafe-inline'; img-src 'self' data:; object-src 'none'; base-uri 'none'; " +
	"form-action 'self'; frame-ancestors 'none'"

// prometheusProxy passes requests to the Prometheus server at target, path and query unchanged. Prometheus
// learns nothing about the person: their session cookie and oauth2-proxy's headers stay here.
func prometheusProxy(target *url.URL) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			h := r.Out.Header
			h.Del("Cookie")
			h.Del("Authorization")
			for name := range h {
				if strings.HasPrefix(name, "X-Forwarded-") || strings.HasPrefix(name, "X-Auth-Request-") {
					h.Del(name)
				}
			}
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", prometheusContentSecurityPolicy)
		proxy.ServeHTTP(w, r)
	})
}

// contentSecurityPolicy lets the pages load scripts, styles and data only from this site, and nothing embed
// them. The built pages have no inline scripts or styles (Vite writes them to files under /assets/), so none are
// allowed.
const contentSecurityPolicy = "default-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// securityHeaders sets headers on every response that limit what a browser does with it: run only this site's
// own code, never sniff a type other than the one sent, never show the site in a frame (X-Frame-Options for
// browsers older than frame-ancestors), send the path in the Referer only to this site, and keep pages opened
// from it out of its window. HSTS isn't here: TLS ends at the ingress, which sets it.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// canonical routes every request by its decoded, cleaned path, and redirects any other path to that one.
//
// ServeMux matches the escaped path, while the file server opens the decoded one, so without this an encoded
// "/" or ".." (as in /admin%2F or /%2e%2e/admin/) would match "/" and reach the admin pages without the admin
// check.
func canonical(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := cleanPath(r.URL.Path)
		if clean != r.URL.Path {
			u := url.URL{Path: clean, RawQuery: r.URL.RawQuery}
			http.Redirect(w, r, u.String(), http.StatusMovedPermanently)
			return
		}
		u := *r.URL
		u.RawPath = ""
		r2 := *r
		r2.URL = &u
		next.ServeHTTP(w, &r2)
	})
}

// cleanPath returns p rooted and without ".", ".." or repeated slashes, keeping a trailing slash.
func cleanPath(p string) string {
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	clean := path.Clean(p)
	if strings.HasSuffix(p, "/") && clean != "/" {
		clean += "/"
	}
	return clean
}

// person is someone who passed the group check.
type person struct {
	email  string
	groups []string
}

// personHandler handles a request from someone who passed the group check.
type personHandler func(http.ResponseWriter, *http.Request, person)

// groupFileKey is the request context key under which memberCheck leaves the group file it read, so that a
// handler uses the same copy rather than read the file again.
type groupFileKey struct{}

// memberCheck returns a wrapper that lets a request through only if it carries exactly one non-empty email,
// and that email is in at least one group in groupFile. It fails closed: anything else is refused. It passes
// on the group file it read in the request's context (groupFileKey).
func memberCheck(groupFile string) func(personHandler) http.Handler {
	return func(next personHandler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			emails := r.Header.Values(emailHeader)
			if len(emails) != 1 || emails[0] == "" {
				forbidden(w)
				return
			}
			data, err := readGroupFile(groupFile)
			if err != nil {
				log.Printf("reading the group file: %v", err)
				http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
				return
			}
			groups := data.groupsOf(emails[0])
			if len(groups) == 0 {
				forbidden(w)
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), groupFileKey{}, data))
			next(w, r, person{email: emails[0], groups: groups})
		})
	}
}

// forbidden refuses a request without logging it, since anyone can cause one.
func forbidden(w http.ResponseWriter) {
	http.Error(w, "Forbidden", http.StatusForbidden)
}

func anyGroup(next http.Handler) personHandler {
	return func(w http.ResponseWriter, r *http.Request, _ person) {
		next.ServeHTTP(w, r)
	}
}

func adminOnly(next http.Handler) personHandler {
	return func(w http.ResponseWriter, r *http.Request, p person) {
		if !slices.Contains(p.groups, adminGroup) {
			forbidden(w)
			return
		}
		next.ServeHTTP(w, r)
	}
}

func me(w http.ResponseWriter, r *http.Request, p person) {
	writeJSON(w, map[string]any{
		"email":  p.email,
		"groups": p.groups,
		"admin":  slices.Contains(p.groups, adminGroup),
	})
}

// siteConfig tells the pages what only the deployment knows. Prometheus's URL is for Administrators only, like
// its tab, so everyone else gets "", as does everyone when there is no Prometheus.
func siteConfig(cfg config) personHandler {
	return func(w http.ResponseWriter, r *http.Request, p person) {
		prometheusURL := ""
		if cfg.prometheusURL != nil && slices.Contains(p.groups, adminGroup) {
			prometheusURL = prometheusPath
		}
		writeJSON(w, map[string]any{"metabaseUrl": cfg.metabaseLinkURL, "prometheusUrl": prometheusURL})
	}
}

// adminRules serves the admin page's document: the per-deployment data rules Trino enforces, from the web pod's
// OPA; each catalog, with its Metabase connections, its rules, and who can read each table and column in the
// schema file, decided by that OPA as it would decide for Trino (see access.go); how the Metabase sync is
// going; and what needs attention (see warnings.go).
func adminRules(cfg config) http.Handler {
	o := opa{url: cfg.opaURL, client: &http.Client{Timeout: 5 * time.Second}}
	now := cfg.now
	if now == nil {
		now = time.Now
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		groupData, ok := r.Context().Value(groupFileKey{}).(groupFileData)
		if !ok {
			log.Print("the admin page's handler was reached without the group check")
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		rules, err := o.rules(r.Context())
		if err != nil {
			badGateway(w, err)
			return
		}
		// A deployment's own policy may keep its rules in another shape. The page then shows the cells, which
		// come from OPA's decisions, without the rules.
		var byKey map[string][]string
		rulesCustom := json.Unmarshal(rules, &byKey) != nil
		if rulesCustom {
			byKey = nil // Unmarshal keeps what it decoded before the error
		}
		// A file from an older sync leaves the page without the column list and connections, which is worth a
		// warning but not the rules and groups, which an administrator may need in order to fix it.
		schemaOldFormat := ""
		schema, schemaSynced, err := readSchema(cfg.schemaFile)
		var oldFormat *schemaOldFormatError
		if errors.As(err, &oldFormat) {
			log.Printf("reading the schema file: %v", err)
			schemaOldFormat = err.Error()
			schema = schemaFile{}
		} else if err != nil {
			log.Printf("reading the schema file: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		// A broken status file says nothing about the syncs, so the page goes on as without one, rather than fail.
		status, err := readStatus(cfg.statusFile)
		if err != nil {
			log.Printf("reading the status file: %v", err)
		}
		// When each file the page reads last synced, from the files themselves.
		syncedAt := map[string]*time.Time{"group": groupData.syncedAt}
		if cfg.schemaFile != "" {
			syncedAt["schema"] = schemaSynced
		}
		fileGroups := groupData.names()
		groups := pageGroups(fileGroups, byKey)

		// Failed lookups don't fail the page: their cells say so, and the rest still show. One deciding_keys
		// request explains the tables and columns, and finds the key that decides each rule's parent, which the
		// warnings compare the rule with.
		ctx, cancel := context.WithTimeout(r.Context(), accessTimeout)
		defer cancel()
		failures := 0
		paths := schemaPaths(schema.Catalogs)
		asked := map[string]bool{}
		for _, path := range paths {
			asked[pathID(path)] = true
		}
		for key := range byKey {
			if parent, ok := parentPath(key); ok && !asked[pathID(parent)] {
				asked[pathID(parent)] = true
				paths = append(paths, parent)
			}
		}
		decided, explained, err := o.decidingKeys(ctx, paths)
		if err != nil {
			log.Printf("asking OPA which rules decide the tables: %v", err)
			failures++
		}
		var parentKeys map[string]string
		if explained {
			parentKeys = map[string]string{}
			for key := range byKey {
				if parent, ok := parentPath(key); ok {
					parentKeys[key] = decided.of(parent)
				}
			}
		}
		tables, accessFailures := access(ctx, o, groups, schema.Catalogs, decided)
		withMetabase(tables, schema.Metabase)
		if failures += accessFailures; failures > 0 {
			log.Printf("%d of the admin page's OPA lookups failed", failures)
		}

		at := now().UTC()
		rulesFormat := "standard"
		if rulesCustom {
			rulesFormat = "custom"
		}
		writeJSON(w, map[string]any{
			"now": at.Format(time.RFC3339),
			// Where the page links to Metabase's admin pages, "" for no links.
			"metabaseUrl": cfg.metabaseLinkURL,
			"rules":       rules,
			"rulesFormat": rulesFormat,
			// Whether decidedBy can be trusted: an empty one then means no rule covers the path, and not
			// that OPA couldn't say (it failed, or the policy has no deciding_keys).
			"explained": explained,
			"groups":    groups,
			"sync":      syncReportOf(syncedAt, status),
			"warnings": findWarnings(warningInput{
				rules: byKey, rulesCustom: rulesCustom, parentKeys: parentKeys, fileGroups: fileGroups,
				trinoCatalogs: cfg.trinoCatalogs, schema: schema, schemaOldFormat: schemaOldFormat, status: status,
				syncedAt: syncedAt, now: at, opaFailures: failures,
			}),
			"catalogs": catalogEntries(cfg.trinoCatalogs, schema.Connections, byKey, tables),
		})
	})
}

// syncReport is the admin page's "sync": when each file it reads last synced, and how its last sync went.
// IntervalSeconds is 0, and the errors empty, without a status file.
type syncReport struct {
	IntervalSeconds int                 `json:"intervalSeconds"`
	Files           map[string]fileSync `json:"files"`
}

// fileSync is one file in syncReport. SyncedAt is nil if the file has never synced.
type fileSync struct {
	SyncedAt    *time.Time `json:"syncedAt"`
	LastError   string     `json:"lastError,omitempty"`
	LastErrorAt *time.Time `json:"lastErrorAt,omitempty"`
}

// syncReportOf returns the sync report for the files in syncedAt, with status's errors if status isn't nil.
func syncReportOf(syncedAt map[string]*time.Time, status *syncStatus) syncReport {
	report := syncReport{Files: map[string]fileSync{}}
	if status != nil {
		report.IntervalSeconds = status.IntervalSeconds
	}
	for name, at := range syncedAt {
		file := fileSync{SyncedAt: at}
		if status != nil && status.Files[name] != nil {
			file.LastError = status.Files[name].LastError
			file.LastErrorAt = status.Files[name].LastErrorAt
		}
		report.Files[name] = file
	}
	return report
}

// catalogEntry is one catalog on the admin page. InTrino is nil when Trino's catalogs are unknown.
// Connections are the Metabase connections that use it through the reverse proxy, by ID. Bypassing are those
// that don't, whose tables aren't listed, which the warnings say; they are here for the page to link to.
type catalogEntry struct {
	Name        string            `json:"name"`
	InTrino     *bool             `json:"inTrino"`
	Connections []connectionEntry `json:"connections"`
	Bypassing   []connectionEntry `json:"bypassing"`
	Rules       []ruleEntry       `json:"rules"`
	Tables      []tableAccess     `json:"tables"`
}

// connectionEntry is a Metabase connection. OptimizedPrepare is nil when the schema file doesn't say.
type connectionEntry struct {
	ID               int        `json:"id"`
	Name             string     `json:"name"`
	OptimizedPrepare *bool      `json:"optimizedPrepare"`
	LastScan         *time.Time `json:"lastScan"`
}

// ruleEntry is a rules key, what it names ("catalog", "schema", "table" or "column") and its groups.
type ruleEntry struct {
	Key    string   `json:"key"`
	Level  string   `json:"level"`
	Groups []string `json:"groups"`
}

// catalogEntries returns every catalog Trino, Metabase's connections or the rules name, sorted, each with its
// connections, its rules by key, and its tables, which are sorted by catalog already.
func catalogEntries(trinoCatalogs []string, connections []metabaseConnection, rules map[string][]string,
	tables []tableAccess) []catalogEntry {
	byName := map[string]*catalogEntry{}
	entry := func(name string) *catalogEntry {
		if byName[name] == nil {
			e := &catalogEntry{
				Name: name, Connections: []connectionEntry{}, Bypassing: []connectionEntry{}, Rules: []ruleEntry{},
				Tables: []tableAccess{},
			}
			if trinoCatalogs != nil {
				inTrino := catalogInTrino(trinoCatalogs, name)
				e.InTrino = &inTrino
			}
			byName[name] = e
		}
		return byName[name]
	}
	for _, name := range trinoCatalogs {
		entry(name)
	}
	for _, c := range connections {
		if c.Catalog == "" {
			continue
		}
		e := entry(c.Catalog)
		ce := connectionEntry{ID: c.ID, Name: c.Name, OptimizedPrepare: c.OptimizedPrepare, LastScan: c.LastScan}
		if c.ViaProxy {
			e.Connections = append(e.Connections, ce)
		} else {
			e.Bypassing = append(e.Bypassing, ce)
		}
	}
	for key, groups := range rules {
		catalog, _, _ := strings.Cut(key, ".")
		e := entry(catalog)
		if groups == nil {
			groups = []string{} // a key whose value is null
		}
		e.Rules = append(e.Rules, ruleEntry{Key: key, Level: ruleLevel(key), Groups: groups})
	}
	for _, table := range tables {
		e := entry(table.Catalog)
		e.Tables = append(e.Tables, table)
	}

	entries := []catalogEntry{}
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		e := byName[name]
		byID := func(a, b connectionEntry) int { return cmp.Compare(a.ID, b.ID) }
		slices.SortFunc(e.Connections, byID)
		slices.SortFunc(e.Bypassing, byID)
		slices.SortFunc(e.Rules, func(a, b ruleEntry) int { return cmp.Compare(a.Key, b.Key) })
		entries = append(entries, *e)
	}
	return entries
}

func badGateway(w http.ResponseWriter, err error) {
	log.Printf("asking OPA: %v", err)
	http.Error(w, "Bad Gateway", http.StatusBadGateway)
}

// writeJSON writes v as the response. It is never stored in a cache: it says who the person is, and to
// Administrators who can read what.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writing a response: %v", err)
	}
}

// indexOnly hides every directory without an index.html, so the file server never lists a directory.
type indexOnly struct{ fs http.FileSystem }

func (d indexOnly) Open(name string) (http.File, error) {
	f, err := d.fs.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil && info.IsDir() && !d.isFile(path.Join(name, "index.html")) {
		err = fs.ErrNotExist
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func (d indexOnly) isFile(name string) bool {
	f, err := d.fs.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	return err == nil && !info.IsDir()
}
