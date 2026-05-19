package api

import (
	"encoding/json"
	"net/http"
)

type healthzResponse struct {
	Status string `json:"status"`
	SHA    string `json:"sha"`
}

// healthzHandler returns a handler that reports server health and build SHA.
func healthzHandler(buildSHA string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(healthzResponse{
			Status: "ok",
			SHA:    buildSHA,
		})
	}
}
