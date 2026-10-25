package textindex

import (
	"testing"
)

// prueba para AddDocument y Postings
func TestAddDocumentAndPostings(t *testing.T) {
	m := NewMemIndex()
	doc1 := []string{"hola", "mundo", "hola"}
	doc2 := []string{"adiós", "mundo"}

	id1 := m.AddDocument(doc1)
	if id1 != 0 {
		t.Errorf("AddDocument returned docID %d, want 0", id1)
	}
	if m.DocLen(id1) != len(doc1) {
		t.Errorf("DocLen(%d) = %d, want %d", id1, m.DocLen(id1), len(doc1))
	}

	id2 := m.AddDocument(doc2)
	if id2 != 1 {
		t.Errorf("AddDocument returned docID %d, want 1", id2)
	}
	if m.DocLen(id2) != len(doc2) {
		t.Errorf("DocLen(%d) = %d, want %d", id2, m.DocLen(id2), len(doc2))
	}

	if m.NumDocs() != 2 {
		t.Errorf("NumDocs() = %d, want 2", m.NumDocs())
	}

	if m.AvgDocLen() != float64(len(doc1)+len(doc2))/2 {
		t.Errorf("AvgDocLen() = %f, want %f", m.AvgDocLen(), float64(len(doc1)+len(doc2))/2)
	}

	postingsHola, err := m.Postings("hola")
	if err != nil {
		t.Errorf("Postings(\"hola\") returned error: %v", err)
	}
	if len(postingsHola) != 1 || postingsHola[0].DocID != id1 || postingsHola[0].TF != 2 {
		t.Errorf("Postings(\"hola\") = %v, want DocID %d with TF 2", postingsHola, id1)
	}

	postingsMundo, err := m.Postings("mundo")
	if err != nil {
		t.Errorf("Postings(\"mundo\") returned error: %v", err)
	}
	if len(postingsMundo) != 2 {
		t.Errorf("Postings(\"mundo\") = %v, want 2 postings", postingsMundo)
	}
	if postingsMundo[0] != (Posting{DocID: id1, TF: 1}) || postingsMundo[1] != (Posting{DocID: id2, TF: 1}) {
		t.Errorf("Postings(\"mundo\") = %v, want DocID %d and %d with TF 1 each", postingsMundo, id1, id2)
	}

	postingsAdios, err := m.Postings("adiós")
	if err != nil {
		t.Errorf("Postings(\"adiós\") returned error: %v", err)
	}
	if len(postingsAdios) != 1 || postingsAdios[0].DocID != id2 || postingsAdios[0].TF != 1 {
		t.Errorf("Postings(\"adiós\") = %v, want DocID %d with TF 1", postingsAdios, id2)
	}

	postingsUnknown, err := m.Postings("desconocido")
	if err != nil {
		t.Errorf("Postings(\"desconocido\") returned error: %v", err)
	}
	if postingsUnknown != nil {
		t.Errorf("Postings(\"desconocido\") = %v, want nil", postingsUnknown)
	}

	id3 := m.AddDocument(nil)
	if id3 != 2 {
		t.Errorf("AddDocument returned docID %d, want 2", id3)
	}
	if m.DocLen(id3) != 0 {
		t.Errorf("DocLen(%d) = %d, want 0 for empty document", id3, m.DocLen(id3))
	}

	id4 := m.AddDocument([]string{})
	if id4 != 3 {
		t.Errorf("AddDocument returned docID %d, want 3", id4)
	}
	if m.DocLen(id4) != 0 {
		t.Errorf("DocLen(%d) = %d, want 0 for empty document", id4, m.DocLen(id4))
	}

	if m.NumDocs() != 4 {
		t.Errorf("NumDocs() = %d, want 4", m.NumDocs())
	}

	if m.AvgDocLen() != float64(len(doc1)+len(doc2))/4 {
		t.Errorf("AvgDocLen() = %f, want %f", m.AvgDocLen(), float64(len(doc1)+len(doc2))/4)
	}
}

// prueba de que postings devuelve una copia y no el slice interno
func TestPostingsReturnsCopy(t *testing.T) {
	m := NewMemIndex()
	doc := []string{"hola", "mundo"}
	m.AddDocument(doc)

	postings, err := m.Postings("hola")
	if err != nil {
		t.Errorf("Postings(\"hola\") returned error: %v", err)
	}
	if len(postings) != 1 {
		t.Fatalf("Postings(\"hola\") = %v, want 1 posting", postings)
	}

	// Modificar el slice devuelto no debería afectar al índice interno
	postings[0].TF = 999

	postingsAfter, err := m.Postings("hola")
	if err != nil {
		t.Errorf("Postings(\"hola\") returned error: %v", err)
	}
	if postingsAfter[0].TF != 1 {
		t.Errorf("Postings(\"hola\") after modification = %v, want TF 1", postingsAfter)
	}
}

// prueba para DF
func TestDF(t *testing.T) {
	m := NewMemIndex()
	m.AddDocument([]string{"hola", "mundo", "hola"})
	m.AddDocument([]string{"adiós", "mundo"})

	if m.DF("hola") != 1 {
		t.Errorf("DF(\"hola\") = %d, want 1", m.DF("hola"))
	}
	if m.DF("mundo") != 2 {
		t.Errorf("DF(\"mundo\") = %d, want 2", m.DF("mundo"))
	}
	if m.DF("adiós") != 1 {
		t.Errorf("DF(\"adiós\") = %d, want 1", m.DF("adiós"))
	}
	if m.DF("desconocido") != 0 {
		t.Errorf("DF(\"desconocido\") = %d, want 0", m.DF("desconocido"))
	}
}

// prueba para DocLen
func TestDocLen(t *testing.T) {
	m := NewMemIndex()
	id1 := m.AddDocument([]string{"hola", "mundo", "hola"})
	id2 := m.AddDocument([]string{"adiós", "mundo"})

	if m.DocLen(id1) != 3 {
		t.Errorf("DocLen(%d) = %d, want 3", id1, m.DocLen(id1))
	}
	if m.DocLen(id2) != 2 {
		t.Errorf("DocLen(%d) = %d, want 2", id2, m.DocLen(id2))
	}
	if m.DocLen(999) != 0 {
		t.Errorf("DocLen(999) = %d, want 0 for non-existent docID", m.DocLen(999))
	}
}

// prueba para NumDocs
func TestNumDocs(t *testing.T) {
	m := NewMemIndex()
	if m.NumDocs() != 0 {
		t.Errorf("NumDocs() = %d, want 0", m.NumDocs())
	}
	m.AddDocument([]string{"hola"})
	if m.NumDocs() != 1 {
		t.Errorf("NumDocs() = %d, want 1", m.NumDocs())
	}
	m.AddDocument([]string{"adiós"})
	if m.NumDocs() != 2 {
		t.Errorf("NumDocs() = %d, want 2", m.NumDocs())
	}
}

// prueba para AvgDocLen
func TestAvgDocLen(t *testing.T) {
	m := NewMemIndex()
	if m.AvgDocLen() != 0 {
		t.Errorf("AvgDocLen() = %f, want 0", m.AvgDocLen())
	}
	m.AddDocument([]string{"hola", "mundo"})
	if m.AvgDocLen() != 2 {
		t.Errorf("AvgDocLen() = %f, want 2", m.AvgDocLen())
	}
	m.AddDocument([]string{"adiós"})
	if m.AvgDocLen() != 1.5 {
		t.Errorf("AvgDocLen() = %f, want 1.5", m.AvgDocLen())
	}
}

// prueba para Terms
func TestTerms(t *testing.T) {
	m := NewMemIndex()
	m.AddDocument([]string{"hola", "mundo", "hola"})
	m.AddDocument([]string{"adiós", "mundo"})

	terms := m.Terms()
	if len(terms) != 3 {
		t.Errorf("Terms() = %v, want 3 terms", terms)
	}
	expectedTerms := []string{"adiós", "hola", "mundo"}
	for i, term := range terms {
		if term != expectedTerms[i] {
			t.Errorf("Terms() = %v, want %v", terms, expectedTerms)
		}
	}
}

// prueba para terminos desconocidos
func TestUnknownTerms(t *testing.T) {
	m := NewMemIndex()
	m.AddDocument([]string{"hola", "mundo"})

	postings, err := m.Postings("desconocido")
	if err != nil {
		t.Errorf("Postings(\"desconocido\") returned error: %v", err)
	}
	if postings != nil {
		t.Errorf("Postings(\"desconocido\") = %v, want nil", postings)
	}
	if m.DF("desconocido") != 0 {
		t.Errorf("DF(\"desconocido\") = %d, want 0", m.DF("desconocido"))
	}
}

// prueba de preservacion de docIDs
func TestDocIDPreservation(t *testing.T) {
	m := NewMemIndex()
	doc1 := []string{"hola", "mundo"}
	doc2 := []string{"adiós", "mundo"}
	doc3 := []string{"hola", "adiós"}

	id1 := m.AddDocument(doc1)
	id2 := m.AddDocument(doc2)
	id3 := m.AddDocument(doc3)

	if id1 != 0 || id2 != 1 || id3 != 2 {
		t.Errorf("DocIDs = %d, %d, %d; want 0, 1, 2", id1, id2, id3)
	}

	postingsMundo, err := m.Postings("mundo")
	if err != nil {
		t.Fatalf("Postings(\"mundo\") returned error: %v", err)
	}
	if len(postingsMundo) != 2 || postingsMundo[0].DocID != id1 || postingsMundo[1].DocID != id2 {
		t.Errorf("Postings(\"mundo\") = %v; want DocIDs %d and %d", postingsMundo, id1, id2)
	}
	postingsHola, err := m.Postings("hola")
	if err != nil {
		t.Fatalf("Postings(\"hola\") returned error: %v", err)
	}
	if len(postingsHola) != 2 || postingsHola[0].DocID != id1 || postingsHola[1].DocID != id3 {
		t.Errorf("Postings(\"hola\") = %v; want DocIDs %d and %d", postingsHola, id1, id3)
	}

	postingsAdios, err := m.Postings("adiós")
	if err != nil {
		t.Fatalf("Postings(\"adiós\") returned error: %v", err)
	}
	if len(postingsAdios) != 2 || postingsAdios[0].DocID != id2 || postingsAdios[1].DocID != id3 {
		t.Errorf("Postings(\"adiós\") = %v; want DocIDs %d and %d", postingsAdios, id2, id3)
	}
}
