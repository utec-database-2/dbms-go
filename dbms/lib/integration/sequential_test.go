package integration

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/concurrency/sequential"
)

func newTestSeqFile(t *testing.T, pageCapacity, payloadSize int) *sequential.SeqFile {
	t.Helper()
	dir := t.TempDir()
	s, err := sequential.Create(filepath.Join(dir, "main.seq"), filepath.Join(dir, "ovf.seq"), pageCapacity, payloadSize)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func payload(s string) []byte { return []byte(s) }

func TestInsertAndSearch(t *testing.T) {
	s := newTestSeqFile(t, 4, 16)

	if err := s.Insert(10, payload("ten")); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, ok, err := s.Search(10)
	if err != nil || !ok {
		t.Fatalf("Search: got=%v ok=%v err=%v", got, ok, err)
	}
	if string(got) != "ten" {
		t.Fatalf("got %q, want %q", got, "ten")
	}
}

func TestSearchMissingKey(t *testing.T) {
	s := newTestSeqFile(t, 4, 16)
	if err := s.Insert(1, payload("one")); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	_, ok, err := s.Search(999)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if ok {
		t.Fatalf("expected key 999 to be missing")
	}
}

func TestInsertRejectsDuplicateKey(t *testing.T) {
	s := newTestSeqFile(t, 4, 16)
	if err := s.Insert(5, payload("a")); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.Insert(5, payload("b")); err != sequential.ErrKeyExists {
		t.Fatalf("got err=%v, want ErrKeyExists", err)
	}
}

func TestInsertMaintainsOrderWithinPage(t *testing.T) {
	s := newTestSeqFile(t, 8, 16)

	keys := []int64{5, 1, 9, 3, 7}
	for _, k := range keys {
		if err := s.Insert(k, payload(fmt.Sprintf("v%d", k))); err != nil {
			t.Fatalf("Insert %d: %v", k, err)
		}
	}

	scan, err := s.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := []int64{1, 3, 5, 7, 9}
	if len(scan) != len(want) {
		t.Fatalf("scan length = %d, want %d", len(scan), len(want))
	}
	for i, kv := range scan {
		if kv.Key != want[i] {
			t.Fatalf("scan[%d].Key = %d, want %d", i, kv.Key, want[i])
		}
	}
}

func TestOverflowChainKeepsGlobalOrder(t *testing.T) {
	// pageCapacity=2 fuerza que todo insert extra vaya a la cadena de overflow
	s := newTestSeqFile(t, 2, 16)

	keys := []int64{20, 5, 15, 1, 25, 10, 30}
	for _, k := range keys {
		if err := s.Insert(k, payload(fmt.Sprintf("v%d", k))); err != nil {
			t.Fatalf("Insert %d: %v", k, err)
		}
	}

	scan, err := s.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	prev := int64(-1 << 62)
	for _, kv := range scan {
		if kv.Key < prev {
			t.Fatalf("scan not sorted: %v", scan)
		}
		prev = kv.Key
	}
	if len(scan) != len(keys) {
		t.Fatalf("scan length = %d, want %d", len(scan), len(keys))
	}
	for _, k := range keys {
		got, ok, err := s.Search(k)
		if err != nil || !ok {
			t.Fatalf("Search(%d): ok=%v err=%v", k, ok, err)
		}
		if string(got) != fmt.Sprintf("v%d", k) {
			t.Fatalf("Search(%d) = %q, want %q", k, got, fmt.Sprintf("v%d", k))
		}
	}
}

func TestLazyDelete(t *testing.T) {
	s := newTestSeqFile(t, 4, 16)
	s.SetReorgThreshold(1.1) // disable auto reorg for this test

	for _, k := range []int64{1, 2, 3, 4} {
		if err := s.Insert(k, payload("x")); err != nil {
			t.Fatalf("Insert %d: %v", k, err)
		}
	}
	ok, err := s.Delete(2)
	if err != nil || !ok {
		t.Fatalf("Delete: ok=%v err=%v", ok, err)
	}
	_, ok, err = s.Search(2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if ok {
		t.Fatalf("expected key 2 to be gone after delete")
	}

	ok, err = s.Delete(2)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if ok {
		t.Fatalf("deleting an already-deleted key should return false")
	}

	stats := s.Stats()
	if stats.LiveCount != 3 || stats.DeadCount != 1 {
		t.Fatalf("stats = %+v, want live=3 dead=1", stats)
	}
}

func TestDeleteFromOverflowChain(t *testing.T) {
	s := newTestSeqFile(t, 2, 16)
	s.SetReorgThreshold(1.1)

	for _, k := range []int64{1, 2, 3, 4, 5} {
		if err := s.Insert(k, payload("x")); err != nil {
			t.Fatalf("Insert %d: %v", k, err)
		}
	}
	ok, err := s.Delete(4)
	if err != nil || !ok {
		t.Fatalf("Delete: ok=%v err=%v", ok, err)
	}
	scan, err := s.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, kv := range scan {
		if kv.Key == 4 {
			t.Fatalf("deleted key 4 still present in scan: %v", scan)
		}
	}
	if len(scan) != 4 {
		t.Fatalf("scan length = %d, want 4", len(scan))
	}
}

func TestRangeScan(t *testing.T) {
	s := newTestSeqFile(t, 3, 16)
	for i := int64(1); i <= 20; i++ {
		if err := s.Insert(i, payload(fmt.Sprintf("v%d", i))); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	got, err := s.RangeScan(5, 10)
	if err != nil {
		t.Fatalf("RangeScan: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("range length = %d, want 6", len(got))
	}
	for i, kv := range got {
		if kv.Key != int64(5+i) {
			t.Fatalf("range[%d].Key = %d, want %d", i, kv.Key, 5+i)
		}
	}
}

func TestAutoReorganizationOnThreshold(t *testing.T) {
	s := newTestSeqFile(t, 4, 16)
	s.SetReorgThreshold(0.30)

	for i := int64(1); i <= 10; i++ {
		if err := s.Insert(i, payload(fmt.Sprintf("v%d", i))); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	// eliminar 4 de 10 (40%) supera el umbral del 30% y dispara la reorganización
	for _, k := range []int64{1, 2, 3, 4} {
		if _, err := s.Delete(k); err != nil {
			t.Fatalf("Delete %d: %v", k, err)
		}
	}

	stats := s.Stats()
	if stats.DeadCount != 0 {
		t.Fatalf("expected reorganization to reclaim tombstones, got DeadCount=%d", stats.DeadCount)
	}
	if stats.LiveCount != 6 {
		t.Fatalf("LiveCount = %d, want 6", stats.LiveCount)
	}

	scan, err := s.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := []int64{5, 6, 7, 8, 9, 10}
	if len(scan) != len(want) {
		t.Fatalf("scan length = %d, want %d", len(scan), len(want))
	}
	for i, kv := range scan {
		if kv.Key != want[i] {
			t.Fatalf("scan[%d].Key = %d, want %d", i, kv.Key, want[i])
		}
	}
}

func TestManualReorganizeAfterDeletes(t *testing.T) {
	s := newTestSeqFile(t, 2, 16)
	s.SetReorgThreshold(1.1) // disable auto reorg, we call it manually

	for i := int64(1); i <= 6; i++ {
		if err := s.Insert(i, payload(fmt.Sprintf("v%d", i))); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	for _, k := range []int64{2, 4} {
		if _, err := s.Delete(k); err != nil {
			t.Fatalf("Delete %d: %v", k, err)
		}
	}
	if err := s.Reorganize(); err != nil {
		t.Fatalf("Reorganize: %v", err)
	}

	stats := s.Stats()
	if stats.DeadCount != 0 || stats.LiveCount != 4 {
		t.Fatalf("stats after reorganize = %+v, want live=4 dead=0", stats)
	}

	if err := s.Insert(100, payload("hundred")); err != nil {
		t.Fatalf("Insert after reorganize: %v", err)
	}
	got, ok, err := s.Search(100)
	if err != nil || !ok || string(got) != "hundred" {
		t.Fatalf("Search(100) after reorganize: got=%q ok=%v err=%v", got, ok, err)
	}
}

func TestSeqFilePersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "main.seq")
	ovfPath := filepath.Join(dir, "ovf.seq")

	s, err := sequential.Create(mainPath, ovfPath, 3, 16)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i := int64(1); i <= 9; i++ {
		if err := s.Insert(i, payload(fmt.Sprintf("v%d", i))); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	if _, err := s.Delete(5); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := sequential.Open(mainPath, ovfPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reopened.Close()

	_, ok, err := reopened.Search(5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if ok {
		t.Fatalf("deleted key 5 resurfaced after reopen")
	}
	for i := int64(1); i <= 9; i++ {
		if i == 5 {
			continue
		}
		got, ok, err := reopened.Search(i)
		if err != nil || !ok {
			t.Fatalf("Search(%d) after reopen: ok=%v err=%v", i, ok, err)
		}
		if string(got) != fmt.Sprintf("v%d", i) {
			t.Fatalf("Search(%d) after reopen = %q", i, got)
		}
	}

	if err := reopened.Insert(50, payload("fifty")); err != nil {
		t.Fatalf("Insert after reopen: %v", err)
	}
	got, ok, err := reopened.Search(50)
	if err != nil || !ok || string(got) != "fifty" {
		t.Fatalf("Search(50) after reopen: got=%q ok=%v err=%v", got, ok, err)
	}
}

func TestPayloadTooLargeRejected(t *testing.T) {
	s := newTestSeqFile(t, 4, 4)
	if err := s.Insert(1, payload("waytoobig")); err == nil {
		t.Fatalf("expected error inserting a payload larger than payloadSize")
	}
}

func TestInsertRecordScanRecordsRead(t *testing.T) {
	s := newTestSeqFile(t, 4, 16)
	want := map[int64]string{1: "uno", 2: "dos", 3: "tres", 4: "cuatro"}
	for _, k := range []int64{3, 1, 4, 2} {
		if _, err := s.InsertRecord(k, payload(want[k])); err != nil {
			t.Fatalf("InsertRecord(%d): %v", k, err)
		}
	}

	records, err := s.ScanRecords()
	if err != nil {
		t.Fatalf("ScanRecords: %v", err)
	}
	if len(records) != 4 {
		t.Fatalf("ScanRecords: got %d records, want 4", len(records))
	}
	seenRID := make(map[sequential.RecordID]bool)
	for i, r := range records {
		if seenRID[r.RID] {
			t.Fatalf("record %d: RID %v repetido", i, r.RID)
		}
		seenRID[r.RID] = true
		if string(r.Payload) != want[r.Key] {
			t.Fatalf("record %d: key=%d payload=%q, want %q", i, r.Key, r.Payload, want[r.Key])
		}
		got, err := s.Read(r.RID)
		if err != nil {
			t.Fatalf("Read(%v): %v", r.RID, err)
		}
		if string(got) != want[r.Key] {
			t.Fatalf("Read(%v): got %q, want %q", r.RID, got, want[r.Key])
		}
	}
}

func TestInsertRecordOverflowAndScan(t *testing.T) {
	s := newTestSeqFile(t, 4, 16)
	for k := int64(1); k <= 6; k++ {
		if _, err := s.InsertRecord(k, payload(fmt.Sprintf("k%d", k))); err != nil {
			t.Fatalf("InsertRecord(%d): %v", k, err)
		}
	}

	records, err := s.ScanRecords()
	if err != nil {
		t.Fatalf("ScanRecords: %v", err)
	}
	if len(records) != 6 {
		t.Fatalf("ScanRecords: got %d records, want 6", len(records))
	}
	seenRID := make(map[sequential.RecordID]bool)
	for i, r := range records {
		if seenRID[r.RID] {
			t.Fatalf("record %d: RID %v repetido", i, r.RID)
		}
		seenRID[r.RID] = true
		if want := fmt.Sprintf("k%d", int64(i)+1); string(r.Payload) != want {
			t.Fatalf("record %d: got payload %q, want %q", i, r.Payload, want)
		}
	}
}

func TestDeleteRID(t *testing.T) {
	s := newTestSeqFile(t, 4, 16)
	for _, k := range []int64{1, 2, 3, 4} {
		if _, err := s.InsertRecord(k, payload(fmt.Sprintf("k%d", k))); err != nil {
			t.Fatalf("InsertRecord(%d): %v", k, err)
		}
	}

	records, err := s.ScanRecords()
	if err != nil {
		t.Fatalf("ScanRecords: %v", err)
	}
	toDelete := records[1] // key=2
	if err := s.DeleteRID(toDelete.RID); err != nil {
		t.Fatalf("DeleteRID(%v): %v", toDelete.RID, err)
	}
	if _, ok, _ := s.Search(2); ok {
		t.Fatalf("Search(2) still ok after DeleteRID")
	}

	got, err := s.ScanRecords()
	if err != nil {
		t.Fatalf("ScanRecords: %v", err)
	}
	wantKeys := []int64{1, 3, 4}
	if len(got) != len(wantKeys) {
		t.Fatalf("after DeleteRID: got %d records, want %d", len(got), len(wantKeys))
	}
	for i, r := range got {
		if r.Key != wantKeys[i] {
			t.Fatalf("after DeleteRID: record %d key=%d, want %d", i, r.Key, wantKeys[i])
		}
	}

	if err := s.DeleteRID(sequential.RecordID(999999)); err != sequential.ErrNotFound {
		t.Fatalf("DeleteRID out of range: got %v, want ErrNotFound", err)
	}
}

// Regresión: el RID de un registro ya insertado no debe invalidarse cuando
// otro insert posterior en la MISMA página reordena las claves vivas. Antes
// el RID codificaba (página, posición-entre-claves-vivas), así que insertar
// una clave menor corría a todos los que venían después y Read/Search
// terminaban resolviendo al registro equivocado.
func TestRIDStableAcrossLaterInsertsInSamePage(t *testing.T) {
	s := newTestSeqFile(t, 4, 16)

	rid10, err := s.InsertRecord(10, payload("diez"))
	if err != nil {
		t.Fatalf("InsertRecord(10): %v", err)
	}
	if _, err := s.InsertRecord(5, payload("cinco")); err != nil {
		t.Fatalf("InsertRecord(5): %v", err)
	}
	if _, err := s.InsertRecord(1, payload("uno")); err != nil {
		t.Fatalf("InsertRecord(1): %v", err)
	}

	got, err := s.Read(rid10)
	if err != nil {
		t.Fatalf("Read(rid10): %v", err)
	}
	if string(got) != "diez" {
		t.Fatalf("Read(rid10) = %q, want %q (RID se invalidó por inserts posteriores)", got, "diez")
	}

	found, ok, err := s.Search(10)
	if err != nil || !ok {
		t.Fatalf("Search(10): ok=%v err=%v", ok, err)
	}
	if string(found) != "diez" {
		t.Fatalf("Search(10) = %q, want %q", found, "diez")
	}
}

// Regresión equivalente tras una Reorganize: el RID debe seguir resolviendo
// al mismo registro aunque su ubicación física cambie por completo.
func TestRIDStableAcrossReorganize(t *testing.T) {
	s := newTestSeqFile(t, 3, 16)
	s.SetReorgThreshold(1.1)

	rids := make(map[int64]sequential.RecordID)
	for i := int64(1); i <= 12; i++ {
		rid, err := s.InsertRecord(i, payload(fmt.Sprintf("v%d", i)))
		if err != nil {
			t.Fatalf("InsertRecord(%d): %v", i, err)
		}
		rids[i] = rid
	}
	for _, k := range []int64{2, 5, 8} {
		if _, err := s.Delete(k); err != nil {
			t.Fatalf("Delete(%d): %v", k, err)
		}
	}
	if err := s.Reorganize(); err != nil {
		t.Fatalf("Reorganize: %v", err)
	}

	for i := int64(1); i <= 12; i++ {
		if i == 2 || i == 5 || i == 8 {
			continue
		}
		got, err := s.Read(rids[i])
		if err != nil {
			t.Fatalf("Read(rid de %d) tras Reorganize: %v", i, err)
		}
		if want := fmt.Sprintf("v%d", i); string(got) != want {
			t.Fatalf("Read(rid de %d) tras Reorganize = %q, want %q", i, got, want)
		}
	}
}

// Regresión: al re-enlazar un nodo nuevo de overflow, el código escribía el
// nodo previo con deleted=false sin importar su estado real, resucitando un
// registro ya eliminado con su payload viejo. Y aunque eso se preserve, si
// Search/Delete se detienen en el primer nodo que matchea la clave (aunque
// esté eliminado) tampoco encuentran el nodo vivo que quedó más adelante en
// la cadena tras un delete + insert de la misma clave.
func TestDeleteThenReinsertSameKeyInOverflowChain(t *testing.T) {
	s := newTestSeqFile(t, 1, 16)
	s.SetReorgThreshold(1.1) // aislar el caso, sin que la reorganización lo enmascare

	if err := s.Insert(1, payload("v1")); err != nil {
		t.Fatalf("Insert(1): %v", err)
	}
	if err := s.Insert(2, payload("old-v2")); err != nil {
		t.Fatalf("Insert(2): %v", err)
	}
	if err := s.Insert(3, payload("v3")); err != nil {
		t.Fatalf("Insert(3): %v", err)
	}

	ok, err := s.Delete(2)
	if err != nil || !ok {
		t.Fatalf("Delete(2): ok=%v err=%v", ok, err)
	}

	if err := s.Insert(2, payload("new-v2")); err != nil {
		t.Fatalf("re-Insert(2): %v", err)
	}

	got, found, err := s.Search(2)
	if err != nil {
		t.Fatalf("Search(2): %v", err)
	}
	if !found {
		t.Fatalf("key 2 should be found after being deleted and reinserted")
	}
	if string(got) != "new-v2" {
		t.Fatalf("got %q, want %q (a deleted node resurrected with stale data)", got, "new-v2")
	}

	ok, err = s.Delete(2)
	if err != nil || !ok {
		t.Fatalf("final Delete(2): ok=%v err=%v", ok, err)
	}
	if _, found, err := s.Search(2); err != nil || found {
		t.Fatalf("key 2 should be gone: found=%v err=%v", found, err)
	}
}
