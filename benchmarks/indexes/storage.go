package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dbms-go/v2/dbms/lib/concurrency/sequential"
	"github.com/dbms-go/v2/dbms/lib/storage/heap"
)

type baseStorage struct {
	dir      string
	seqMain  string
	seqOvf   string
	heapPath string
	seq      *sequential.SeqFile
	hf       *heap.HeapFile
}

func createBaseStorage(dir string, n int, cfg config) (*baseStorage, error) {
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	bs := &baseStorage{
		dir:      dir,
		seqMain:  filepath.Join(dir, "table.seq"),
		seqOvf:   filepath.Join(dir, "table.seq.ovf"),
		heapPath: filepath.Join(dir, "table.heap"),
	}

	seq, err := sequential.Create(bs.seqMain, bs.seqOvf, cfg.seqPageCapacity, cfg.payloadSize)
	if err != nil {
		return nil, fmt.Errorf("crear sequential: %w", err)
	}
	bs.seq = seq

	hf, err := heap.Create(bs.heapPath, heap.DefaultPageSize)
	if err != nil {
		seq.Close()
		return nil, fmt.Errorf("crear heap: %w", err)
	}
	bs.hf = hf

	// El clustered storage se carga en orden. Su orden físico por clave es una
	// propiedad de la técnica y no forma parte del tiempo de construcción del índice.
	for key := int64(1); key <= int64(n); key++ {
		if _, err := seq.InsertRecord(key, payloadForKey(key, cfg.payloadSize)); err != nil {
			bs.close()
			return nil, fmt.Errorf("sequential insert key=%d: %w", key, err)
		}
	}

	// HeapFile recibe las mismas claves/payloads pero en un orden físico
	// determinista y no ordenado.
	for _, key := range shuffledKeys(n, cfg.seed+int64(n)) {
		if _, err := hf.Insert(payloadForKey(key, cfg.payloadSize)); err != nil {
			bs.close()
			return nil, fmt.Errorf("heap insert key=%d: %w", key, err)
		}
	}
	return bs, nil
}

func (bs *baseStorage) close() error {
	var first error
	if bs.seq != nil {
		if err := bs.seq.Close(); err != nil && first == nil {
			first = err
		}
		bs.seq = nil
	}
	if bs.hf != nil {
		if err := bs.hf.Close(); err != nil && first == nil {
			first = err
		}
		bs.hf = nil
	}
	return first
}
