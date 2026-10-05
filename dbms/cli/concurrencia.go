package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dbms-go/v2/dbms/lib/sql"
)

// Demostración de transacciones y concurrencia (parte 1, sección 2.1.4).
//
// Lanza varios hilos (goroutines), cada uno con su propia sesión del motor, que
// compiten por la misma cuenta bancaria. Muestra primero la race condition sin
// control de concurrencia y después cómo el motor la evita con transacciones,
// bloqueos 2PL estrictos y detección de deadlocks.

type demoLog struct {
	mu    sync.Mutex
	start time.Time
}

func (l *demoLog) printf(who, format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	ms := time.Since(l.start).Milliseconds()
	fmt.Printf("  [%5d ms] %-9s %s\n", ms, who, fmt.Sprintf(format, args...))
}

func runConcurrencia(args []string) {
	fs := flag.NewFlagSet("concurrencia", flag.ExitOnError)
	hilos := fs.Int("hilos", 5, "número de hilos (transacciones simultáneas)")
	depositos := fs.Int("depositos", 3, "depósitos de 10 que hace cada hilo")
	pausa := fs.Duration("pausa", 15*time.Millisecond, "pausa entre leer y escribir, para que los hilos se entrecrucen")
	dir := fs.String("dir", "", "directorio de la base (por defecto uno temporal)")
	_ = fs.Parse(args)

	base := *dir
	if base == "" {
		tmp, err := os.MkdirTemp("", "dbms-concurrencia-*")
		if err != nil {
			fatal(err)
		}
		defer os.RemoveAll(tmp)
		base = tmp
	}
	eng, err := sql.Open("concurrencia", sql.Options{Dir: base})
	if err != nil {
		fatal(err)
	}
	defer eng.Close()

	mustRun(eng, "CREATE TABLE cuentas (id INT PRIMARY KEY, titular VARCHAR(20), saldo BIGINT)")
	mustRun(eng, "CREATE TABLE movimientos (id INT PRIMARY KEY, cuenta INT, monto INT)")
	mustRun(eng, "INSERT INTO cuentas VALUES (1, 'Ana', 1000), (2, 'Bob', 1000)")

	esperado := 1000 + int64(*hilos**depositos*10)
	titulo("Demostración de concurrencia: %d hilos x %d depósitos de 10 sobre la cuenta 1 (saldo inicial 1000)", *hilos, *depositos)
	fmt.Printf("  Saldo correcto al final: %d\n", esperado)

	// ---------------------------------------------------------------
	titulo("1) SIN control de concurrencia: leer y escribir en sentencias sueltas (autocommit)")
	fmt.Println("  Cada hilo lee el saldo, espera un poco y escribe saldo+10. Las lecturas")
	fmt.Println("  se entrecruzan: varios hilos leen el mismo valor y sus escrituras se pisan.")
	resetSaldo(eng)
	log := &demoLog{start: time.Now()}
	var wg sync.WaitGroup
	for h := 1; h <= *hilos; h++ {
		wg.Add(1)
		go func(h int) {
			defer wg.Done()
			s := eng.NewSession()
			defer s.Close()
			who := fmt.Sprintf("hilo %d", h)
			for d := 0; d < *depositos; d++ {
				v := leerSaldo(s, 1)
				log.printf(who, "lee saldo = %d", v)
				time.Sleep(*pausa)
				if _, err := s.Exec(fmt.Sprintf("UPDATE cuentas SET saldo = %d WHERE id = 1", v+10)); err != nil {
					log.printf(who, "error: %v", err)
					continue
				}
				log.printf(who, "escribe saldo = %d", v+10)
			}
		}(h)
	}
	wg.Wait()
	final := leerSaldo(eng.NewSession(), 1)
	resultado(final, esperado, "race condition: se perdieron %d depósito(s) (lost update)")

	// ---------------------------------------------------------------
	titulo("2) CON transacciones: BEGIN TRANSACTION ... END TRANSACTION + bloqueos 2PL")
	fmt.Println("  El SELECT toma un bloqueo compartido (S) sobre la tabla y el UPDATE lo sube a")
	fmt.Println("  exclusivo (X). Si dos hilos leyeron a la vez, quedan esperándose: el motor")
	fmt.Println("  detecta el deadlock, aborta a uno (ROLLBACK automático) y ese hilo reintenta.")
	resetSaldo(eng)
	log = &demoLog{start: time.Now()}
	var deadlocks atomic.Int64
	for h := 1; h <= *hilos; h++ {
		wg.Add(1)
		go func(h int) {
			defer wg.Done()
			s := eng.NewSession()
			defer s.Close()
			who := fmt.Sprintf("hilo %d", h)
			for d := 0; d < *depositos; d++ {
				for intento := 1; ; intento++ {
					err := depositoTx(s, log, who, *pausa)
					if err == nil {
						break
					}
					if sql.IsDeadlock(err) {
						deadlocks.Add(1)
						log.printf(who, "DEADLOCK: fue la víctima, su transacción se deshizo; reintenta (intento %d)", intento+1)
						continue
					}
					log.printf(who, "error: %v", err)
					break
				}
			}
		}(h)
	}
	wg.Wait()
	final = leerSaldo(eng.NewSession(), 1)
	resultado(final, esperado, "")
	fmt.Printf("  Deadlocks detectados y resueltos: %d\n", deadlocks.Load())

	// ---------------------------------------------------------------
	titulo("3) CON transacciones y UPDATE atómico: SET saldo = saldo + 10")
	fmt.Println("  El UPDATE pide el bloqueo X de entrada, así que los hilos se ponen en fila")
	fmt.Println("  (esperan al COMMIT del anterior) y no hay deadlocks.")
	resetSaldo(eng)
	log = &demoLog{start: time.Now()}
	for h := 1; h <= *hilos; h++ {
		wg.Add(1)
		go func(h int) {
			defer wg.Done()
			s := eng.NewSession()
			defer s.Close()
			who := fmt.Sprintf("hilo %d", h)
			for d := 0; d < *depositos; d++ {
				mustSession(s, "BEGIN TRANSACTION")
				t0 := time.Now()
				_, err := s.Exec("UPDATE cuentas SET saldo = saldo + 10 WHERE id = 1")
				if err != nil {
					log.printf(who, "error: %v", err)
					_, _ = s.Exec("ROLLBACK")
					continue
				}
				espera := time.Since(t0).Round(time.Millisecond)
				log.printf(who, "tx %d: UPDATE saldo+10 con bloqueo X (esperó %v al COMMIT del anterior)", s.TransactionID(), espera)
				time.Sleep(*pausa)
				mustSession(s, "END TRANSACTION")
			}
		}(h)
	}
	wg.Wait()
	final = leerSaldo(eng.NewSession(), 1)
	resultado(final, esperado, "")

	// ---------------------------------------------------------------
	titulo("4) Deadlock clásico entre dos tablas")
	fmt.Println("  T1 actualiza cuentas y luego quiere insertar en movimientos; T2 hace lo")
	fmt.Println("  contrario. Cada una espera a la otra: el grafo de espera tiene un ciclo.")
	log = &demoLog{start: time.Now()}
	a, b := eng.NewSession(), eng.NewSession()
	mustSession(a, "BEGIN TRANSACTION")
	mustSession(b, "BEGIN TRANSACTION")
	ta, tb := fmt.Sprintf("T%d", a.TransactionID()), fmt.Sprintf("T%d", b.TransactionID())
	mustSession(a, "UPDATE cuentas SET saldo = saldo - 100 WHERE id = 1")
	log.printf(ta, "UPDATE cuentas (bloqueo X sobre cuentas)")
	mustSession(b, "INSERT INTO movimientos VALUES (1, 2, 50)")
	log.printf(tb, "INSERT movimientos (bloqueo X sobre movimientos)")
	doneA := make(chan error, 1)
	go func() {
		log.printf(ta, "INSERT movimientos ... espera a %s", tb)
		_, err := a.Exec("INSERT INTO movimientos VALUES (2, 1, -100)")
		doneA <- err
	}()
	time.Sleep(50 * time.Millisecond)
	log.printf(tb, "UPDATE cuentas ... cerraría el ciclo %s -> %s -> %s", tb, ta, tb)
	if _, err := b.Exec("UPDATE cuentas SET saldo = saldo + 100 WHERE id = 2"); err != nil {
		log.printf(tb, "%v", err)
	}
	if err := <-doneA; err != nil {
		log.printf(ta, "error inesperado: %v", err)
	} else {
		log.printf(ta, "obtiene el bloqueo de movimientos y continúa")
	}
	mustSession(a, "COMMIT")
	log.printf(ta, "COMMIT")
	a.Close()
	b.Close()

	// ---------------------------------------------------------------
	titulo("5) Aislamiento: no hay lecturas sucias")
	resetSaldo(eng)
	log = &demoLog{start: time.Now()}
	w, r := eng.NewSession(), eng.NewSession()
	mustSession(w, "BEGIN TRANSACTION")
	mustSession(w, "UPDATE cuentas SET saldo = 0 WHERE id = 1")
	log.printf("escritor", "tx %d: UPDATE saldo = 0 (sin confirmar)", w.TransactionID())
	leido := make(chan int64, 1)
	go func() {
		log.printf("lector", "SELECT saldo ... espera el bloqueo del escritor")
		leido <- leerSaldo(r, 1)
	}()
	time.Sleep(80 * time.Millisecond)
	mustSession(w, "ROLLBACK")
	log.printf("escritor", "ROLLBACK")
	log.printf("lector", "lee saldo = %d (el valor confirmado, nunca vio el 0)", <-leido)
	w.Close()
	r.Close()
	fmt.Println()
}

// depositoTx hace un depósito de lectura-modificación-escritura en una
// transacción y cuenta lo que pasa.
func depositoTx(s *sql.Session, log *demoLog, who string, pausa time.Duration) error {
	if _, err := s.Exec("BEGIN TRANSACTION"); err != nil {
		return err
	}
	tx := s.TransactionID()
	res, err := s.Exec("SELECT saldo FROM cuentas WHERE id = 1")
	if err != nil {
		return err
	}
	v := toInt(res.Rows[0][0])
	log.printf(who, "tx %d: lee saldo = %d (bloqueo S)", tx, v)
	time.Sleep(pausa)
	if _, err := s.Exec(fmt.Sprintf("UPDATE cuentas SET saldo = %d WHERE id = 1", v+10)); err != nil {
		return err
	}
	if _, err := s.Exec("END TRANSACTION"); err != nil {
		return err
	}
	log.printf(who, "tx %d: escribe saldo = %d y hace COMMIT", tx, v+10)
	return nil
}

func titulo(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	fmt.Printf("\n%s\n%s\n", line, strings.Repeat("─", min(len([]rune(line)), 100)))
}

func resultado(final, esperado int64, perdida string) {
	if final == esperado {
		fmt.Printf("  ✔ Saldo final = %d (correcto)\n", final)
		return
	}
	fmt.Printf("  ✘ Saldo final = %d, debía ser %d → ", final, esperado)
	fmt.Printf(perdida+"\n", (esperado-final)/10)
}

func resetSaldo(eng *sql.Engine) {
	mustRun(eng, "UPDATE cuentas SET saldo = 1000 WHERE id = 1")
}

func leerSaldo(s interface {
	Exec(string) (*sql.Result, error)
}, id int) int64 {
	res, err := s.Exec(fmt.Sprintf("SELECT saldo FROM cuentas WHERE id = %d", id))
	if err != nil {
		fatal(err)
	}
	return toInt(res.Rows[0][0])
}

func toInt(v any) int64 {
	switch x := v.(type) {
	case int32:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	}
	return 0
}

func mustRun(eng *sql.Engine, q string) {
	if _, err := eng.Exec(q); err != nil {
		fatal(fmt.Errorf("%s: %w", q, err))
	}
}

func mustSession(s *sql.Session, q string) {
	if _, err := s.Exec(q); err != nil {
		fatal(fmt.Errorf("%s: %w", q, err))
	}
}
