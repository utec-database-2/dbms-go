package lexer

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/dbms-go/v2/dbms/lib/dsl/token"
)

type LexerContext struct {
	current int
	end     int
	first   int
	src     string
	newline string
	err     error
	Tokens  []token.Token
}

func (ctx *LexerContext) CurrChar() rune {
	return rune(ctx.src[ctx.current])
}

// Err devuelve el primer error de lexing encontrado por Tokenize, si lo hubo.
func (ctx *LexerContext) Err() error {
	return ctx.err
}

func (ctx *LexerContext) HasNext() bool {
	return ctx.current < ctx.end
}

func Tokenize(src string, nl string) LexerContext {
	ctx := LexerContext{end: len(src), src: src, newline: nl}

	for ctx.HasNext() {
		token, err := ctx.Next()
		if err != nil {
			fmt.Println("Lexer error:", err)
			ctx.err = err
			return ctx
		}

		ctx.Tokens = append(ctx.Tokens, token)
	}

	ctx.Tokens = append(ctx.Tokens, token.CreateToken(token.TokenEnd, "", 0, 0))

	return ctx
}

func (ctx *LexerContext) Next() (token.Token, error) {
	if !ctx.HasNext() {
		return token.CreateToken(token.TokenUnknown, "", 0, 0), errors.New("Lexer ends")
	}
	ctx.ConsumeSpaces()
	ctx.first = ctx.current

	curr := ctx.CurrChar()
	var token token.Token
	if unicode.IsLetter(curr) || curr == '_' {
		token = ctx.NextID()
	} else if curr == '"' || curr == '\'' {
		token = ctx.NextWord()
	} else if unicode.IsDigit(curr) {
		token = ctx.NextNumber()
	} else {
		token = ctx.NextSpecial()
	}

	if token.IsUnknown() {
		return token, errors.New("Token not recognized: " + token.GetContent())
	}

	return token, nil
}

func (ctx *LexerContext) ConsumeSpaces() {
	for ctx.HasNext() && unicode.IsSpace(ctx.CurrChar()) {
		ctx.current++
	}
}

func (ctx *LexerContext) NextWord() token.Token {
	quote := ctx.CurrChar()
	ctx.current++ // jump opening quote
	for ctx.HasNext() && ctx.CurrChar() != quote {
		ctx.current++
	}
	if ctx.HasNext() {
		ctx.current++ // jump closing quote
	}
	content := ctx.src[ctx.first:ctx.current]
	return token.CreateToken(token.TokenString, content, 0, 0) // FIXME: Position
}

func (ctx *LexerContext) NextID() token.Token {
	ctx.current++
	for ctx.HasNext() {
		curr := ctx.CurrChar()
		if !(unicode.IsNumber(curr) || unicode.IsLetter(curr) || curr == '_') {
			break
		}
		ctx.current++
	}

	content := ctx.src[ctx.first:ctx.current]
	if ttype := token.FindBySymbol(strings.ToLower(content)); ttype != token.TokenUnknown {
		return token.CreateToken(ttype, content, 0, 0) // FIXME: Position
	}

	return token.CreateToken(token.TokenId, content, 0, 0) // FIXME: Position
}

func (ctx *LexerContext) NextNumber() token.Token {
	for ctx.HasNext() && unicode.IsNumber(ctx.CurrChar()) {
		ctx.current++
	}

	if ctx.current+1 < ctx.end && ctx.src[ctx.current] == '.' && unicode.IsNumber(rune(ctx.src[ctx.current+1])) {
		ctx.current++
		for ctx.HasNext() && unicode.IsNumber(ctx.CurrChar()) {
			ctx.current++
		}
		content := ctx.src[ctx.first:ctx.current]
		return token.CreateToken(token.TokenDecimal, content, 0, 0) // FIXME: Position
	}

	content := ctx.src[ctx.first:ctx.current]
	return token.CreateToken(token.TokenInt, content, 0, 0) // FIXME: Position
}

var twoCharSymbols = map[string]token.TokenType{
	"==": token.TokenEq,
	"!=": token.TokenNeq,
	"<=": token.TokenLte,
	">=": token.TokenGte,
	"&&": token.TokenAnd,
	"||": token.TokenOr,
	"//": token.TokenDivI,
	"**": token.TokenPow,
}

func (ctx *LexerContext) NextSpecial() token.Token {
	if ctx.current+1 < ctx.end {
		two := ctx.src[ctx.first : ctx.first+2]
		if ttype, ok := twoCharSymbols[two]; ok {
			ctx.current += 2
			return token.CreateToken(ttype, two, 0, 0) // FIXME: Position
		}
	}

	ctx.current++
	content := ctx.src[ctx.first:ctx.current]
	ttype := token.FindBySymbol(content)
	if ttype == token.TokenUnknown {
		return token.CreateToken(token.TokenUnknown, "", 0, 0)
	}

	return token.CreateToken(ttype, content, 0, 0) // FIXME: Position
}
