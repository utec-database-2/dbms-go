package parser

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/dbms-go/v2/dbms/lib/dsl/ast"
	"github.com/dbms-go/v2/dbms/lib/dsl/lexer"
	"github.com/dbms-go/v2/dbms/lib/dsl/token"
)

type ParserContext struct {
	parent ast.ASTNode
	idx    int
	err    error
	tokens []token.Token
}

func (parser *ParserContext) CurrToken() token.Token {
	if parser.idx >= len(parser.tokens) {
		return token.CreateToken(token.TokenEnd, "", 0, 0)
	}
	return parser.tokens[parser.idx]
}

func (parser *ParserContext) CurrStr() string {
	return parser.CurrToken().GetContent()
}

func (parser *ParserContext) HasNext() bool {
	return parser.idx < len(parser.tokens)
}

func (parser *ParserContext) IsEnd() bool {
	if !parser.HasNext() {
		return true
	}
	return parser.CurrToken().GetType() == token.TokenEnd
}

func (parser *ParserContext) Err() error {
	return parser.err
}

func (parser *ParserContext) Parent() ast.ASTNode {
	return parser.parent
}

func (parser *ParserContext) Match(t token.TokenType) bool {
	if parser.Check(t) {
		parser.idx++
		return true
	}
	return false
}

func (parser *ParserContext) Check(t token.TokenType) bool {
	return parser.CurrToken().GetType() == t
}

// Peek devuelve el tipo del token n posiciones adelante sin avanzar.
func (parser *ParserContext) Peek(n int) token.TokenType {
	if parser.idx+n >= len(parser.tokens) {
		return token.TokenEnd
	}
	return parser.tokens[parser.idx+n].GetType()
}

func (parser *ParserContext) Required(t token.TokenType) (token.Token, error) {
	if parser.Check(t) {
		tok := parser.CurrToken()
		parser.idx++
		return tok, nil
	}
	return token.CreateToken(token.TokenUnknown, "", 0, 0),
		fmt.Errorf("expected %v, got %q", t, parser.CurrStr())
}

func Parse(lexer lexer.LexerContext) ParserContext {
	ctx := ParserContext{tokens: lexer.Tokens}

	switch ctx.CurrToken().GetType() {
	case token.TokenSelect:
		ctx.parent = ctx.ParseSelect()
	case token.TokenInsert:
		ctx.parent = ctx.ParseInsert()
	case token.TokenDelete:
		ctx.parent = ctx.ParseDelete()
	case token.TokenCreate:
		if ctx.Peek(1) == token.TokenIndex || ctx.Peek(1) == token.TokenUnique {
			ctx.parent = ctx.parseCreateIndex()
		} else {
			ctx.parent = ctx.ParseCreate()
		}
	case token.TokenUpdate:
		ctx.parent = ctx.ParseUpdate()
	case token.TokenDrop:
		ctx.parent = ctx.ParseDrop()
	case token.TokenTruncate:
		ctx.parent = ctx.ParseTruncate()
	case token.TokenAlter:
		ctx.parent = ctx.ParseAlter()
	case token.TokenBegin:
		ctx.parent = ctx.ParseBegin()
	case token.TokenCommit:
		ctx.parent = ctx.ParseCommit()
	case token.TokenRollback:
		ctx.parent = ctx.ParseRollback()
	case token.TokenSavepoint:
		ctx.parent = ctx.ParseSavepoint()
	case token.TokenRelease:
		ctx.parent = ctx.ParseRelease()
	case token.TokenEnd:
		ctx.err = errors.New("empty input")
	default:
		ctx.err = fmt.Errorf("unexpected token %q", ctx.CurrStr())
	}

	if ctx.err == nil {
		ctx.Match(token.TokenSemicolon)
		if !ctx.IsEnd() {
			ctx.err = fmt.Errorf("unexpected trailing token %q", ctx.CurrStr())
		}
	}

	return ctx
}

func (parser *ParserContext) ParseNameIndex() *ast.NameExpr {
	expr := &ast.NameExpr{}
	if !parser.Check(token.TokenId) {
		return expr
	}
	id1 := parser.CurrToken()
	parser.idx++
	expr.Name = id1.GetContent()
	if parser.Match(token.TokenDot) {
		if parser.Check(token.TokenId) || parser.Check(token.TokenAsterisk) {
			if parser.Check(token.TokenAsterisk) {
				expr.Table = &expr.Name
				expr.Name = "*"
				parser.idx++
			} else {
				tok := parser.CurrToken()
				parser.idx++
				expr.Table = &expr.Name
				expr.Name = tok.GetContent()
			}
		}
	}
	if parser.Match(token.TokenAs) {
		if parser.Check(token.TokenId) {
			alias := parser.CurrToken()
			parser.idx++
			body := alias.GetContent()
			expr.Alias = &body
		}
	}
	return expr
}

func (parser *ParserContext) ParseTableExpr() *ast.TableExpr {
	n := parser.ParseNameIndex()
	t := &ast.TableExpr{}
	t.Alias = n.Alias
	t.Name = n.Name
	return t
}

func (parser *ParserContext) ParseId() *ast.IdExpr {
	if !parser.Check(token.TokenId) {
		return &ast.IdExpr{}
	}
	id1 := parser.CurrToken()
	parser.idx++
	expr := &ast.IdExpr{Name: id1.GetContent()}
	if parser.Match(token.TokenDot) {
		if parser.Check(token.TokenId) {
			tok := parser.CurrToken()
			parser.idx++
			expr.Table = &expr.Name
			expr.Name = tok.GetContent()
		}
	}
	return expr
}

func (parser *ParserContext) ParseBool() *ast.BoolExpr {
	if parser.Match(token.TokenTrue) {
		return &ast.BoolExpr{Value: true}
	}
	if parser.Match(token.TokenFalse) {
		return &ast.BoolExpr{Value: false}
	}
	return &ast.BoolExpr{}
}

func (parser *ParserContext) ParseInt() *ast.IntExpr {
	t, err := parser.Required(token.TokenInt)
	if err != nil {
		return &ast.IntExpr{}
	}
	v, _ := strconv.Atoi(t.GetContent())
	return &ast.IntExpr{Value: v}
}

func (parser *ParserContext) ParseFloat() *ast.FloatExpr {
	t, err := parser.Required(token.TokenDecimal)
	if err != nil {
		return &ast.FloatExpr{}
	}
	v, _ := strconv.ParseFloat(t.GetContent(), 32)
	return &ast.FloatExpr{Value: float32(v)}
}

func (parser *ParserContext) ParseString() *ast.StringExpr {
	t, err := parser.Required(token.TokenString)
	if err != nil {
		return &ast.StringExpr{}
	}
	return &ast.StringExpr{Value: t.GetContent()}
}

func (parser *ParserContext) ParseFuncCall(name string) *ast.FuncCallExpr {
	fn := &ast.FuncCallExpr{Name: name}
	parser.Match(token.TokenLparent)
	for !parser.Check(token.TokenRparent) && !parser.IsEnd() {
		v, err := parser.ParseValue()
		if err != nil {
			break
		}
		fn.Args = append(fn.Args, v)
		if !parser.Match(token.TokenComma) {
			break
		}
	}
	parser.Match(token.TokenRparent)
	return fn
}

// ParseValue parsea un valor o una expresión aritmética: un operando seguido de
// operadores + - * / se convierte en un árbol de BinaryExpr.
func (parser *ParserContext) ParseValue() (ast.ASTNode, error) {
	left, err := parser.parseOperand()
	if err != nil {
		return nil, err
	}
	for {
		var op ast.Operator
		switch parser.CurrToken().GetType() {
		case token.TokenPlus:
			op = ast.OpPlus
		case token.TokenMinus:
			op = ast.OpSub
		case token.TokenAsterisk:
			op = ast.OpMul
		case token.TokenDiv:
			op = ast.OpDiv
		default:
			return left, nil
		}
		parser.idx++
		right, err := parser.parseOperand()
		if err != nil {
			return left, nil
		}
		left = &ast.BinaryExpr{Left: left, Op: op, Right: right}
	}
}

func (parser *ParserContext) parseOperand() (ast.ASTNode, error) {
	switch parser.CurrToken().GetType() {
	case token.TokenAsterisk:
		parser.idx++
		return &ast.StarExpr{}, nil
	case token.TokenId:
		// Un identificador seguido de '(' es una llamada a función genérica
		// (COUNT, SUM, UPPER, ...), no una columna.
		if parser.Peek(1) == token.TokenLparent {
			name := strings.ToLower(parser.CurrStr())
			parser.idx++
			return parser.ParseFuncCall(name), nil
		}
		return parser.ParseId(), nil
	case token.TokenTrue, token.TokenFalse:
		return parser.ParseBool(), nil
	case token.TokenInt:
		return parser.ParseInt(), nil
	case token.TokenDecimal:
		return parser.ParseFloat(), nil
	case token.TokenString:
		return parser.ParseString(), nil
	case token.TokenNull:
		parser.idx++
		return &ast.NilExpr{}, nil
	case token.TokenSTArea, token.TokenSTLength, token.TokenSTDistance, token.TokenSTIntersects, token.TokenSTContains, token.TokenSTWithin, token.TokenSTAsText, token.TokenSTGeomFromText:
		name := strings.ToLower(parser.CurrStr())
		parser.idx++
		return parser.ParseFuncCall(name), nil
	case token.TokenMinus, token.TokenPlus:
		parser.idx++
		v, err := parser.ParseValue()
		if err != nil {
			return nil, err
		}
		return v, nil
	case token.TokenLparent:
		parser.idx++
		v, err := parser.ParseValue()
		if err != nil {
			parser.Match(token.TokenRparent)
			return nil, err
		}
		parser.Match(token.TokenRparent)
		return v, nil
	}
	return nil, fmt.Errorf("expected value, got %q", parser.CurrStr())
}

func tokenToOperator(t token.TokenType) ast.Operator {
	switch t {
	case token.TokenPlus:
		return ast.OpPlus
	case token.TokenMinus:
		return ast.OpSub
	case token.TokenDiv:
		return ast.OpDiv
	case token.TokenAsterisk:
		return ast.OpMul
	case token.TokenIn:
		return ast.OpIn
	case token.TokenAnd:
		return ast.OpAnd
	case token.TokenOr:
		return ast.OpOr
	case token.TokenEq, token.TokenAssign:
		return ast.OpEq
	case token.TokenNeq:
		return ast.OpNeq
	case token.TokenLt:
		return ast.OpLt
	case token.TokenLte:
		return ast.OpLte
	case token.TokenGt:
		return ast.OpGt
	case token.TokenGte:
		return ast.OpGte
	}
	return ast.OpUnknown
}

func (parser *ParserContext) comparisonOp() ast.Operator {
	switch parser.CurrToken().GetType() {
	case token.TokenEq, token.TokenAssign, token.TokenNeq,
		token.TokenLt, token.TokenLte, token.TokenGt, token.TokenGte:
		return tokenToOperator(parser.CurrToken().GetType())
	}
	return ast.OpUnknown
}

func (parser *ParserContext) parseAndCompare() (ast.ASTNode, error) {
	left, err := parser.ParseValue()
	if err != nil {
		return nil, err
	}
	if op := parser.comparisonOp(); op != ast.OpUnknown {
		parser.idx++
		right, err := parser.ParseValue()
		if err != nil {
			return nil, err
		}
		left = &ast.BinaryExpr{Left: left, Op: op, Right: right}
	}
	return left, nil
}

func (parser *ParserContext) ParseCondition() (ast.ASTNode, error) {
	left, err := parser.parseAndCompare()
	if err != nil {
		return nil, err
	}
	for {
		var op ast.Operator
		switch {
		case parser.Match(token.TokenAnd):
			op = ast.OpAnd
		case parser.Match(token.TokenOr):
			op = ast.OpOr
		default:
			return left, nil
		}

		right, err := parser.parseAndCompare()
		if err != nil {
			return nil, err
		}
		left = &ast.BinaryExpr{Left: left, Op: op, Right: right}
	}
}

func (parser *ParserContext) ParseWhereExpr() *ast.WhereExpr {
	_, err := parser.Required(token.TokenWhere)
	if err != nil {
		return nil
	}

	cond, err := parser.ParseCondition()
	if err != nil {
		return nil
	}

	expr := &ast.WhereExpr{}
	if bin, ok := cond.(*ast.BinaryExpr); ok {
		expr.Content = *bin
	}
	return expr
}

func (parser *ParserContext) ParseSelect() *ast.Select {
	parser.Match(token.TokenSelect)
	node := &ast.Select{}

	node.Distinct = parser.Match(token.TokenDistinct)

	if parser.Match(token.TokenAsterisk) {
		node.All = true
	} else {
		for !parser.Check(token.TokenFrom) && !parser.Check(token.TokenWhere) && !parser.IsEnd() && parser.CurrToken().GetType() != token.TokenSemicolon {
			if parser.Match(token.TokenComma) {
				continue
			}
			v, err := parser.ParseValue()
			if err != nil {
				break
			}
			item := ast.ASTNode(v)
			if parser.Match(token.TokenAs) {
				if alias, err := parser.Required(token.TokenId); err == nil {
					name := alias.GetContent()
					item = &ast.AliasedExpr{Expr: v, Alias: &name}
				}
			}
			node.Selected = append(node.Selected, item)
			if !parser.Match(token.TokenComma) {
				break
			}
		}
	}

	if parser.Match(token.TokenFrom) {
		node.From = *parser.ParseTableExpr()
	}

	for {
		switch parser.CurrToken().GetType() {
		case token.TokenJoin, token.TokenInner, token.TokenLeft, token.TokenRight, token.TokenFull, token.TokenCross:
			j := parser.parseJoin()
			if j != nil {
				node.Join = append(node.Join, *j)
			}
			continue
		}
		break
	}

	if parser.Check(token.TokenWhere) {
		node.Closure = parser.ParseWhereExpr()
	}

	if parser.Match(token.TokenGroup) {
		parser.Match(token.TokenBy)
		for !parser.Check(token.TokenHaving) && !parser.Check(token.TokenOrder) && !parser.Check(token.TokenLimit) && !parser.IsEnd() && parser.CurrToken().GetType() != token.TokenSemicolon {
			if parser.Match(token.TokenComma) {
				continue
			}
			v, err := parser.ParseValue()
			if err != nil {
				break
			}
			node.GroupBy = append(node.GroupBy, v)
			if !parser.Match(token.TokenComma) {
				break
			}
		}
	}

	if parser.Match(token.TokenHaving) {
		cond, err := parser.ParseCondition()
		if err == nil {
			node.Having = cond
		}
	}

	if parser.Match(token.TokenOrder) {
		parser.Match(token.TokenBy)
		v, err := parser.ParseValue()
		if err == nil {
			order := &ast.OrderBy{Expr: v}
			if parser.Check(token.TokenId) {
				dir := strings.ToLower(parser.CurrStr())
				if dir == "asc" || dir == "desc" {
					order.Descendent = dir == "desc"
					parser.idx++
				}
			}
			node.OrderBy = order
		}
	}

	node.Limit = parser.parseLimit()

	return node
}

func (parser *ParserContext) ParseInsert() *ast.Insert {
	parser.Match(token.TokenInsert)
	parser.Match(token.TokenInto)
	node := &ast.Insert{}
	node.Table = *parser.ParseTableExpr()

	if parser.Match(token.TokenLparent) {
		for parser.Check(token.TokenId) {
			id, err := parser.Required(token.TokenId)
			if err != nil {
				break
			}
			node.Columns = append(node.Columns, ast.NameExpr{Name: id.GetContent()})
			if !parser.Match(token.TokenComma) {
				break
			}
		}
		parser.Required(token.TokenRparent)
	}

	if parser.Match(token.TokenValues) {
		for {
			parser.Required(token.TokenLparent)
			var row []ast.ASTNode
			for !parser.Check(token.TokenRparent) {
				v, err := parser.ParseValue()
				if err != nil {
					break
				}
				row = append(row, v)
				if !parser.Match(token.TokenComma) {
					break
				}
			}
			parser.Required(token.TokenRparent)
			node.Rows = append(node.Rows, row)
			if !parser.Match(token.TokenComma) {
				break
			}
		}
	}

	return node
}

func (parser *ParserContext) ParseDelete() *ast.Delete {
	parser.Match(token.TokenDelete)
	node := &ast.Delete{}

	parser.Match(token.TokenFrom)
	node.From = *parser.ParseTableExpr()

	if parser.Check(token.TokenWhere) {
		node.Closure = parser.ParseWhereExpr()
	}

	return node
}

func (parser *ParserContext) ParseColumnExpr() *ast.ColumnExpr {
	id, err := parser.Required(token.TokenId)
	if err != nil {
		return nil
	}

	col := &ast.ColumnExpr{Name: ast.IdExpr{Name: id.GetContent()}, Nullable: true}

	switch parser.CurrToken().GetType() {
	case token.TokenId, token.TokenInt, token.TokenDecimal, token.TokenString:
		ttype := parser.CurrToken()
		parser.idx++
		col.Type = ast.IdExpr{Name: ttype.GetContent()}
	}

	// Tamaño declarado: VARCHAR(64), CHAR(8), DECIMAL(10,2).
	if parser.Match(token.TokenLparent) {
		if n, err := parser.Required(token.TokenInt); err == nil {
			col.Length, _ = strconv.Atoi(n.GetContent())
		}
		for parser.Match(token.TokenComma) {
			if _, err := parser.Required(token.TokenInt); err != nil {
				break
			}
		}
		parser.Match(token.TokenRparent)
	}

	// Restricciones de columna, en cualquier orden y repetibles.
	for {
		switch {
		case parser.Check(token.TokenPrimary):
			parser.idx++
			parser.Match(token.TokenKey)
			col.PrimaryKey = true
			col.Nullable = false
		case parser.Check(token.TokenUnique):
			parser.idx++
			col.Unique = true
		case parser.Check(token.TokenNot) && parser.Peek(1) == token.TokenNull:
			parser.idx += 2
			col.Nullable = false
		case parser.Check(token.TokenNull):
			parser.idx++
			col.Nullable = true
		case parser.Check(token.TokenIndex) || parser.Check(token.TokenKey):
			parser.idx++
			col.Indexed = true
		default:
			return col
		}
	}
}

func (parser *ParserContext) ParseCreate() *ast.CreateTable {
	pendingClustered := false
	parser.Match(token.TokenCreate)

	// CREATE CLUSTERED TABLE y CREATE TABLE ... CLUSTERED BY (a) se aceptan:
	// el primero decide antes del nombre, el segundo después.
	if parser.Check(token.TokenClustered) {
		parser.idx++
		pendingClustered = true
	}
	parser.Match(token.TokenTable)

	name, err := parser.Required(token.TokenId)
	if err != nil {
		return nil
	}
	node := &ast.CreateTable{Name: name.GetContent(), Clustered: pendingClustered}

	// CLUSTERED BY (a, b) fuerza almacenamiento agrupado por esas columnas.
	// CLUSTERED a secas agrupa por la clave primaria.
	if parser.Check(token.TokenClustered) {
		parser.idx++
		node.Clustered = true
		if parser.Match(token.TokenBy) && parser.Match(token.TokenLparent) {
			for parser.Check(token.TokenId) {
				n, err := parser.Required(token.TokenId)
				if err != nil {
					break
				}
				node.ClusterKeys = append(node.ClusterKeys, ast.NameExpr{Name: n.GetContent()})
				if !parser.Match(token.TokenComma) {
					break
				}
			}
			parser.Match(token.TokenRparent)
		}
	}

	if parser.Match(token.TokenLparent) {
		for parser.Check(token.TokenId) {
			col := parser.ParseColumnExpr()
			if col == nil {
				break
			}
			node.Columns = append(node.Columns, *col)
			if !parser.Match(token.TokenComma) {
				break
			}
		}
		parser.Required(token.TokenRparent)
	}

	// Restricciones de tabla: PRIMARY KEY (a, b), UNIQUE (a), CONSTRAINT ...,
	// que se aplican sobre las columnas ya declaradas.
	for {
		switch {
		case parser.Check(token.TokenPrimary):
			parser.idx++
			parser.Match(token.TokenKey)
			for _, name := range parser.parseKeyColumns(node) {
				markPrimaryKey(node, name)
			}
		case parser.Check(token.TokenUnique):
			parser.idx++
			for _, name := range parser.parseKeyColumns(node) {
				markUnique(node, name)
			}
		case parser.Check(token.TokenConstraint):
			parser.idx++
			if _, err := parser.Required(token.TokenId); err != nil {
				return node
			}
			continue
		default:
			return node
		}
	}
}

// parseCreateIndex lee CREATE [UNIQUE] INDEX nombre ON tabla (columna[, ...]).
func (parser *ParserContext) parseCreateIndex() *ast.CreateIndex {
	parser.Match(token.TokenCreate)
	node := &ast.CreateIndex{}
	if parser.Match(token.TokenUnique) {
		node.Unique = true
	}
	parser.Match(token.TokenIndex)
	if parser.Check(token.TokenIf) && parser.Peek(1) == token.TokenNot {
		parser.idx += 3 // IF NOT EXISTS
		node.IfNotExists = true
	}
	name, err := parser.Required(token.TokenId)
	if err != nil {
		return node
	}
	node.Name = name.GetContent()

	if !parser.Match(token.TokenOn) {
		return node
	}
	tbl, err := parser.Required(token.TokenId)
	if err != nil {
		return node
	}
	node.Table = tbl.GetContent()

	if parser.Match(token.TokenLparent) {
		for parser.Check(token.TokenId) {
			col, err := parser.Required(token.TokenId)
			if err != nil {
				break
			}
			node.Columns = append(node.Columns, ast.NameExpr{Name: col.GetContent()})
			if !parser.Match(token.TokenComma) {
				break
			}
		}
		parser.Match(token.TokenRparent)
	}
	return node
}

// parseKeyColumns lee la lista de columnas de una restricción de tabla.
func (parser *ParserContext) parseKeyColumns(node *ast.CreateTable) []string {
	var names []string
	if !parser.Match(token.TokenLparent) {
		return names
	}
	for parser.Check(token.TokenId) {
		n, err := parser.Required(token.TokenId)
		if err != nil {
			break
		}
		names = append(names, n.GetContent())
		if !parser.Match(token.TokenComma) {
			break
		}
	}
	parser.Match(token.TokenRparent)
	return names
}

func markPrimaryKey(node *ast.CreateTable, name string) {
	for i := range node.Columns {
		if strings.EqualFold(node.Columns[i].Name.Name, name) {
			node.Columns[i].PrimaryKey = true
			node.Columns[i].Nullable = false
		}
	}
}

func markUnique(node *ast.CreateTable, name string) {
	for i := range node.Columns {
		if strings.EqualFold(node.Columns[i].Name.Name, name) {
			node.Columns[i].Unique = true
		}
	}
}

func (parser *ParserContext) ParseUpdate() *ast.Update {
	node := &ast.Update{}
	parser.Match(token.TokenUpdate)
	tbl := parser.ParseTableExpr()
	node.Table = *tbl
	if parser.Match(token.TokenSet) {
		for {
			n := parser.ParseNameIndex()
			if n == nil || n.Name == "" {
				break
			}
			parser.Match(token.TokenAssign)
			v, err := parser.ParseValue()
			if err != nil {
				break
			}
			node.Assignments = append(node.Assignments, ast.Assignment{Col: *n, Value: v})
			if !parser.Match(token.TokenComma) {
				break
			}
		}
	}
	if parser.Check(token.TokenWhere) {
		node.Closure = parser.ParseWhereExpr()
	}
	return node
}
func (parser *ParserContext) parseJoin() *ast.JoinExpr {
	join := &ast.JoinExpr{}
	switch parser.CurrToken().GetType() {
	case token.TokenInner:
		join.Type = "INNER"
		parser.idx++
		parser.Match(token.TokenJoin)
	case token.TokenLeft:
		join.Type = "LEFT"
		parser.idx++
		parser.Match(token.TokenJoin)
	case token.TokenRight:
		join.Type = "RIGHT"
		parser.idx++
		parser.Match(token.TokenJoin)
	case token.TokenFull:
		join.Type = "FULL"
		parser.idx++
		parser.Match(token.TokenJoin)
	case token.TokenCross:
		join.Type = "CROSS"
		parser.idx++
		parser.Match(token.TokenJoin)
	case token.TokenJoin:
		join.Type = "INNER"
		parser.idx++
	default:
		return nil
	}
	join.Table = *parser.ParseTableExpr()
	if parser.Match(token.TokenOn) {
		if cond, err := parser.ParseCondition(); err == nil {
			join.On = cond
		}
	} else if parser.Match(token.TokenUsing) {
		parser.Match(token.TokenLparent)
		for !parser.Check(token.TokenRparent) && !parser.IsEnd() {
			n := parser.ParseId()
			join.Using = append(join.Using, ast.NameExpr{Name: n.Name})
			if !parser.Match(token.TokenComma) {
				break
			}
		}
		parser.Match(token.TokenRparent)
	}
	return join
}

func (parser *ParserContext) parseLimit() *ast.Limit {
	if !parser.Match(token.TokenLimit) {
		return nil
	}
	l := &ast.Limit{}
	v, err := parser.ParseValue()
	if err == nil {
		l.Count = v
	}
	if parser.Match(token.TokenOffset) {
		if ov, err := parser.ParseValue(); err == nil {
			l.Offset = ov
		}
	}
	return l
}
func (parser *ParserContext) ParseDrop() *ast.DropTable {
	parser.Match(token.TokenDrop)
	parser.Match(token.TokenTable)
	node := &ast.DropTable{}
	node.IfExists = parser.Match(token.TokenIf) && parser.Match(token.TokenExists)
	name, err := parser.Required(token.TokenId)
	if err == nil {
		node.Name = name.GetContent()
	}
	return node
}

func (parser *ParserContext) ParseTruncate() *ast.TruncateTable {
	parser.Match(token.TokenTruncate)
	parser.Match(token.TokenTable)
	node := &ast.TruncateTable{}
	name, err := parser.Required(token.TokenId)
	if err == nil {
		node.Name = name.GetContent()
	}
	return node
}

func (parser *ParserContext) ParseAlter() *ast.AlterTable {
	parser.Match(token.TokenAlter)
	parser.Match(token.TokenTable)
	node := &ast.AlterTable{}
	name, err := parser.Required(token.TokenId)
	if err == nil {
		node.Name = name.GetContent()
	}
	return node
}

func (parser *ParserContext) ParseBegin() *ast.BeginTransaction {
	parser.Match(token.TokenBegin)
	parser.Match(token.TokenTransaction)
	return &ast.BeginTransaction{}
}

func (parser *ParserContext) ParseCommit() *ast.Commit {
	parser.Match(token.TokenCommit)
	return &ast.Commit{}
}

func (parser *ParserContext) ParseRollback() *ast.Rollback {
	parser.Match(token.TokenRollback)
	node := &ast.Rollback{}
	if parser.Check(token.TokenId) || parser.Check(token.TokenSavepoint) {
		// skip savepoint name if present
		sp := parser.CurrStr()
		node.Savepoint = &sp
		parser.idx++
	}
	return node
}

func (parser *ParserContext) ParseSavepoint() *ast.Savepoint {
	parser.Match(token.TokenSavepoint)
	node := &ast.Savepoint{}
	name, err := parser.Required(token.TokenId)
	if err == nil {
		node.Name = name.GetContent()
	}
	return node
}

func (parser *ParserContext) ParseRelease() *ast.Release {
	parser.Match(token.TokenRelease)
	parser.Match(token.TokenSavepoint)
	node := &ast.Release{}
	name, err := parser.Required(token.TokenId)
	if err == nil {
		node.Name = name.GetContent()
	}
	return node
}
