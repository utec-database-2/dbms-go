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

	Selected []NameExpr
	From     TableExpr
	Closure  ASTNode
	OrderBy  *OrderBy
	All      bool
}

type CreateTable struct {
	BaseNode

	Name    string
	Columns []ColumnExpr
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

	Setted  []string
	From    NameExpr
	Closure ASTNode
}

type NameExpr struct {
	BaseNode

	Name  string
	Alias *string // Nullable
}

type TableExpr struct {
	NameExpr

	// FIXME: Join things here
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

	Name string
}

type ColumnExpr struct {
	BaseNode

	Name IdExpr
	Type IdExpr
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

	Value float32
}

type StringExpr struct {
	BaseNode

	Value string
}

type NilExpr struct {
	BaseNode
}
