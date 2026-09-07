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
		ctx.parent = ctx.ParseCreate()
	case token.TokenUpdate:
		ctx.parent = ctx.ParseUpdate()
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
	t, err := parser.Required(token.TokenId)
	if err != nil {
		return expr
	}
	expr.Name = t.GetContent()
	if parser.Match(token.TokenAs) {
		alias, err := parser.Required(token.TokenId)
		if err == nil {
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
	t, err := parser.Required(token.TokenId)
	if err != nil {
		return &ast.IdExpr{}
	}
	return &ast.IdExpr{Name: t.GetContent()}
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

func (parser *ParserContext) ParseValue() (ast.ASTNode, error) {
	switch parser.CurrToken().GetType() {
	case token.TokenId:
		return parser.ParseId(), nil
	case token.TokenTrue, token.TokenFalse:
		return parser.ParseBool(), nil
	case token.TokenInt:
		return parser.ParseInt(), nil
	case token.TokenDecimal:
		return parser.ParseFloat(), nil
	case token.TokenString:
		return parser.ParseString(), nil
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

	if parser.Match(token.TokenAsterisk) {
		node.All = true
	} else {
		for parser.Check(token.TokenId) {
			node.Selected = append(node.Selected, *parser.ParseNameIndex())
			if !parser.Match(token.TokenComma) {
				break
			}
		}
	}

	parser.Match(token.TokenFrom)
	node.From = *parser.ParseTableExpr()

	if parser.Check(token.TokenWhere) {
		node.Closure = parser.ParseWhereExpr()
	}

	if parser.Match(token.TokenOrder) {
		parser.Match(token.TokenBy)
		id, err := parser.Required(token.TokenId)
		if err == nil {
			order := &ast.OrderBy{Expr: &ast.IdExpr{Name: id.GetContent()}}
			if parser.Check(token.TokenId) {
				direction := strings.ToLower(parser.CurrStr())
				if direction == "asc" || direction == "desc" {
					order.Descendent = direction == "desc"
					parser.idx++
				}
			}
			node.OrderBy = order
		}
	}

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

	col := &ast.ColumnExpr{Name: ast.IdExpr{Name: id.GetContent()}}

	switch parser.CurrToken().GetType() {
	case token.TokenId, token.TokenInt, token.TokenDecimal, token.TokenString:
		ttype := parser.CurrToken()
		parser.idx++
		col.Type = ast.IdExpr{Name: ttype.GetContent()}
	}

	return col
}

func (parser *ParserContext) ParseCreate() *ast.CreateTable {
	parser.Match(token.TokenCreate)
	parser.Match(token.TokenTable)

	name, err := parser.Required(token.TokenId)
	if err != nil {
		return nil
	}
	node := &ast.CreateTable{Name: name.GetContent()}

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

	return node
}

func (parser *ParserContext) ParseUpdate() *ast.Update {
	node := &ast.Update{}
	parser.Match(token.TokenFrom)
	node.From = *parser.ParseNameIndex()
	return node
}
