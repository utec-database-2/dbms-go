package textindex

import "sort"

// MemIndex es un índice invertido en memoria, simple y sin optimizaciones.
// Sirve de índice de prueba para que los demás frentes avancen sin esperar
// al SPIMI, y de oráculo para comprobar que el índice real devuelve lo mismo.
type MemIndex struct {
	postings map[string][]Posting
	docLens  []uint32
	total    uint64
}

func NewMemIndex() *MemIndex {
	return &MemIndex{postings: make(map[string][]Posting)}
}

// AddDocument agrega un documento ya tokenizado y devuelve su docID.
func (m *MemIndex) AddDocument(tokens []string) uint32 {
	id := uint32(len(m.docLens))
	counts := make(map[string]uint32, len(tokens))
	for _, t := range tokens {
		counts[t]++
	}
	for t, tf := range counts {
		m.postings[t] = append(m.postings[t], Posting{DocID: id, TF: tf})
	}
	m.docLens = append(m.docLens, uint32(len(tokens)))
	m.total += uint64(len(tokens))
	return id
}

func (m *MemIndex) Postings(term string) ([]Posting, error) {
	ps := m.postings[term]
	if len(ps) == 0 {
		return nil, nil
	}
	out := make([]Posting, len(ps))
	copy(out, ps)
	return out, nil
}

func (m *MemIndex) DF(term string) int { return len(m.postings[term]) }

func (m *MemIndex) DocLen(doc uint32) int {
	if int(doc) >= len(m.docLens) {
		return 0
	}
	return int(m.docLens[doc])
}

func (m *MemIndex) NumDocs() int { return len(m.docLens) }

func (m *MemIndex) AvgDocLen() float64 {
	if len(m.docLens) == 0 {
		return 0
	}
	return float64(m.total) / float64(len(m.docLens))
}

// Terms devuelve el vocabulario ordenado.
func (m *MemIndex) Terms() []string {
	out := make([]string, 0, len(m.postings))
	for t := range m.postings {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

var _ InvertedIndex = (*MemIndex)(nil)
