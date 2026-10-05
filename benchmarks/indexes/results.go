package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

type csvSink struct {
	file *os.File
	w    *csv.Writer
}

func newCSVSink(path string, header []string) (*csvSink, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := csv.NewWriter(f)
	if err := w.Write(header); err != nil {
		f.Close()
		return nil, err
	}
	return &csvSink{file: f, w: w}, nil
}

func (s *csvSink) write(row ...string) error {
	if err := s.w.Write(row); err != nil {
		return err
	}
	s.w.Flush()
	return s.w.Error()
}

func (s *csvSink) close() error {
	s.w.Flush()
	if err := s.w.Error(); err != nil {
		s.file.Close()
		return err
	}
	return s.file.Close()
}

func i(v int) string             { return strconv.Itoa(v) }
func i64s(v int64) string        { return strconv.FormatInt(v, 10) }
func f64(v float64) string       { return strconv.FormatFloat(v, 'f', 6, 64) }
func bytesString(v int64) string { return strconv.FormatInt(v, 10) }

func fileSize(path string) (int64, error) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", path, err)
	}
	return st.Size(), nil
}
