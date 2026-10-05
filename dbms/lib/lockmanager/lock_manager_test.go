package lockmanager

import (
	"testing"
	"time"
)

func TestSharedLocksAreCompatible(t *testing.T) {
	lm := NewLockManager()

	if err := lm.Lock(1, "A", Shared); err != nil {
		t.Fatal(err)
	}
	if err := lm.Lock(2, "A", Shared); err != nil {
		t.Fatal(err)
	}
}

func TestExclusiveLockWaitsAndIsGrantedAfterUnlock(t *testing.T) {
	lm := NewLockManager()

	if err := lm.Lock(1, "A", Exclusive); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		result <- lm.Lock(2, "A", Shared)
	}()

	select {
	case err := <-result:
		t.Fatalf("lock should be waiting, got %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	if !lm.Unlock(1, "A") {
		t.Fatal("expected unlock to succeed")
	}

	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting lock was not granted")
	}
}

func TestUnlockAll(t *testing.T) {
	lm := NewLockManager()

	if err := lm.Lock(1, "A", Exclusive); err != nil {
		t.Fatal(err)
	}
	if err := lm.Lock(1, "B", Shared); err != nil {
		t.Fatal(err)
	}

	lm.UnlockAll(1)

	snapshot := lm.Snapshot()
	if len(snapshot) != 0 {
		t.Fatalf("expected empty lock table, got %d resources", len(snapshot))
	}
}

func TestTransactionTwoPhaseLocking(t *testing.T) {
	lm := NewLockManager()
	tm := NewTransactionManager(lm)
	tx := tm.Begin()

	if err := tx.Lock("A", Shared); err != nil {
		t.Fatal(err)
	}
	if err := tx.Unlock("A"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Lock("B", Shared); err != ErrTransactionNotGrowing {
		t.Fatalf("expected growing-phase error, got %v", err)
	}
}

func TestUpgrade(t *testing.T) {
	lm := NewLockManager()
	if err := lm.Lock(1, "A", Shared); err != nil {
		t.Fatal(err)
	}
	if err := lm.Lock(1, "A", Exclusive); err != nil {
		t.Fatal(err)
	}

	snapshot := lm.Snapshot()
	if snapshot["A"].Granted[0].Mode != Exclusive {
		t.Fatal("expected upgraded X lock")
	}
}
