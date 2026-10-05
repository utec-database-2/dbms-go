package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/dbms-go/v2/dbms/lib/spatial"
)

// POST /api/spatial/knn {"latitude": ..., "longitude": ..., "k": 10, "metric": "haversine"}
func handleSpatialKNN(store *spatial.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		var req spatial.KNNRequest

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("body inválido: %w", err))
			return
		}

		result, err := store.SearchKNN(req)

		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		writeJSON(w, http.StatusOK, result)
	}
}
