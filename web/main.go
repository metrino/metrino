// Command web serves the docs to anyone in a Metabase group, and the admin pages to Administrators.
//
// It trusts the X-Forwarded-Email header only because it listens on loopback, behind oauth2-proxy in the same
// pod, which sets that header on every request it lets through. WEB_HOST must never be set in Kubernetes: on any
// other address, anyone who can reach the pod could send the header and choose who they are.
//
// Environment: GROUP_FILE (required), WEB_HOST (default 127.0.0.1), WEB_LISTEN_PORT (8000), OPA_URL
// (http://127.0.0.1:8181), STATIC_DIR (/srv/www), METABASE_LINK_URL (the header's Metabase tab and the admin
// page's links to Metabase; neither if unset), PROMETHEUS_URL (the Prometheus server, as http://host[:port],
// which Administrators see at /prometheus/ and in the header's Prometheus tab; neither if unset), SCHEMA_FILE (the Metabase sync's schema file, for the admin page's
// table of who reads each column; no columns if unset), STATUS_FILE (the sync's interval and last errors, for
// the same page's sync warnings; no sync warnings if unset) and TRINO_CATALOGS (the names of Trino's catalogs,
// comma-separated; unknown if unset).
package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
)

func main() {
	groupFile := os.Getenv("GROUP_FILE")
	if groupFile == "" {
		fmt.Fprintln(os.Stderr, "Error: GROUP_FILE is not set")
		os.Exit(1)
	}
	prometheusURL, err := prometheusURLFromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
	cfg := config{
		groupFile:       groupFile,
		opaURL:          getenv("OPA_URL", "http://127.0.0.1:8181"),
		staticDir:       getenv("STATIC_DIR", "/srv/www"),
		metabaseLinkURL: os.Getenv("METABASE_LINK_URL"),
		prometheusURL:   prometheusURL,
		schemaFile:      os.Getenv("SCHEMA_FILE"),
		statusFile:      os.Getenv("STATUS_FILE"),
		trinoCatalogs:   trinoCatalogsFromEnv(),
	}
	addr := net.JoinHostPort(getenv("WEB_HOST", "127.0.0.1"), getenv("WEB_LISTEN_PORT", "8000"))
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, newHandler(cfg)))
}

// getenv returns the environment variable key, or fallback if it is unset or empty.
func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
