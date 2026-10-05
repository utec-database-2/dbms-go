package main

import (
	"encoding/binary"
	"fmt"
	"math/rand"
)

func payloadForKey(key int64, size int) []byte {
	buf := make([]byte, size)
	binary.LittleEndian.PutUint64(buf[:8], uint64(key))
	for i := 8; i < len(buf); i++ {
		buf[i] = byte((key + int64(i*31)) % 251)
	}
	return buf
}

func keyFromPayload(payload []byte) (int64, error) {
	if len(payload) < 8 {
		return 0, fmt.Errorf("payload demasiado corto: %d bytes", len(payload))
	}
	return int64(binary.LittleEndian.Uint64(payload[:8])), nil
}

func shuffledKeys(n int, seed int64) []int64 {
	keys := make([]int64, n)
	for i := range keys {
		keys[i] = int64(i + 1)
	}
	r := rand.New(rand.NewSource(seed))
	r.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
	return keys
}

func equalityKeys(n, count int, seed int64) []int64 {
	r := rand.New(rand.NewSource(seed))
	out := make([]int64, count)
	for i := range out {
		out[i] = int64(r.Intn(n) + 1)
	}
	return out
}

type keyRange struct {
	low  int64
	high int64
}

func rangeQueries(n, count, width int, seed int64) []keyRange {
	if width < 1 {
		width = 1
	}
	if width > n {
		width = n
	}
	maxStart := n - width + 1
	r := rand.New(rand.NewSource(seed))
	out := make([]keyRange, count)
	for i := range out {
		start := r.Intn(maxStart) + 1
		out[i] = keyRange{low: int64(start), high: int64(start + width - 1)}
	}
	return out
}
