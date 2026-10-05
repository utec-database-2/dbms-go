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
	TokenDistinct
	TokenFrom
	TokenWhere
	TokenGroup
	TokenHaving
	TokenOrder
	TokenBy
	TokenLimit
	TokenOffset
	TokenInsert
	TokenInto
	TokenValues
	TokenUpdate
	TokenSet
	TokenDelete
	TokenCreate
	TokenTable
	TokenClustered
	TokenDrop
	TokenAlter
	TokenTruncate
	TokenIndex
	TokenView
	TokenConstraint
	TokenPrimary
	TokenKey
	TokenUnique
	TokenNull
	TokenNot
	TokenDefault
	TokenIf
	TokenElse
	TokenExists
	TokenLike
	TokenBetween
	TokenIs
	TokenCast
	TokenST
	TokenGeometry
	TokenSTArea
	TokenSTLength
	TokenSTDistance
	TokenSTIntersects
	TokenSTContains
	TokenSTWithin
	TokenSTAsText
	TokenSTGeomFromText

	TokenJoin
	TokenInner
	TokenLeft
	TokenRight
	TokenFull
	TokenCross
	TokenOn
	TokenUsing

	TokenIn
	TokenAs

	TokenBegin
	TokenTransaction
	TokenCommit
	TokenRollback
	TokenSavepoint
	TokenRelease

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
	// Keywords
	TokenIf:   "if",
	TokenElse: "else",

	TokenSelect:   "select",
	TokenDistinct: "distinct",
	TokenFrom:     "from",
	TokenWhere:    "where",
	TokenGroup:    "group",
	TokenHaving:   "having",
	TokenOrder:    "order",
	TokenBy:       "by",
	TokenLimit:    "limit",
	TokenOffset:   "offset",

	TokenInsert: "insert",
	TokenInto:   "into",
	TokenValues: "values",

	TokenUpdate: "update",
	TokenSet:    "set",

	TokenDelete: "delete",

	TokenCreate:     "create",
	TokenTable:      "table",
	TokenClustered:  "clustered",
	TokenIndex:      "index",
	TokenView:       "view",
	TokenConstraint: "constraint",
	TokenPrimary:    "primary",
	TokenKey:        "key",
	TokenUnique:     "unique",
	TokenNull:       "null",
	TokenNot:        "not",
	TokenDefault:    "default",

	TokenDrop:     "drop",
	TokenAlter:    "alter",
	TokenTruncate: "truncate",
	TokenExists:   "exists",
	TokenLike:     "like",
	TokenBetween:  "between",
	TokenIs:       "is",
	TokenCast:     "cast",

	// Spatial functions (basic)
	TokenST:             "st",
	TokenGeometry:       "geometry",
	TokenSTArea:         "st_area",
	TokenSTLength:       "st_length",
	TokenSTDistance:     "st_distance",
	TokenSTIntersects:   "st_intersects",
	TokenSTContains:     "st_contains",
	TokenSTWithin:       "st_within",
	TokenSTAsText:       "st_astext",
	TokenSTGeomFromText: "st_geomfromtext",

	TokenJoin:  "join",
	TokenInner: "inner",
	TokenLeft:  "left",
	TokenRight: "right",
	TokenFull:  "full",
	TokenCross: "cross",
	TokenOn:    "on",
	TokenUsing: "using",

	TokenIn: "in",
	TokenAs: "as",

	TokenBegin:       "begin",
	TokenTransaction: "transaction",
	TokenCommit:      "commit",
	TokenRollback:    "rollback",
	TokenSavepoint:   "savepoint",
	TokenRelease:     "release",

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
		"not": TokenNot,
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
