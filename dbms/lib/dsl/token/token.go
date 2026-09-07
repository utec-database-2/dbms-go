package token

import "github.com/dbms-go/v2/dbms/lib/dsl/utils"

type Token struct {
	content string
	pos     utils.Position
	tType   TokenType
}

func (t Token) GetContent() string {
	return t.content
}

func (t Token) GetType() TokenType {
	return t.tType
}

func (t Token) IsUnknown() bool {
	return t.tType == TokenUnknown
}

// TokenType enum
type TokenType int

const (
	TokenUnknown TokenType = iota // null case

	// Operators & Punctuation
	TokenPlus
	TokenMinus
	TokenDiv
	TokenDivI
	TokenPow
	TokenEq
	TokenNeq
	TokenLt
	TokenLte
	TokenGt
	TokenGte
	TokenExclamation
	TokenAnd
	TokenOr
	TokenAssign
	TokenLbracket
	TokenRbracket
	TokenLparent
	TokenRparent
	TokenLbraces
	TokenRbraces
	TokenSemicolon
	TokenColon
	TokenQuestion
	TokenComma
	TokenDot
	TokenAsterisk

	// Keywords
	TokenSelect
	TokenFrom
	TokenWhere
	TokenInsert
	TokenValues
	TokenDelete
	TokenCreate
	TokenUpdate
	TokenTable
	TokenInto
	TokenOrder
	TokenBy
	TokenIn
	TokenAs

	TokenIf
	TokenElse

	TokenTrue
	TokenFalse

	// Literals & Special
	TokenInt
	TokenDecimal
	TokenInterval
	TokenId
	TokenArray
	TokenString
	TokenEnd
)

var tokenSymbols = map[TokenType]string{
	TokenPlus:        "+",
	TokenMinus:       "-",
	TokenDiv:         "/",
	TokenDivI:        "//",
	TokenPow:         "**",
	TokenEq:          "==",
	TokenNeq:         "!=",
	TokenLt:          "<",
	TokenLte:         "<=",
	TokenGt:          ">",
	TokenGte:         ">=",
	TokenExclamation: "!",
	TokenAnd:         "&&",
	TokenOr:          "||",
	TokenAssign:      "=",
	TokenLbracket:    "[",
	TokenRbracket:    "]",
	TokenLparent:     "(",
	TokenRparent:     ")",
	TokenLbraces:     "{",
	TokenRbraces:     "}",
	TokenSemicolon:   ";",
	TokenColon:       ":",
	TokenQuestion:    "?",
	TokenComma:       ",",
	TokenDot:         ".",
	TokenAsterisk:    "*",

	// Keywords
	TokenIf:   "if",
	TokenElse: "else",

	TokenSelect: "select",
	TokenFrom:   "from",
	TokenWhere:  "where",
	TokenInsert: "insert",
	TokenValues: "values",
	TokenDelete: "delete",
	TokenCreate: "create",
	TokenUpdate: "update",
	TokenTable:  "table",
	TokenInto:   "into",
	TokenOrder:  "order",
	TokenBy:     "by",
	TokenIn:     "in",
	TokenAs:     "as",

	TokenTrue:  "true",
	TokenFalse: "false",

	// Int, Decimal, ID, etc. return empty string/nil equivalent
}

var (
	// Reversed Map
	symbolsMap map[string]TokenType

	// Words that map to operator tokens whose symbol differs (e.g. AND vs &&)
	keywordSymbols = map[string]TokenType{
		"and": TokenAnd,
		"or":  TokenOr,
	}
)

func init() {
	symbolsMap = make(map[string]TokenType)
	for key, value := range tokenSymbols {
		symbolsMap[value] = key
	}
}

func FindBySymbol(sym string) TokenType {
	if res, ok := keywordSymbols[sym]; ok {
		return res
	}
	res, ok := symbolsMap[sym]
	if !ok {
		return TokenUnknown
	}
	return res
}

func CreateToken(tt TokenType, content string, line int, index int) Token {
	return Token{content, utils.Position{Line: line, Index: index}, tt}
}
