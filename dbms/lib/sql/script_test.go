package sql

import (
	"errors"
	"strings"
	"testing"
)

// TestExecScriptVariasSentencias ejecuta un script completo y comprueba que
// devuelve un resultado por sentencia, en orden, tolerando ";" vacíos y el
// ";" final con salto de línea.
func TestExecScriptVariasSentencias(t *testing.T) {
	e := newTestEngine(t, Options{})

	res, err := e.ExecScript(
		"CREATE TABLE alumno (codigo INT PRIMARY KEY, nombre VARCHAR(40), ciclo INT);\n" +
			"\t;\n" +
			"INSERT INTO alumno VALUES (1, 'Ana', 5), (2, 'Bob', 5);;\n" +
			"SELECT * FROM alumno;\n")
	if err != nil {
		t.Fatalf("el script debía ejecutarse: %v", err)
	}
	if len(res) != 3 {
		t.Fatalf("hay %d resultados, se esperaban 3 (los ';' vacíos no cuentan)", len(res))
	}
	if res[0].Message == "" {
		t.Error("el CREATE debe devolver su mensaje")
	}
	if res[1].Affected != 2 {
		t.Errorf("el INSERT afectó %d filas, se esperaban 2", res[1].Affected)
	}
	if len(res[2].Rows) != 2 {
		t.Errorf("el SELECT devolvió %d filas, se esperaban 2", len(res[2].Rows))
	}
	// Cada sentencia conserva su ciclo de bloqueos en el plan.
	if len(res[2].Plan) == 0 || res[2].Plan[0].Kind != StepLock {
		t.Errorf("falta el paso de bloqueos en el plan de la sentencia 3:\n%s", planText(res[2]))
	}
}

// TestExecScriptLiteralConPuntoYComma comprueba que un ";" dentro de un
// literal de texto no parte la sentencia (el problema de partir el script
// con strings.Split).
func TestExecScriptLiteralConPuntoYComma(t *testing.T) {
	e := newTestEngine(t, Options{})

	_, err := e.ExecScript(
		"CREATE TABLE notas (id INT PRIMARY KEY, texto VARCHAR(40)); " +
			"INSERT INTO notas VALUES (1, 'buenas; malas'), (2, 'ok');")
	if err != nil {
		t.Fatalf("el script debía ejecutarse: %v", err)
	}

	res, err := e.ExecScript("SELECT texto FROM notas WHERE id = 1;")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if len(res[0].Rows) != 1 || res[0].Rows[0][0] != "buenas; malas" {
		t.Errorf("el literal llegó como %v, se esperaba %q", res[0].Rows, "buenas; malas")
	}
}

// TestExecScriptFallaEnLaMitad: la sentencia que falla no se ejecuta, el
// error dice cuál es (con errors.As se obtiene el índice) y las anteriores
// sí quedaron aplicadas.
func TestExecScriptFallaEnLaMitad(t *testing.T) {
	e := newTestEngine(t, Options{})
	run(t, e, "CREATE TABLE cuentas (id INT PRIMARY KEY, saldo BIGINT)")
	run(t, e, "INSERT INTO cuentas VALUES (1, 100), (2, 200)")

	res, err := e.ExecScript(
		"UPDATE cuentas SET saldo = 999 WHERE id = 1; " +
			"SELECT * FROM no_existe; " +
			"UPDATE cuentas SET saldo = 888 WHERE id = 2;")
	if err == nil {
		t.Fatal("se esperaba un error en la sentencia 2")
	}
	var se *StmtError
	if !errors.As(err, &se) {
		t.Fatalf("el error debe ser *StmtError, es %T: %v", err, err)
	}
	if se.Index != 1 || se.Total != 3 {
		t.Errorf("el error señala la sentencia %d de %d, se esperaba 2 de 3", se.Index+1, se.Total)
	}
	if !strings.Contains(err.Error(), "sentencia 2 de 3") {
		t.Errorf("el mensaje debe ubicar el fallo: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("hay %d resultados parciales, se esperaba 1", len(res))
	}

	// Lo que corrió quedó aplicado; la sentencia posterior no corrió.
	if got := saldo(t, e.def, 1); got != 999 {
		t.Errorf("la sentencia 1 no quedó aplicada: saldo %d, esperaba 999", got)
	}
	if got := saldo(t, e.def, 2); got != 200 {
		t.Errorf("la sentencia 3 no debió ejecutarse: saldo %d, esperaba 200", got)
	}
}

// TestExecScriptErrorDeSintaxisNoEjecutaNada: si una sentencia no parsea,
// no se ejecuta ninguna (el script se parsea completo antes de correr).
func TestExecScriptErrorDeSintaxisNoEjecutaNada(t *testing.T) {
	e := newTestEngine(t, Options{})

	_, err := e.ExecScript(
		"CREATE TABLE t (id INT PRIMARY KEY); FOO BAR; INSERT INTO t VALUES (1);")
	if err == nil {
		t.Fatal("se esperaba error de sintaxis")
	}
	if !strings.Contains(err.Error(), "sentencia 2") {
		t.Errorf("el error debe decir cuál sentencia falló: %v", err)
	}
	if _, err := e.Exec("SELECT * FROM t"); err == nil {
		t.Fatal("la tabla no debió crearse: ninguna sentencia corre si el script no parsea")
	}
}

// TestExecScriptTransaccionUnica: BEGIN ... COMMIT dentro de un mismo script
// corre entero en una transacción y la cierra.
func TestExecScriptTransaccionUnica(t *testing.T) {
	e := newTestEngine(t, Options{})
	seedCuentas(t, e)

	_, err := e.ExecScript(
		"BEGIN TRANSACTION; UPDATE cuentas SET saldo = 500 WHERE id = 1; COMMIT;")
	if err != nil {
		t.Fatalf("el script transaccional debía ejecutarse: %v", err)
	}
	if e.def.InTransaction() {
		t.Error("la transacción debió cerrarse con el COMMIT del script")
	}
	if got := saldo(t, e.def, 1); got != 500 {
		t.Errorf("saldo %d, se esperaba 500", got)
	}
}

// TestExecScriptVacio: solo ";" o espacios es entrada vacía, igual que para
// una sentencia única.
func TestExecScriptVacio(t *testing.T) {
	e := newTestEngine(t, Options{})
	if _, err := e.ExecScript(" ; \n ;; "); err == nil || !strings.Contains(err.Error(), "empty input") {
		t.Errorf("se esperaba empty input, el error fue %v", err)
	}
}

// TestScriptTouches clasifica scripts contra la tabla espacial: solo las
// escrituras y las sentencias que cierran transacción cambian lo visible.
func TestScriptTouches(t *testing.T) {
	e := newTestEngine(t, Options{})
	cases := []struct {
		script string
		want   bool
	}{
		{"SELECT * FROM ubicaciones;", false},
		{"INSERT INTO otra VALUES (1);", false},
		{"BEGIN TRANSACTION;", false},
		// El caso multi-sentencia que un HasPrefix sobre el texto crudo se
		// perdía: la escritura no está al principio del script.
		{"INSERT INTO otra VALUES (1); INSERT INTO ubicaciones VALUES (1, 'plaza');", true},
		{"CREATE TABLE ubicaciones (id INT PRIMARY KEY, nombre VARCHAR(30));", true},
		{"DELETE FROM ubicaciones WHERE id = 1;", true},
		{"TRUNCATE ubicaciones;", true},
		{"COMMIT;", true},
		{"SELECT * FROM ubicaciones; ROLLBACK;", true},
	}
	for _, c := range cases {
		got, err := e.ScriptTouches(c.script, "ubicaciones")
		if err != nil {
			t.Errorf("%q: %v", c.script, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q: dio %v, se esperaba %v", c.script, got, c.want)
		}
	}
}
