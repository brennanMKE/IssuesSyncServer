// Package admin serves the server-rendered HTML admin console at /admin/*.
// It uses html/template + htmx; no SPA framework is required.
//
// Phase F — admin console implementation.
package admin

import (
	"net/http"
)

// Handler returns an http.Handler that serves the admin console.
// The hub parameter is typed as interface{} so that this stub compiles
// before the ws.Hub type is fully fleshed out in Phase E.
//
// Phase F will replace this stub with full server-rendered HTML routes.
func Handler(_ interface{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "admin console not yet implemented", http.StatusNotImplemented)
	})
}
