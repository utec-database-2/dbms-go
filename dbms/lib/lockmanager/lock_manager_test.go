package lockmanager

import (
	"runtime"
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

// TestDeadlockDetectado: T1 tiene A y pide B; T2 tiene B y pide A. La segunda
// petición cerraría el ciclo, así que se rechaza con ErrDeadlock y, al abortar
// T2, T1 obtiene B.
func TestDeadlockDetectado(t *testing.T) {
	lm := NewLockManager()
	if err := lm.Lock(1, "A", Exclusive); err != nil {
		t.Fatal(err)
	}
	if err := lm.Lock(2, "B", Exclusive); err != nil {
		t.Fatal(err)
	}

	got := make(chan error, 1)
	go func() { got <- lm.Lock(1, "B", Exclusive) }()
	// Espera a que T1 quede encolado en B.
	for len(lm.table.BuildWaitForGraph().Edges()[1]) == 0 {
		runtime.Gosched()
	}

	if err := lm.Lock(2, "A", Exclusive); err != ErrDeadlock {
		t.Fatalf("T2 debía ser la víctima del deadlock, obtuvo %v", err)
	}
	lm.AbortTransaction(2)
	if err := <-got; err != nil {
		t.Fatalf("T1 debía obtener B tras abortar T2: %v", err)
	}
	if lm.HasDeadlock() {
		t.Fatal("no debería quedar ningún ciclo")
	}
}
