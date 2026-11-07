package ast

import "github.com/dbms-go/v2/dbms/lib/dsl/utils"

type Operator int

const (
	OpUnknown = iota
	OpPlus
	OpSub
	OpDiv
	OpMul
	OpIn

	OpAnd
	OpOr

	OpEq
	OpNeq
	OpLt
	OpLte
	OpGt
	OpGte
)

type ASTNode interface {
	GetParent() ASTNode
	GetPosition() utils.Position
}

type BaseNode struct {
	Parent   ASTNode
	Position utils.Position
}

func (b *BaseNode) GetParent() ASTNode {
	return b.Parent
}

func (b *BaseNode) GetPosition() utils.Position {
	return b.Position
}

type Select struct {
	BaseNode

	Distinct bool
	// Selected son las expresiones de la lista de proyección: una columna simple,
	// un literal, una llamada a función o una expresión aritmética, cada una con
	// su alias opcional.
	Selected       []ASTNode
	All            bool
	From           TableExpr
	Join           []JoinExpr
	Closure        ASTNode // WHERE
	TextRankMethod string  // "bm25" o "tf_idf" tras WHERE MATCH(...) USING
	GroupBy        []ASTNode
	Having         ASTNode
	OrderBy        *OrderBy
	Limit          *Limit
}

type CreateTable struct {
	BaseNode

	Name    string
	Columns []ColumnExpr

	// Clustered indica que el almacenamiento físico se ordena por ClusterKeys
	// (archivo secuencial + B+ agrupado). Si es false la tabla usa heap file.
	Clustered   bool
	ClusterKeys []NameExpr
}

type Delete struct {
	BaseNode

	From    TableExpr
	Closure ASTNode
}

type Insert struct {
	BaseNode

	Table   TableExpr
	Columns []NameExpr
	Rows    [][]ASTNode
}

type Update struct {
	BaseNode

	Table       TableExpr
	Assignments []Assignment
	Closure     ASTNode
}

type NameExpr struct {
	BaseNode

	Name  string
	Table *string
	Alias *string // Nullable
}

type TableExpr struct {
	NameExpr

	// FIXME: Join things here
}

// AliasedExpr es un elemento de la lista de proyección con alias opcional
// (SELECT age AS años, age + 1).
type AliasedExpr struct {
	BaseNode

	Expr  ASTNode
	Alias *string
}

type OrderBy struct {
	BaseNode

	Expr       ASTNode
	Descendent bool
}

type WhereExpr struct {
	BaseNode

	Content BinaryExpr
}

type BinaryExpr struct {
	BaseNode

	Left  ASTNode
	Op    Operator
	Right ASTNode
}

type IdExpr struct {
	BaseNode

	Name  string
	Table *string
}

type ColumnExpr struct {
	BaseNode

	Name IdExpr
	Type IdExpr

	// Length es el tamaño declarado entre paréntesis: VARCHAR(64), CHAR(8).
	Length int
	// Restricciones de columna recognized por el parser.
	PrimaryKey bool
	Unique     bool
	Indexed    bool
	Nullable   bool
}

type BoolExpr struct {
	BaseNode

	Value bool
}

type IntExpr struct {
	BaseNode

	Value int
}

type FloatExpr struct {
	BaseNode

	Value float64
}

type StringExpr struct {
	BaseNode

	Value string
}

type NilExpr struct {
	BaseNode
}
type Assignment struct {
	BaseNode
	Col   NameExpr
	Value ASTNode
}

type JoinExpr struct {
	BaseNode
	Type  string // INNER, LEFT, RIGHT, FULL, CROSS
	Table TableExpr
	On    ASTNode
	Using []NameExpr
}

type Limit struct {
	BaseNode
	Count  ASTNode
	Offset ASTNode
}

type GroupBy struct {
	BaseNode
	Exprs []ASTNode
}

type Having struct {
	BaseNode
	Expr ASTNode
}

// CreateIndex crea un índice sobre una tabla:
//
//	CREATE [UNIQUE] INDEX nombre ON tabla [USING tipo] (columna[, ...])
//	[WITH (opcion = valor, ...)]
//
// Using queda vacío para el índice por defecto. Options contiene las opciones
// del índice (por ejemplo nlist, nprobe y metric para IVF).
type CreateIndex struct {
	BaseNode

	Name        string
	Table       string
	Columns     []NameExpr
	Unique      bool
	IfNotExists bool
	Using       string
	Options     map[string]ASTNode
}

type DropType uint8

const (
	DropTable DropType = iota
	DropIndex
)

type DropExpr struct {
	BaseNode
	Type DropType
	Name string
	// Table solo se usa en DROP INDEX nombre ON tabla para deshacer la
	// ambigüedad cuando dos tablas tienen un índice con el mismo nombre.
	Table    string
	IfExists bool
}

type TruncateTable struct {
	BaseNode
	Name string
}

type AlterTable struct {
	BaseNode
	Name    string
	Actions []ASTNode
}

type BeginTransaction struct {
	BaseNode
}

type Commit struct {
	BaseNode
}

type Rollback struct {
	BaseNode
	Savepoint *string
}

type Savepoint struct {
	BaseNode
	Name string
}

type Release struct {
	BaseNode
	Name string
}

// StarExpr es el asterisco de COUNT(*).
type StarExpr struct {
	BaseNode
}

type FuncCallExpr struct {
	BaseNode
	Name string
	Args []ASTNode
}
