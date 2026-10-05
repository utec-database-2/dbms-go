package sql

import (
	"fmt"

	"github.com/dbms-go/v2/dbms/lib/storage"
	"github.com/dbms-go/v2/dbms/lib/storage/heap"
	"github.com/dbms-go/v2/dbms/lib/storage/sequential"
)

// heapAdapter expone un HeapFile con las firmas de callback que exigen los
// índices (bplus.RIDTupleStorage). El HeapFile entrega un iterador propio, así
// que el adaptador lo convierte a la forma func(RID, Tuple) bool.
type heapAdapter struct {
	file *heap.HeapFile
}

func newHeapAdapter(path string, schema storage.Table, opts heap.Options) (*heapAdapter, error) {
	f, err := heap.New(path, schema, opts)
	if err != nil {
		return nil, err
	}
	return &heapAdapter{file: f}, nil
}

func (h *heapAdapter) Open() error  { return h.file.Open() }
func (h *heapAdapter) Close() error { return h.file.Close() }

func (h *heapAdapter) Insert(t storage.Tuple) (storage.RID, error) { return h.file.Insert(t) }

func (h *heapAdapter) Get(rid storage.RID) (storage.Tuple, error) { return h.file.Get(rid) }

func (h *heapAdapter) Update(rid storage.RID, t storage.Tuple) error {
	return h.file.Update(rid, t)
}

func (h *heapAdapter) Delete(rid storage.RID) error { return h.file.Delete(rid) }

// Scan recorre las filas vivas en orden físico (página, slot).
func (h *heapAdapter) Scan(fn func(storage.RID, storage.Tuple) bool) error {
	it := h.file.Scan()
	for it.Next() {
		if !fn(it.RID(), it.Tuple()) {
			break
		}
	}
	return it.Err()
}

// Rows devuelve la cantidad de filas vivas.
func (h *heapAdapter) Rows() int64 { return h.file.Rows() }

// Stats expone las métricas del heap para el panel de archivos.
func (h *heapAdapter) Stats() heapStats {
	hdr := h.file.Header()
	return heapStats{
		Pages:      hdr.Pages,
		Rows:       hdr.Rows,
		TotalSize:  hdr.TotalSize,
		PageSize:   hdr.PageSize,
		SlotSize:   hdr.SlotSize,
		MaxRows:    hdr.MaxRows,
		FreePage:   hdr.FreePage,
		Allocation: "free-list" + policySuffix(hdr.Strategy),
	}
}

type heapStats struct {
	Pages      int32
	Rows       int64
	TotalSize  int64
	PageSize   int32
	SlotSize   int32
	MaxRows    int32
	FreePage   int32
	Allocation string
}

func policySuffix(strategy int32) string {
	if strategy == int32(heap.MoveTheLast) {
		return "/move-last"
	}
	return ""
}

// seqAdapter expone un SequentialFile con las firmas que exige el índice
// agrupado (bplus.KeyedTupleStorage). El SequentialFile expone un iterador
// mezclando principal y auxiliar, así que el adaptador lo drena a callback.
type seqAdapter struct {
	file *sequential.SequentialFile
}

func newSeqAdapter(path string, schema storage.Table, opts sequential.Options) (*seqAdapter, error) {
	f, err := sequential.New(path, schema, opts)
	if err != nil {
		return nil, err
	}
	return &seqAdapter{file: f}, nil
}

func (s *seqAdapter) Open() error  { return s.file.Open() }
func (s *seqAdapter) Close() error { return s.file.Close() }

func (s *seqAdapter) Insert(t storage.Tuple) error { return s.file.Insert(t) }

func (s *seqAdapter) Search(key storage.Tuple) (storage.Tuple, error) {
	return s.file.Search(key)
}

func (s *seqAdapter) Update(key storage.Tuple, t storage.Tuple) error {
	return s.file.Update(key, t)
}

func (s *seqAdapter) Delete(key storage.Tuple) error { return s.file.Delete(key) }

// Scan entrega las filas en orden de clave primaria.
func (s *seqAdapter) Scan(fn func(storage.Tuple) bool) error {
	it := s.file.Scan()
	for it.Next() {
		if !fn(it.Tuple()) {
			break
		}
	}
	return it.Err()
}

// RangeScan entrega en orden las filas con lo <= PK <= hi. El secuencial se
// posiciona con búsqueda binaria en lo y se detiene al pasar hi.
func (s *seqAdapter) RangeScan(lo, hi storage.Tuple, fn func(storage.Tuple) bool) error {
	it := s.file.Range(lo, hi)
	for it.Next() {
		if !fn(it.Tuple()) {
			break
		}
	}
	return it.Err()
}

// Stats expone las métricas del secuencial (páginas, desperdicio, overflow).
func (s *seqAdapter) Stats() seqStats {
	st := s.file.Stats()
	dead := st.MainDead
	total := st.MainSlots
	var wasted float64
	if total > 0 {
		wasted = float64(dead) / float64(total)
	}
	return seqStats{
		MainSlots:   st.MainSlots,
		MainDead:    st.MainDead,
		AuxSlots:    st.AuxSlots,
		AuxLive:     st.AuxLive,
		Epoch:       st.Epoch,
		WastedRatio: wasted,
	}
}

type seqStats struct {
	MainSlots   int64
	MainDead    int64
	AuxSlots    int64
	AuxLive     int64
	Epoch       uint64
	WastedRatio float64
}

// scanTable recorre todas las filas de una tabla en el orden que impone su
// almacenamiento: clave primaria si es agrupada, orden físico si no.
func (t *Table) scanTable() ([]storage.Tuple, error) {
	var out []storage.Tuple
	var err error
	switch {
	case t.Clustered && t.seq != nil:
		err = t.seq.Scan(func(tp storage.Tuple) bool {
			out = append(out, tp)
			return true
		})
	case t.heap != nil:
		err = t.heap.Scan(func(_ storage.RID, tp storage.Tuple) bool {
			out = append(out, tp)
			return true
		})
	default:
		return nil, fmt.Errorf("sql: la tabla %s no tiene almacenamiento", t.Name())
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}
