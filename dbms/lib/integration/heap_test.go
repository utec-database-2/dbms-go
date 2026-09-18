package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dbms-go/v2/dbms/lib/storage/heap"
)

func newTestHeap(t *testing.T, pageSize int) *heap.HeapFile {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.heap")
	h, err := heap.Create(path, pageSize)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

func TestInsertAndRead(t *testing.T) {
	h := newTestHeap(t, heap.DefaultPageSize)

	rid, err := h.Insert([]byte("hello world"))
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := h.Read(rid)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("got %q, want %q", got, "hello world")
	}
}

func TestInsertMultipleSamePage(t *testing.T) {
	h := newTestHeap(t, heap.DefaultPageSize)

	rids := make([]heap.RecordID, 5)
	for i := 0; i < 5; i++ {
		rid, err := h.Insert([]byte(fmt.Sprintf("rec-%d", i)))
		if err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
		rids[i] = rid
	}
	if h.NumPages() != 1 {
		t.Fatalf("expected 1 page, got %d", h.NumPages())
	}
	for i, rid := range rids {
		got, err := h.Read(rid)
		if err != nil {
			t.Fatalf("Read %d: %v", i, err)
		}
		want := fmt.Sprintf("rec-%d", i)
		if string(got) != want {
			t.Fatalf("record %d: got %q, want %q", i, got, want)
		}
	}
}

func TestInsertSpillsToNewPage(t *testing.T) {
	h := newTestHeap(t, 64) // página chica: se llena rápido

	payload := make([]byte, 20)
	for i := range payload {
		payload[i] = byte('a' + i%26)
	}

	var last heap.RecordID
	for i := 0; i < 4; i++ {
		rid, err := h.Insert(payload)
		if err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
		last = rid
	}
	if h.NumPages() < 2 {
		t.Fatalf("expected records to spill into >=2 pages, got %d", h.NumPages())
	}
	if _, err := h.Read(last); err != nil {
		t.Fatalf("Read last: %v", err)
	}
}

func TestDeleteThenRead(t *testing.T) {
	h := newTestHeap(t, heap.DefaultPageSize)

	rid, err := h.Insert([]byte("to be deleted"))
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := h.Delete(rid); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := h.Read(rid); err != heap.ErrNotFound {
		t.Fatalf("Read after delete: got err=%v, want ErrNotFound", err)
	}
	if err := h.Delete(rid); err != heap.ErrNotFound {
		t.Fatalf("double Delete: got err=%v, want ErrNotFound", err)
	}
}

func TestFreeSpaceReuse(t *testing.T) {
	recSize := 20
	// 56 = pageHeaderSize(4) + 2*(slotEntrySize(4)+recSize): página con espacio
	// exacto para 2 registros de este tamaño.
	pageSize := 56
	h := newTestHeap(t, pageSize)

	payload := make([]byte, recSize)
	rid1, err := h.Insert(payload)
	if err != nil {
		t.Fatalf("Insert 1: %v", err)
	}
	if _, err := h.Insert(payload); err != nil {
		t.Fatalf("Insert 2: %v", err)
	}
	if h.NumPages() != 1 {
		t.Fatalf("expected 1 page, got %d", h.NumPages())
	}

	if err := h.Delete(rid1); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	rid3, err := h.Insert(payload)
	if err != nil {
		t.Fatalf("Insert 3: %v", err)
	}
	if h.NumPages() != 1 {
		t.Fatalf("expected reused space to avoid a new page, got %d pages", h.NumPages())
	}
	if rid3.PageID != rid1.PageID {
		t.Fatalf("expected reinsert on page %d, got %d", rid1.PageID, rid3.PageID)
	}
}

func TestUpdateInPlace(t *testing.T) {
	h := newTestHeap(t, heap.DefaultPageSize)

	rid, err := h.Insert([]byte("original-value"))
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	newRid, err := h.Update(rid, []byte("short"))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if newRid != rid {
		t.Fatalf("in-place update should keep the same RecordID, got %v want %v", newRid, rid)
	}
	got, err := h.Read(rid)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != "short" {
		t.Fatalf("got %q, want %q", got, "short")
	}
}

func TestUpdateRelocatesWhenLarger(t *testing.T) {
	h := newTestHeap(t, heap.DefaultPageSize)

	rid, err := h.Insert([]byte("small"))
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	bigger := "this is a much larger payload that cannot fit in place"
	newRid, err := h.Update(rid, []byte(bigger))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := h.Read(newRid)
	if err != nil {
		t.Fatalf("Read new location: %v", err)
	}
	if string(got) != bigger {
		t.Fatalf("got %q, want %q", got, bigger)
	}
	// la reubicación puede reusar el mismo slot que acaba de liberar
	if newRid != rid {
		if _, err := h.Read(rid); err != heap.ErrNotFound {
			t.Fatalf("old slot should be tombstoned, got err=%v", err)
		}
	}
}

func TestScanVisitsAllLiveRecords(t *testing.T) {
	h := newTestHeap(t, 128)

	total := 12
	for i := 0; i < total; i++ {
		if _, err := h.Insert([]byte(fmt.Sprintf("item-%02d", i))); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	var toDelete heap.RecordID
	count := 0
	h.Scan(func(rid heap.RecordID, data []byte) bool {
		if count == 3 {
			toDelete = rid
		}
		count++
		return true
	})
	if err := h.Delete(toDelete); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	seen := 0
	h.Scan(func(rid heap.RecordID, data []byte) bool {
		seen++
		return true
	})
	if seen != total-1 {
		t.Fatalf("scan visited %d records, want %d", seen, total-1)
	}
}

func TestRecordTooLargeForPage(t *testing.T) {
	h := newTestHeap(t, 64)

	huge := make([]byte, 1024)
	if _, err := h.Insert(huge); err != heap.ErrRecordTooLarge {
		t.Fatalf("got err=%v, want ErrRecordTooLarge", err)
	}
}

func TestHeapPersistenceAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persist.heap")
	h, err := heap.Create(path, heap.DefaultPageSize)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var rids []heap.RecordID
	for i := 0; i < 5; i++ {
		rid, err := h.Insert([]byte(fmt.Sprintf("persisted-%d", i)))
		if err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
		rids = append(rids, rid)
	}
	if err := h.Delete(rids[1]); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := heap.Open(path, heap.DefaultPageSize)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reopened.Close()

	if _, err := reopened.Read(rids[1]); err != heap.ErrNotFound {
		t.Fatalf("deleted record resurfaced after reopen: err=%v", err)
	}
	for i, rid := range rids {
		if i == 1 {
			continue
		}
		got, err := reopened.Read(rid)
		if err != nil {
			t.Fatalf("Read %d after reopen: %v", i, err)
		}
		want := fmt.Sprintf("persisted-%d", i)
		if string(got) != want {
			t.Fatalf("record %d after reopen: got %q, want %q", i, got, want)
		}
	}

	rid, err := reopened.Insert([]byte("after-reopen"))
	if err != nil {
		t.Fatalf("Insert after reopen: %v", err)
	}
	got, err := reopened.Read(rid)
	if err != nil || string(got) != "after-reopen" {
		t.Fatalf("Read after reopen insert: got %q, err=%v", got, err)
	}
}

func TestOpenRejectsCorruptSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.heap")
	if err := os.WriteFile(path, make([]byte, 10), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := heap.Open(path, heap.DefaultPageSize); err == nil {
		t.Fatalf("expected error opening a file with a size that isn't a page multiple")
	}
}