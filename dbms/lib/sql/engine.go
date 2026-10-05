// Package sql es el motor del DBMS: une el DSL (lexer, parser y AST), la capa
// de almacenamiento (heap file y archivo secuencial paginado), los índices
// (B+ agrupado, B+ no agrupado y hash dinámico) y los algoritmos externos
// (external sorting con k-way merge) para ejecutar consultas SQL completas.
//
// Cada consulta devuelve, además del resultado, el Plan de ejecución que
// documenta qué acceso se eligió (índice o recorrido secuencial), en qué orden
// se aplicaron los operadores y por qué. Ese plan alimenta el panel de
// ejecución del frontend.
package sql

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/dbms-go/v2/dbms/lib/dsl/ast"
	"github.com/dbms-go/v2/dbms/lib/dsl/lexer"
	"github.com/dbms-go/v2/dbms/lib/dsl/parser"
	"github.com/dbms-go/v2/dbms/lib/lockmanager"
	"github.com/dbms-go/v2/dbms/lib/storage"
	"github.com/dbms-go/v2/dbms/lib/transaction"
)

// StepKind clasifica un paso del plan de ejecución.
type StepKind string

const (
	StepScan         StepKind = "seq-scan"
	StepIndexScan    StepKind = "index-scan"
	StepIndexSeek    StepKind = "index-seek"
	StepFilter       StepKind = "filter"
	StepProject      StepKind = "project"
	StepExternalSort StepKind = "external-sort"
	StepGroupBy      StepKind = "group-by"
	StepJoin         StepKind = "join"
	StepLimit        StepKind = "limit"
	StepInsert       StepKind = "insert"
	StepUpdate       StepKind = "update"
	StepDelete       StepKind = "delete"
	StepBegin        StepKind = "begin"
	StepCommit       StepKind = "commit"
	StepRollback     StepKind = "rollback"
	StepLock         StepKind = "lock"
)

// Step es un nodo del plan de ejecución.
type Step struct {
	Kind StepKind
	// Detail explica la decisión, por ejemplo "índice idx_users_email
	// (unclustered-bplus) para email = 'a@b.c'".
	Detail string
	// Rows es el número de filas afectadas o devueltas por el paso.
	Rows int
	// Cost es una estimación de E/S (lecturas de página) del paso.
	Cost int
}

// Result es la respuesta del motor a una consulta.
type Result struct {
	// Columns son los nombres de las columnas del resultado (vacío en DML).
	Columns []string
	// Rows contiene las filas resultantes.
	Rows [][]any
	// Affected es el número de filas modificadas por una sentencia DML.
	Affected int
	// Plan son los pasos del plan de ejecución, en orden de ejecución.
	Plan []Step
	// Message describe el efecto de sentencias sin conjunto de resultados.
	Message string
}

// Options configura el motor al abrirse.
type Options struct {
	// Dir es el directorio donde viven los archivos de las tablas.
	Dir string
	// PageSize del heap file (por defecto storage.DefaultPageSize).
	PageSize int
	// SlotSize del heap file (por defecto storage.DefaultSlotSize).
	SlotSize int
	// HeapStrategy elige la política de reutilización de espacio libre.
	HeapStrategy int
	// BPlusOrder es el orden (fanout) de los árboles B+.
	BPlusOrder int
	// HashBucketSize es la capacidad de una cubeta del hash extensible.
	HashBucketSize int
	// SortBufferSlots son los slots que caben en memoria durante un
	// external sort; al superarlos se generan runs y se hace k-way merge.
	SortBufferSlots int
	// DisableSpill obliga al external sort a mantener los runs en memoria. Por
	// defecto el sort vuelca a disco los runs que no caben en el buffer, que es
	// lo que acota la memoria al tamaño del buffer.
	DisableSpill bool
	// Newline es el separador de líneas que el lexer usa para contar posiciones.
	Newline string
}

// Engine ejecuta consultas SQL sobre el catálogo de tablas.
type Engine struct {
	cat *catalog
	opt Options
	// catalogMu serializa las escrituras del catálogo en disco para que dos
	// sentencias DDL simultáneas no se pisen el archivo temporal.
	catalogMu sync.Mutex

	// Control de concurrencia y de fallos.
	txs     *transaction.Manager
	lockTxs *lockmanager.TransactionManager
	// active y activeLk son la transacción de la sesión que está ejecutando
	// la sentencia en curso: Session.Exec los carga bajo stmtMu y los guarda
	// de vuelta al terminar (ver session.go).
	active   *transaction.Tx
	activeLk *lockmanager.Transaction
	// stmtMu serializa la ejecución de sentencias: cada sentencia es atómica
	// frente a las de otras sesiones. El aislamiento entre transacciones lo
	// dan los bloqueos de tabla, que se esperan sin tener tomado stmtMu.
	stmtMu sync.Mutex
	// def es la sesión que usa Engine.Exec; sessions son todas las abiertas.
	def        *Session
	sessionsMu sync.Mutex
	sessions   map[*Session]struct{}
	// walFound describe lo que había en el WAL al abrir la base.
	walFound transaction.Stats
	// recovery cuenta los cambios que se deshacieron al montar el motor.
	recovery RecoveryInfo
	// initErr guarda un fallo al preparar el WAL para reportarlo en la primera
	// sentencia (New no puede devolver error).
	initErr error
}

// RecoveryInfo resume lo que hizo la recuperación del WAL al abrir la base.
type RecoveryInfo struct {
	// Undone son los cambios revertidos de transacciones sin confirmar.
	Undone int
	// Committed y Aborted son las transacciones que el WAL ya daba por
	// terminadas antes de este arranque.
	Committed int
	Aborted   int
	// Skipped son registros que no se pudieron deshacer (por ejemplo, de una
	// tabla que ya no existe).
	Skipped int
}

// New crea un motor cuyo catálogo arranca vacío, sin leer el directorio. Úsalo
// para bases nuevas o en memoria; para abrir una base ya existente (y que las
// tablas sobrevivan al reinicio) usa Open.
func New(dir string, opt Options) *Engine {
	if opt.PageSize == 0 {
		opt.PageSize = storage.DefaultPageSize
	}
	if opt.SlotSize == 0 {
		opt.SlotSize = storage.DefaultSlotSize
	}
	if opt.BPlusOrder < 3 {
		opt.BPlusOrder = 32
	}
	if opt.HashBucketSize < 2 {
		opt.HashBucketSize = 8
	}
	if opt.SortBufferSlots < 1 {
		opt.SortBufferSlots = 4096
	}
	if opt.Newline == "" {
		opt.Newline = "\n"
	}
	e := &Engine{cat: newCatalog(dir), opt: opt, sessions: make(map[*Session]struct{})}
	e.def = e.NewSession()
	// El directorio de datos se crea aquí: así CREATE TABLE funciona sobre una
	// ruta que aún no existe. El error se reporta al abrir el motor con Open.
	_ = os.MkdirAll(e.dbDir(), 0o755)
	e.lockTxs = lockmanager.NewTransactionManager(lockmanager.NewLockManager())

	// El WAL se abre aquí, pero no se trunca: si el proceso anterior se cortó,
	// sus registros son los que permiten deshacer lo que quedó a medias.
	mgr, found, err := transaction.NewManager(e.walPath())
	if err != nil {
		e.initErr = err
		return e
	}
	e.txs = mgr
	e.walFound = found
	e.recovery.Committed = found.Committed
	e.recovery.Aborted = found.Aborted
	return e
}

// walPath es la ruta del write-ahead log de la base.
func (e *Engine) walPath() string {
	return filepath.Join(e.dbDir(), "wal.log")
}

// Open abre una base de datos existente en el directorio opt.Dir: lee el
// catálogo persistido y reabre cada tabla con su almacenamiento e índices. Si
// el directorio todavía no tiene catálogo, devuelve un motor vacío, listo para
// CREATE TABLE. A diferencia de New, este es el constructor que hace que los
// datos sobrevivan al reinicio.
//
//	eng, err := sql.Open("miapp", sql.Options{Dir: "./data"})
func Open(name string, opt Options) (*Engine, error) {
	e := New(name, opt)
	if err := os.MkdirAll(e.dbDir(), 0o755); err != nil {
		return nil, fmt.Errorf("sql: no se pudo crear el directorio de datos %s: %w", e.dbDir(), err)
	}
	if e.initErr != nil {
		return nil, e.initErr
	}
	if err := e.loadCatalog(); err != nil {
		e.Close()
		return nil, err
	}
	// Con las tablas ya abiertas se puede deshacer lo que quedó a medias. Solo
	// después de una recuperación limpia se hace checkpoint del log.
	if err := e.recover(); err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}

// Close cierra los archivos de las tablas. Si queda una transacción abierta se
// deshace (como haría un cliente al desconectarse) y después se hace checkpoint
// del WAL, que ya no tiene nada que recuperar.
func (e *Engine) Close() {
	e.sessionsMu.Lock()
	open := make([]*Session, 0, len(e.sessions))
	for s := range e.sessions {
		open = append(open, s)
	}
	e.sessionsMu.Unlock()
	e.stmtMu.Lock()
	for _, s := range open {
		if s.active != nil {
			s.swapIn()
			_, _ = e.execRollback(&ast.Rollback{})
			s.swapOut()
		}
	}
	e.stmtMu.Unlock()
	if e.txs != nil {
		if err := e.txs.Checkpoint(); err == nil {
			_ = e.txs.Close()
		}
	}
	for _, t := range e.cat.all() {
		for _, idx := range t.indexes {
			_ = idx.Close()
		}
		if t.heap != nil {
			_ = t.heap.Close()
		}
		if t.seq != nil {
			_ = t.seq.Close()
		}
	}
}

// Exec parsea y ejecuta una sentencia SQL en la sesión por defecto del motor.
// Para varios usuarios concurrentes cada uno usa su propia sesión (NewSession).
func (e *Engine) Exec(sql string) (*Result, error) {
	return e.def.Exec(sql)
}

// parse convierte el texto SQL en el nodo raíz del AST.
func (e *Engine) parse(sql string) (ast.ASTNode, error) {
	lex := lexer.Tokenize(sql, e.opt.Newline)
	if err := lex.Err(); err != nil {
		return nil, fmt.Errorf("sql: error léxico: %w", err)
	}
	p := parser.Parse(lex)
	if err := p.Err(); err != nil {
		return nil, fmt.Errorf("sql: error de sintaxis: %w", err)
	}
	return p.Parent(), nil
}

// execNode ejecuta una sentencia ya parseada. Se llama con stmtMu tomado y con
// la transacción de la sesión cargada en e.active.
func (e *Engine) execNode(root ast.ASTNode) (*Result, error) {
	if e.initErr != nil {
		return nil, e.initErr
	}
	switch node := root.(type) {
	case *ast.Select:
		return e.execSelect(node)
	case *ast.Insert:
		return e.execInsert(node)
	case *ast.Update:
		return e.execUpdate(node)
	case *ast.Delete:
		return e.execDelete(node)
	case *ast.BeginTransaction:
		return e.execBegin(node)
	case *ast.Commit:
		return e.execCommit(node)
	case *ast.Rollback:
		return e.execRollback(node)
	case *ast.Savepoint:
		return e.execSavepoint(node)
	case *ast.Release:
		return e.execRelease(node)
	case *ast.CreateTable:
		if err := e.rejectDDLInTx("CREATE TABLE"); err != nil {
			return nil, err
		}
		return e.execCreateTable(node)
	case *ast.CreateIndex:
		if err := e.rejectDDLInTx("CREATE INDEX"); err != nil {
			return nil, err
		}
		return e.execCreateIndex(node)
	case *ast.DropTable:
		if err := e.rejectDDLInTx("DROP TABLE"); err != nil {
			return nil, err
		}
		return e.execDropTable(node)
	case *ast.TruncateTable:
		if err := e.rejectDDLInTx("TRUNCATE"); err != nil {
			return nil, err
		}
		return e.execTruncate(node)
	default:
		return nil, fmt.Errorf("sql: sentencia no soportada %T", root)
	}
}

// rejectDDLInTx impide el DDL dentro de una transacción: el cambio de esquema
// se persiste en el catálogo, no en el WAL, así que no se podría deshacer.
func (e *Engine) rejectDDLInTx(what string) error {
	if e.active != nil {
		return fmt.Errorf("sql: %s no está permitido dentro de una transacción; haz COMMIT o ROLLBACK primero", what)
	}
	return nil
}

// ---------------------------------------------------------------------------
// DDL
// ---------------------------------------------------------------------------

// execCreateTable crea la tabla, su almacenamiento y sus índices iniciales.
// La sintaxis acepta modificadores de tabla que el parser aún no expone, así que
// el tipo de almacenamiento se decide aquí:
//
//	CREATE TABLE t (a INT PRIMARY KEY, b VARCHAR(64), INDEX ...)
//	CREATE TABLE t CLUSTERED BY (a) (...)
//
// y, si no se indica nada, la tabla usa B+ no agrupado sobre heap file, que es
// el caso por defecto del proyecto.
func (e *Engine) execCreateTable(n *ast.CreateTable) (*Result, error) {
	if _, exists := e.cat.get(n.Name); exists {
		return nil, fmt.Errorf("sql: la tabla %s ya existe", n.Name)
	}
	schema, err := buildSchema(n)
	if err != nil {
		return nil, err
	}
	schema.Name = n.Name

	tbl := &Table{Schema: schema, indexes: make(map[string]Index)}
	clustered := n.Clustered

	// Índices declarados en la definición de la tabla: PRIMARY KEY sobre tablas
	// no agrupadas (para hacer cumplible la unicidad) e INDEX/UNIQUE por columna.
	// Las columnas ya cubiertas por el índice agrupado se omiten.
	declared := make([]indexDisk, 0, len(n.Columns))
	for _, col := range n.Columns {
		name := col.Name.Name
		if _, ok := tbl.ColumnIndex(name); !ok {
			continue
		}
		if isPointType(col) {
			declared = append(declared, indexDisk{Name: rtreePrefix + "_" + strings.ToLower(name), Column: name})
			continue
		}
		if !(col.PrimaryKey || col.Unique || col.Indexed) {
			continue
		}
		if clustered && tbl.IsPrimaryKeyIndexColumn(name) {
			continue
		}
		idxName := "idx_" + strings.ToLower(name)
		if col.PrimaryKey {
			idxName = "pk_" + strings.ToLower(n.Name)
		}
		declared = append(declared, indexDisk{
			Name:   idxName,
			Column: name,
			Unique: col.Unique || col.PrimaryKey,
		})
	}

	if err := e.openStorage(tbl, schema, clustered, declared); err != nil {
		_ = tbl.Close()
		return nil, err
	}
	if err := e.cat.put(tbl); err != nil {
		_ = tbl.Close()
		return nil, err
	}
	if err := e.saveCatalog(); err != nil {
		// El esquema no queda registrado: se deshace la creación para no dejar
		// una tabla en disco que al reiniciar sería invisible.
		e.cat.drop(n.Name)
		_ = tbl.Close()
		_ = dropFiles(e.dbDir(), n.Name)
		return nil, err
	}

	plan := []Step{{
		Kind:   StepScan,
		Detail: fmt.Sprintf("create table %s sobre %s", n.Name, storageLabel(tbl)),
		Rows:   len(schema.Columns),
	}}
	for _, name := range tbl.IndexNames() {
		idx := tbl.indexes[name]
		plan = append(plan, Step{
			Kind:   StepIndexScan,
			Detail: fmt.Sprintf("índice %s sobre %s (%s) con %d entrada(s)", idx.Name(), idx.Column(), idx.Kind(), idx.Entries()),
			Rows:   idx.Entries(),
		})
	}

	return &Result{
		Message: fmt.Sprintf("tabla %s creada (%s)", n.Name, storageLabel(tbl)),
		Columns: schema.Columns,
		Plan:    plan,
	}, nil
}

// execCreateIndex crea un índice sobre una tabla existente. El tipo depende del
// nombre: pk_<tabla> agrupa (solo válido en tablas agrupadas), hash_<col> usa
// extendible hashing y cualquier otro nombre crea un B+ no agrupado.
func (e *Engine) execCreateIndex(n *ast.CreateIndex) (*Result, error) {
	tbl, err := e.requireTable(n.Table)
	if err != nil {
		return nil, err
	}
	if len(n.Columns) != 1 {
		return nil, fmt.Errorf("sql: CREATE INDEX requiere exactamente una columna (recibidas %d)", len(n.Columns))
	}
	col := n.Columns[0].Name
	if _, ok := tbl.ColumnIndex(col); !ok {
		return nil, fmt.Errorf("sql: la columna %s no existe en %s", col, tbl.Name())
	}
	if _, exists := tbl.Index(n.Name); exists {
		if n.IfNotExists {
			return &Result{Message: fmt.Sprintf("el índice %s ya existe", n.Name)}, nil
		}
		return nil, fmt.Errorf("sql: el índice %s ya existe en %s", n.Name, tbl.Name())
	}

	idx, err := e.createIndex(tbl, n.Name, col, n.Unique)
	if err != nil {
		return nil, err
	}
	if err := e.saveCatalog(); err != nil {
		tbl.dropIndex(idx.Name())
		return nil, err
	}
	return &Result{
		Message: fmt.Sprintf("índice %s creado sobre %s (%s)", idx.Name(), tbl.Name(), idx.Kind()),
		Plan: []Step{{
			Kind:   StepIndexScan,
			Detail: fmt.Sprintf("índice %s sobre %s (%s) con %d entrada(s)", idx.Name(), col, idx.Kind(), idx.Entries()),
			Rows:   idx.Entries(),
		}},
	}, nil
}

func (e *Engine) execDropTable(n *ast.DropTable) (*Result, error) {
	tbl, ok := e.cat.drop(n.Name)
	if !ok {
		if n.IfExists {
			return &Result{Message: fmt.Sprintf("la tabla %s no existe", n.Name)}, nil
		}
		return nil, fmt.Errorf("sql: la tabla %s no existe", n.Name)
	}
	if err := tbl.Close(); err != nil {
		return nil, err
	}
	if err := dropFiles(e.dbDir(), n.Name); err != nil {
		return nil, err
	}
	if err := e.saveCatalog(); err != nil {
		return nil, err
	}
	return &Result{
		Message: fmt.Sprintf("tabla %s eliminada", n.Name),
		Plan:    []Step{{Kind: StepDelete, Detail: "drop table " + n.Name}},
	}, nil
}

func (e *Engine) execTruncate(n *ast.TruncateTable) (*Result, error) {
	if _, ok := e.cat.get(n.Name); !ok {
		return nil, fmt.Errorf("sql: la tabla %s no existe", n.Name)
	}
	// TRUNCATE se implementa como un DELETE sin WHERE: se recorre el
	// almacenamiento y se borra fila por fila.
	del := &ast.Delete{From: ast.TableExpr{NameExpr: ast.NameExpr{Name: n.Name}}}
	res, err := e.execDelete(del)
	if err != nil {
		return nil, err
	}
	res.Message = fmt.Sprintf("tabla %s vaciada (%d filas)", n.Name, res.Affected)
	return res, nil
}

// buildSchema traduce las columnas del AST al esquema físico de storage.
func buildSchema(n *ast.CreateTable) (storage.Table, error) {
	schema := storage.Table{Name: n.Name}
	var keyCols []int
	seen := make(map[string]bool)

	for _, col := range n.Columns {
		name := col.Name.Name
		if name == "" {
			return storage.Table{}, fmt.Errorf("sql: columna sin nombre en %s", n.Name)
		}
		if seen[strings.ToLower(name)] {
			return storage.Table{}, fmt.Errorf("sql: columna duplicada %s", name)
		}
		seen[strings.ToLower(name)] = true

		typ, maxLen, err := sqlType(col)
		if err != nil {
			return storage.Table{}, err
		}
		pos := len(schema.Columns)
		schema.Columns = append(schema.Columns, name)
		schema.Types = append(schema.Types, typ)
		schema.MaxLen = append(schema.MaxLen, maxLen)

		if col.PrimaryKey {
			keyCols = append(keyCols, pos)
		}
	}
	if len(schema.Columns) == 0 {
		return storage.Table{}, fmt.Errorf("sql: la tabla %s no tiene columnas", n.Name)
	}

	// CLUSTERED BY (a, b) puede declarar la clave si no hay PRIMARY KEY.
	if len(keyCols) == 0 {
		for _, ck := range n.ClusterKeys {
			if pos, ok := columnPos(schema, ck.Name); ok {
				keyCols = append(keyCols, pos)
			}
		}
	}
	schema.KeyCols = keyCols
	if err := schema.Validate(); err != nil {
		return storage.Table{}, err
	}
	return schema, nil
}

// columnPos busca una columna del esquema por nombre.
func columnPos(schema storage.Table, name string) (int, bool) {
	for i, c := range schema.Columns {
		if strings.EqualFold(c, name) {
			return i, true
		}
	}
	return 0, false
}

// sqlType traduce la columna declarada al tipo físico de storage, usando el
// tamaño explícito de VARCHAR(n) cuando el parser lo capturó.
func sqlType(col ast.ColumnExpr) (storage.Type, int, error) {
	name := col.Type.Name
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "INT", "INTEGER", "INT32":
		return storage.TypeInt32, 0, nil
	case "BIGINT", "LONG", "INT64":
		return storage.TypeInt64, 0, nil
	case "BOOL", "BOOLEAN":
		return storage.TypeBool, 0, nil
	case "TEXT", "STRING":
		return storage.TypeString, 255, nil
	case pointTypeName:
		// Se guarda como texto canónico "POINT(lat lon)" y lleva un R-Tree.
		return storage.TypeString, pointMaxLen, nil
	}
	// VARCHAR(n) / VARBINARY(n): el tamaño viene en col.Length.
	up := strings.ToUpper(strings.TrimSpace(name))
	if strings.HasPrefix(up, "VARCHAR") || strings.HasPrefix(up, "VARBINARY") || strings.HasPrefix(up, "CHAR") {
		n := col.Length
		if n <= 0 {
			n = 255
		}
		if strings.HasPrefix(up, "VARBINARY") {
			return storage.TypeBytes, n, nil
		}
		return storage.TypeString, n, nil
	}
	if strings.HasPrefix(up, "BLOB") || strings.HasPrefix(up, "BYTES") {
		return storage.TypeBytes, 4096, nil
	}
	return storage.TypeInt32, 0, fmt.Errorf("sql: tipo no soportado %q", name)
}

func storageLabel(t *Table) string {
	if t.Clustered {
		return "archivo secuencial paginado (B+ agrupado)"
	}
	return "heap file (B+ no agrupado)"
}
