package sql

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func saldo(t *testing.T, s *Session, id int) int64 {
	t.Helper()
	res, err := s.Exec("SELECT saldo FROM cuentas WHERE id = " + itoa(id))
	if err != nil {
		t.Fatalf("leer saldo: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("la cuenta %d no existe", id)
	}
	v, _ := asInt64(res.Rows[0][0])
	return v
}

func itoa(i int) string { return strconv.Itoa(i) }

func seedCuentas(t *testing.T, e *Engine) {
	t.Helper()
	run(t, e, "CREATE TABLE cuentas (id INT PRIMARY KEY, titular VARCHAR(20), saldo BIGINT)")
	run(t, e, "INSERT INTO cuentas VALUES (1, 'Ana', 1000), (2, 'Bob', 1000)")
}

// TestRaceConditionSinTransaccion reproduce un lost update: cada hilo lee el
// saldo y después escribe saldo+10 en dos sentencias sueltas (autocommit). Una
// barrera hace que todos lean antes de que nadie escriba, así que todos
// escriben 1010 y se pierden los demás depósitos.
func TestRaceConditionSinTransaccion(t *testing.T) {
	e := newTestEngine(t, Options{})
	seedCuentas(t, e)

	const hilos = 8
	var leidos, wg sync.WaitGroup
	leidos.Add(hilos)
	for i := 0; i < hilos; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := e.NewSession()
			defer s.Close()
			v := saldo(t, s, 1)
			leidos.Done()
			leidos.Wait()
			if _, err := s.Exec("UPDATE cuentas SET saldo = " + itoa(int(v)+10) + " WHERE id = 1"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := saldo(t, e.def, 1); got != 1010 {
		t.Fatalf("sin transacciones se esperaba perder depósitos (1010), quedó %d", got)
	}
}

// TestTransaccionesEvitanLaRaceCondition hace lo mismo dentro de transacciones:
// el SELECT toma un bloqueo S sobre la tabla y el UPDATE lo sube a X. Dos
// hilos que leyeron a la vez quedan esperándose (deadlock): el lockmanager lo
// detecta, aborta a uno y ese hilo reintenta. Ningún depósito se pierde.
func TestTransaccionesEvitanLaRaceCondition(t *testing.T) {
	e := newTestEngine(t, Options{})
	seedCuentas(t, e)

	const hilos, depositos = 6, 5
	var deadlocks atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < hilos; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := e.NewSession()
			defer s.Close()
			for d := 0; d < depositos; d++ {
				for {
					err := deposito(s, 1, 10)
					if err == nil {
						break
					}
					if !IsDeadlock(err) {
						t.Error(err)
						return
					}
					deadlocks.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	want := int64(1000 + hilos*depositos*10)
	if got := saldo(t, e.def, 1); got != want {
		t.Fatalf("con transacciones el saldo debe ser %d, quedó %d", want, got)
	}
	t.Logf("%d depósitos concurrentes correctos; %d deadlocks detectados y reintentados", hilos*depositos, deadlocks.Load())
}

// deposito es una transacción de lectura-modificación-escritura.
func deposito(s *Session, id, monto int) error {
	if _, err := s.Exec("BEGIN TRANSACTION"); err != nil {
		return err
	}
	res, err := s.Exec("SELECT saldo FROM cuentas WHERE id = " + itoa(id))
	if err != nil {
		return err // si fue deadlock la transacción ya se deshizo
	}
	v, _ := asInt64(res.Rows[0][0])
	if _, err := s.Exec("UPDATE cuentas SET saldo = " + itoa(int(v)+monto) + " WHERE id = " + itoa(id)); err != nil {
		return err
	}
	_, err = s.Exec("END TRANSACTION")
	return err
}

// TestAislamientoSinLecturasSucias: mientras A tiene un UPDATE sin confirmar,
// B no puede leer la tabla (espera el bloqueo). Si A hace ROLLBACK, B ve el
// valor original; nunca ve el valor intermedio.
func TestAislamientoSinLecturasSucias(t *testing.T) {
	e := newTestEngine(t, Options{})
	seedCuentas(t, e)
	a, b := e.NewSession(), e.NewSession()
	defer a.Close()
	defer b.Close()

	if _, err := a.Exec("BEGIN TRANSACTION"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Exec("UPDATE cuentas SET saldo = 0 WHERE id = 1"); err != nil {
		t.Fatal(err)
	}

	leido := make(chan int64, 1)
	go func() { leido <- saldo(t, b, 1) }()

	select {
	case v := <-leido:
		t.Fatalf("B leyó %d sin esperar el COMMIT/ROLLBACK de A (lectura sucia)", v)
	case <-time.After(100 * time.Millisecond):
		// B está esperando el bloqueo de A, como debe ser.
	}
	if _, err := a.Exec("ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if v := <-leido; v != 1000 {
		t.Fatalf("tras el ROLLBACK de A, B debía leer 1000, leyó %d", v)
	}
}

// TestDeadlockEntreDosTablas: A bloquea cuentas y luego quiere movimientos; B
// bloquea movimientos y luego quiere cuentas. El segundo en pedir cierra el
// ciclo y es abortado; el otro termina normalmente.
func TestDeadlockEntreDosTablas(t *testing.T) {
	e := newTestEngine(t, Options{})
	seedCuentas(t, e)
	run(t, e, "CREATE TABLE movimientos (id INT PRIMARY KEY, cuenta INT, monto INT)")
	a, b := e.NewSession(), e.NewSession()
	defer a.Close()
	defer b.Close()

	mustExec := func(s *Session, q string) {
		t.Helper()
		if _, err := s.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(a, "BEGIN TRANSACTION")
	mustExec(b, "BEGIN TRANSACTION")
	mustExec(a, "UPDATE cuentas SET saldo = 900 WHERE id = 1")
	mustExec(b, "INSERT INTO movimientos VALUES (1, 2, 50)")

	doneA := make(chan error, 1)
	go func() {
		_, err := a.Exec("INSERT INTO movimientos VALUES (2, 1, -100)") // espera a B
		doneA <- err
	}()
	// Espera a que A esté encolado antes de que B cierre el ciclo.
	deadline := time.Now().Add(2 * time.Second)
	for len(e.lockTxs.LockManager().Snapshot()["table#movimientos"].Waiting) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("A nunca quedó esperando el bloqueo de movimientos")
		}
		time.Sleep(time.Millisecond)
	}

	_, err := b.Exec("UPDATE cuentas SET saldo = 1100 WHERE id = 2")
	if !IsDeadlock(err) {
		t.Fatalf("B debía ser la víctima del deadlock, obtuvo %v", err)
	}
	if b.InTransaction() {
		t.Fatal("la transacción de B debía deshacerse")
	}
	if err := <-doneA; err != nil {
		t.Fatalf("A debía continuar tras abortar B: %v", err)
	}
	mustExec(a, "COMMIT")

	res := run(t, e, "SELECT id FROM movimientos")
	if len(res.Rows) != 1 || res.Rows[0][0] != int32(2) {
		t.Fatalf("solo debe quedar el movimiento de A: %v", res.Rows)
	}
	if got := saldo(t, e.def, 2); got != 1000 {
		t.Fatalf("el cambio de B se deshizo, saldo de Bob = %d", got)
	}
}
