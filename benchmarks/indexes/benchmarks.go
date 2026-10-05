package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/dbms-go/v2/dbms/lib/concurrency/sequential"
	"github.com/dbms-go/v2/dbms/lib/index/extendible"
	"github.com/dbms-go/v2/dbms/lib/storage"
	"github.com/dbms-go/v2/dbms/lib/storage/heap"
	bplus "github.com/dbms-go/v2/indexes/bplus"
)

const (
	clusteredName   = "bplus_clustered"
	unclusteredName = "bplus_unclustered"
	hashName        = "extendible_hash"
)

type liveIndexes struct {
	clusteredPath   string
	unclusteredPath string
	hashPath        string
	clustered       *bplus.ClusteredIndex
	unclustered     *bplus.UnclusteredIndex[int64]
	hash            *extendible.Index
}

func createLiveIndexes(bs *baseStorage, cfg config) (*liveIndexes, error) {
	li := &liveIndexes{
		clusteredPath:   filepath.Join(bs.dir, "clustered.idx"),
		unclusteredPath: filepath.Join(bs.dir, "unclustered.idx"),
		hashPath:        filepath.Join(bs.dir, "extendible.idx"),
	}

	var err error
	li.clustered, err = bplus.CreateClusteredIndex(li.clusteredPath, cfg.bplusOrder, bs.seq)
	if err != nil {
		return nil, err
	}
	li.unclustered, err = bplus.CreateUnclusteredIndex[int64](li.unclusteredPath, cfg.bplusOrder, bs.hf, keyFromPayload)
	if err != nil {
		li.close()
		return nil, err
	}
	li.hash, err = extendible.CreateFromStorage(li.hashPath, cfg.hashBucketSize, bs.hf, func(payload []byte) (any, error) {
		return keyFromPayload(payload)
	})
	if err != nil {
		li.close()
		return nil, err
	}
	return li, nil
}

func (li *liveIndexes) close() error {
	var first error
	if li.clustered != nil {
		if err := li.clustered.Close(); err != nil && first == nil {
			first = err
		}
		li.clustered = nil
	}
	if li.unclustered != nil {
		if err := li.unclustered.Close(); err != nil && first == nil {
			first = err
		}
		li.unclustered = nil
	}
	if li.hash != nil {
		if err := li.hash.Close(); err != nil && first == nil {
			first = err
		}
		li.hash = nil
	}
	return first
}

func runConstruction(bs *baseStorage, n int, cfg config, sink *csvSink) error {
	builders := map[string]func(string) error{
		clusteredName: func(path string) error {
			idx, err := bplus.CreateClusteredIndex(path, cfg.bplusOrder, bs.seq)
			if err != nil {
				return err
			}
			return idx.Close()
		},
		unclusteredName: func(path string) error {
			idx, err := bplus.CreateUnclusteredIndex[int64](path, cfg.bplusOrder, bs.hf, keyFromPayload)
			if err != nil {
				return err
			}
			return idx.Close()
		},
		hashName: func(path string) error {
			idx, err := extendible.CreateFromStorage(path, cfg.hashBucketSize, bs.hf, func(payload []byte) (any, error) {
				return keyFromPayload(payload)
			})
			if err != nil {
				return err
			}
			return idx.Close()
		},
	}

	names := []string{clusteredName, unclusteredName, hashName}
	for rep := 0; rep < cfg.repeats; rep++ {
		for _, name := range rotated(names, rep) {
			path := filepath.Join(bs.dir, fmt.Sprintf("build_%s_%d.idx", name, rep))
			_ = os.Remove(path)
			runtime.GC()
			start := time.Now()
			if err := builders[name](path); err != nil {
				return fmt.Errorf("construcción %s n=%d rep=%d: %w", name, n, rep, err)
			}
			elapsed := time.Since(start)
			sz, err := fileSize(path)
			if err != nil {
				return err
			}
			if err := sink.write(i(n), i(rep+1), name, f64(float64(elapsed.Nanoseconds())/1e6), bytesString(sz)); err != nil {
				return err
			}
			_ = os.Remove(path)
		}
	}
	return nil
}

func runStorage(n int, li *liveIndexes, sink *csvSink) error {
	for _, p := range []struct {
		name string
		path string
	}{
		{clusteredName, li.clusteredPath},
		{unclusteredName, li.unclusteredPath},
		{hashName, li.hashPath},
	} {
		sz, err := fileSize(p.path)
		if err != nil {
			return err
		}
		if err := sink.write(i(n), p.name, bytesString(sz), f64(float64(sz)/(1024*1024))); err != nil {
			return err
		}
	}
	return nil
}

func runEquality(bs *baseStorage, li *liveIndexes, n int, cfg config, sink *csvSink) error {
	names := []string{clusteredName, unclusteredName, hashName}
	for rep := 0; rep < cfg.repeats; rep++ {
		keys := equalityKeys(n, cfg.equalityQueries, cfg.seed+int64(n*100+rep))
		for _, name := range rotated(names, rep) {
			// Warm-up fuera del cronómetro.
			warm := minInt(20, len(keys))
			for j := 0; j < warm; j++ {
				if _, err := equalityOne(name, keys[j], bs.hf, li); err != nil {
					return err
				}
			}
			runtime.GC()
			hits := 0
			start := time.Now()
			for _, key := range keys {
				c, err := equalityOne(name, key, bs.hf, li)
				if err != nil {
					return err
				}
				hits += c
			}
			elapsed := time.Since(start)
			if hits != len(keys) {
				return fmt.Errorf("igualdad %s: hits=%d esperado=%d", name, hits, len(keys))
			}
			if err := sink.write(i(n), i(rep+1), name, i(len(keys)), f64(float64(elapsed.Nanoseconds())/1e6), f64(float64(elapsed.Nanoseconds())/1e3/float64(len(keys)))); err != nil {
				return err
			}
		}
	}
	return nil
}

func equalityOne(name string, key int64, hf *heap.HeapFile, li *liveIndexes) (int, error) {
	switch name {
	case clusteredName:
		recs, err := li.clustered.Search(key)
		return len(recs), err
	case unclusteredName:
		recs, err := li.unclustered.Search(key)
		return len(recs), err
	case hashName:
		rids, err := li.hash.Search(key)
		if err != nil {
			return 0, err
		}
		for _, rid := range rids {
			if _, err := hf.Read(rid); err != nil {
				return 0, err
			}
		}
		return len(rids), nil
	default:
		return 0, fmt.Errorf("estructura desconocida %q", name)
	}
}

func runRange(bs *baseStorage, li *liveIndexes, n int, cfg config, sink *csvSink) error {
	width := int(math.Round(float64(n) * cfg.rangeFraction))
	if width < 1 {
		width = 1
	}
	names := []string{clusteredName, unclusteredName, hashName}
	for rep := 0; rep < cfg.repeats; rep++ {
		ranges := rangeQueries(n, cfg.rangeQueries, width, cfg.seed+700000+int64(n*100+rep))
		expectedHits := width * len(ranges)
		for _, name := range rotated(names, rep) {
			runtime.GC()
			hits := 0
			start := time.Now()
			for _, q := range ranges {
				var count int
				var err error
				switch name {
				case clusteredName:
					var recs []bplus.ClusteredRecord
					recs, err = li.clustered.RangeSearch(q.low, q.high)
					count = len(recs)
				case unclusteredName:
					var recs []bplus.UnclusteredRecord[int64]
					recs, err = li.unclustered.RangeSearch(q.low, q.high)
					count = len(recs)
				case hashName:
					count, err = heapRangeScan(bs.hf, q.low, q.high)
				}
				if err != nil {
					return err
				}
				hits += count
			}
			elapsed := time.Since(start)
			if hits != expectedHits {
				return fmt.Errorf("rango %s: hits=%d esperado=%d", name, hits, expectedHits)
			}
			mode := "native_index"
			if name == hashName {
				mode = "heap_scan_fallback"
			}
			if err := sink.write(i(n), i(rep+1), name, mode, i(len(ranges)), i(width), f64(cfg.rangeFraction), f64(float64(elapsed.Nanoseconds())/1e6), f64(float64(elapsed.Nanoseconds())/1e6/float64(len(ranges)))); err != nil {
				return err
			}
		}
	}
	return nil
}

func heapRangeScan(hf *heap.HeapFile, low, high int64) (int, error) {
	count := 0
	var decodeErr error
	err := hf.Scan(func(_ storage.RID, payload []byte) bool {
		key, err := keyFromPayload(payload)
		if err != nil {
			decodeErr = err
			return false
		}
		if key >= low && key <= high {
			count++
		}
		return true
	})
	if err != nil {
		return 0, err
	}
	if decodeErr != nil {
		return 0, decodeErr
	}
	return count, nil
}

type orderedRecord struct {
	key     int64
	payload []byte
}

func runOrdering(bs *baseStorage, li *liveIndexes, n int, cfg config, sink *csvSink) error {
	names := []string{clusteredName, unclusteredName, hashName}
	for rep := 0; rep < cfg.repeats; rep++ {
		for _, name := range rotated(names, rep) {
			runtime.GC()
			start := time.Now()
			count, sortedOK, err := orderingOne(name, bs.hf, li)
			elapsed := time.Since(start)
			if err != nil {
				return err
			}
			if count != n || !sortedOK {
				return fmt.Errorf("orden %s: count=%d/%d sorted=%v", name, count, n, sortedOK)
			}
			mode := "native_index_order"
			if name == hashName {
				mode = "heap_scan_plus_sort"
			}
			if err := sink.write(i(n), i(rep+1), name, mode, f64(float64(elapsed.Nanoseconds())/1e6)); err != nil {
				return err
			}
		}
	}
	return nil
}

func orderingOne(name string, hf *heap.HeapFile, li *liveIndexes) (int, bool, error) {
	switch name {
	case clusteredName:
		recs, err := li.clustered.OrderedScan()
		if err != nil {
			return 0, false, err
		}
		for j := 1; j < len(recs); j++ {
			if recs[j-1].Key > recs[j].Key {
				return len(recs), false, nil
			}
		}
		return len(recs), true, nil
	case unclusteredName:
		recs, err := li.unclustered.OrderedScan()
		if err != nil {
			return 0, false, err
		}
		for j := 1; j < len(recs); j++ {
			if recs[j-1].Key > recs[j].Key {
				return len(recs), false, nil
			}
		}
		return len(recs), true, nil
	case hashName:
		recs := make([]orderedRecord, 0)
		var decodeErr error
		err := hf.Scan(func(_ storage.RID, payload []byte) bool {
			key, err := keyFromPayload(payload)
			if err != nil {
				decodeErr = err
				return false
			}
			recs = append(recs, orderedRecord{key: key, payload: payload})
			return true
		})
		if err != nil {
			return 0, false, err
		}
		if decodeErr != nil {
			return 0, false, decodeErr
		}
		sort.Slice(recs, func(a, b int) bool { return recs[a].key < recs[b].key })
		for j := 1; j < len(recs); j++ {
			if recs[j-1].key > recs[j].key {
				return len(recs), false, nil
			}
		}
		return len(recs), true, nil
	default:
		return 0, false, fmt.Errorf("estructura desconocida %q", name)
	}
}

func runUpdates(root string, n int, cfg config, sink *csvSink) error {
	for rep := 0; rep < cfg.repeats; rep++ {
		for _, name := range rotated([]string{clusteredName, unclusteredName, hashName}, rep) {
			dir := filepath.Join(root, fmt.Sprintf("updates_n%d_r%d_%s", n, rep, name))
			insertDur, deleteDur, err := updateOneStructure(dir, name, n, cfg)
			if err != nil {
				return fmt.Errorf("updates %s n=%d rep=%d: %w", name, n, rep, err)
			}
			if err := sink.write(i(n), i(rep+1), name, "insert", i(cfg.updates), f64(float64(insertDur.Nanoseconds())/1e6), f64(float64(insertDur.Nanoseconds())/1e3/float64(cfg.updates))); err != nil {
				return err
			}
			if err := sink.write(i(n), i(rep+1), name, "delete", i(cfg.updates), f64(float64(deleteDur.Nanoseconds())/1e6), f64(float64(deleteDur.Nanoseconds())/1e3/float64(cfg.updates))); err != nil {
				return err
			}
			_ = os.RemoveAll(dir)
		}
	}
	return nil
}

func updateOneStructure(dir, name string, n int, cfg config) (time.Duration, time.Duration, error) {
	if err := os.RemoveAll(dir); err != nil {
		return 0, 0, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, 0, err
	}

	switch name {
	case clusteredName:
		bs, err := createBaseStorage(filepath.Join(dir, "base"), n, cfg)
		if err != nil {
			return 0, 0, err
		}
		defer bs.close()
		idx, err := bplus.CreateClusteredIndex(filepath.Join(dir, "clustered.idx"), cfg.bplusOrder, bs.seq)
		if err != nil {
			return 0, 0, err
		}
		defer idx.Close()
		keys := make([]int64, cfg.updates)
		rids := make([]sequential.RecordID, cfg.updates)
		start := time.Now()
		for j := 0; j < cfg.updates; j++ {
			key := int64(n + j + 1)
			rid, err := idx.Insert(key, payloadForKey(key, cfg.payloadSize))
			if err != nil {
				return 0, 0, err
			}
			keys[j] = key
			rids[j] = rid
		}
		insertDur := time.Since(start)
		start = time.Now()
		for j := 0; j < cfg.updates; j++ {
			if err := idx.Delete(keys[j], rids[j]); err != nil {
				return 0, 0, err
			}
		}
		return insertDur, time.Since(start), nil

	case unclusteredName:
		bs, err := createBaseStorage(filepath.Join(dir, "base"), n, cfg)
		if err != nil {
			return 0, 0, err
		}
		defer bs.close()
		idx, err := bplus.CreateUnclusteredIndex[int64](filepath.Join(dir, "unclustered.idx"), cfg.bplusOrder, bs.hf, keyFromPayload)
		if err != nil {
			return 0, 0, err
		}
		defer idx.Close()
		keys := make([]int64, cfg.updates)
		rids := make([]heap.RecordID, cfg.updates)
		start := time.Now()
		for j := 0; j < cfg.updates; j++ {
			key := int64(n + j + 1)
			rid, err := idx.InsertWithKey(key, payloadForKey(key, cfg.payloadSize))
			if err != nil {
				return 0, 0, err
			}
			keys[j], rids[j] = key, rid
		}
		insertDur := time.Since(start)
		start = time.Now()
		for j := 0; j < cfg.updates; j++ {
			if err := idx.Delete(keys[j], rids[j]); err != nil {
				return 0, 0, err
			}
		}
		return insertDur, time.Since(start), nil

	case hashName:
		bs, err := createBaseStorage(filepath.Join(dir, "base"), n, cfg)
		if err != nil {
			return 0, 0, err
		}
		defer bs.close()
		idx, err := extendible.CreateFromStorage(filepath.Join(dir, "extendible.idx"), cfg.hashBucketSize, bs.hf, func(payload []byte) (any, error) {
			return keyFromPayload(payload)
		})
		if err != nil {
			return 0, 0, err
		}
		defer idx.Close()
		keys := make([]int64, cfg.updates)
		rids := make([]heap.RecordID, cfg.updates)
		start := time.Now()
		for j := 0; j < cfg.updates; j++ {
			key := int64(n + j + 1)
			rid, err := bs.hf.Insert(payloadForKey(key, cfg.payloadSize))
			if err != nil {
				return 0, 0, err
			}
			if err := idx.Insert(key, rid); err != nil {
				_ = bs.hf.Delete(rid)
				return 0, 0, err
			}
			keys[j], rids[j] = key, rid
		}
		insertDur := time.Since(start)
		start = time.Now()
		for j := 0; j < cfg.updates; j++ {
			ok, err := idx.Delete(keys[j], rids[j])
			if err != nil {
				return 0, 0, err
			}
			if !ok {
				return 0, 0, fmt.Errorf("hash no encontró key=%d rid=%v", keys[j], rids[j])
			}
			if err := bs.hf.Delete(rids[j]); err != nil {
				return 0, 0, err
			}
		}
		return insertDur, time.Since(start), nil
	default:
		return 0, 0, fmt.Errorf("estructura desconocida %q", name)
	}
}

func rotated(names []string, shift int) []string {
	out := make([]string, len(names))
	for j := range names {
		out[j] = names[(j+shift)%len(names)]
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
