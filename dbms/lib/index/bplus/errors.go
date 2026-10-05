package bplus

import "errors"

var (
	ErrInvalidOrder        = errors.New("bplus: order must be at least 3")
	ErrNotFound            = errors.New("bplus: entry not found")
	ErrInvalidRange        = errors.New("bplus: low key is greater than high key")
	ErrStorageMismatch     = errors.New("bplus: index/storage mismatch")
	ErrScannerRequired     = errors.New("bplus: storage does not provide the scan operation required to rebuild the index")
	ErrDuplicatePrimaryKey = errors.New("bplus: duplicate primary key")
)
