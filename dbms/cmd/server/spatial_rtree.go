package main

import (
	"net/http"

	"github.com/dbms-go/v2/dbms/lib/index/rtree"
	"github.com/dbms-go/v2/dbms/lib/spatial"
)

// rtreeNode es el MBR de un nodo del R-Tree, para dibujarlo en el mapa.
type rtreeNode struct {
	Level  int     `json:"level"` // 0 = raíz
	Leaf   bool    `json:"leaf"`
	Count  int     `json:"count"` // puntos (hoja) o hijos (nodo interno)
	MinLat float64 `json:"minLat"`
	MinLon float64 `json:"minLon"`
	MaxLat float64 `json:"maxLat"`
	MaxLon float64 `json:"maxLon"`
	// Puntos [lat, lon] guardados en la hoja (solo en hojas).
	Entries [][2]float64 `json:"entries,omitempty"`
}

type rtreeResponse struct {
	MaxEntries int         `json:"maxEntries"`
	Height     int         `json:"height"`
	Points     int         `json:"points"`
	Nodes      []rtreeNode `json:"nodes"`
}

// GET /api/spatial/rtree
//
// Devuelve la estructura del R-Tree que ya arma spatial.Store (un MBR por
// nodo y su nivel). Solo lee el árbol: no lo modifica.
func handleSpatialRTree(store *spatial.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store.Tree == nil {
			if err := store.Refresh(); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}

		out := rtreeResponse{
			MaxEntries: store.Tree.MaxEntries,
			Points:     len(store.Locations),
			Nodes:      []rtreeNode{},
		}

		var walk func(n *rtree.Node, level int)
		walk = func(n *rtree.Node, level int) {
			if n == nil {
				return
			}
			if level+1 > out.Height {
				out.Height = level + 1
			}
			count := len(n.Children)
			if n.Leaf {
				count = len(n.Entries)
			}
			if count == 0 {
				return
			}
			box := n.MBR()
			var entries [][2]float64
			if n.Leaf {
				for _, e := range n.Entries {
					entries = append(entries, [2]float64{e.Point.Lat, e.Point.Lon})
				}
			}
			out.Nodes = append(out.Nodes, rtreeNode{
				Level:   level,
				Leaf:    n.Leaf,
				Count:   count,
				MinLat:  box.Min.Lat,
				MinLon:  box.Min.Lon,
				MaxLat:  box.Max.Lat,
				MaxLon:  box.Max.Lon,
				Entries: entries,
			})
			for _, child := range n.Children {
				walk(child, level+1)
			}
		}
		walk(store.Tree.Root, 0)

		writeJSON(w, http.StatusOK, out)
	}
}
