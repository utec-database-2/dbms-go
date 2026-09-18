package integration

import (
	"testing"

	"github.com/dbms-go/v2/dbms/lib/dsl/lexer"
	"github.com/dbms-go/v2/dbms/lib/dsl/token"
)

func lexTokens(t *testing.T, src string) ([]token.Token, error) {
	t.Helper()
	ctx := lexer.Tokenize(src, "\n")
	if err := ctx.Err(); err != nil {
		return ctx.Tokens, err
	}
	return ctx.Tokens, nil
}

func TestTokenizeEmptyInputEndsWithEOF(t *testing.T) {
	toks, err := lexTokens(t, "")
	if err != nil {
		t.Fatalf("empty input: %v", err)
	}
	if len(toks) != 1 || toks[0].GetType() != token.TokenEnd {
		t.Fatalf("empty input: got %d tokens, want single TokenEnd", len(toks))
	}
}

func TestTokenizeKeywords(t *testing.T) {
	cases := map[string]token.TokenType{
		"SELECT": token.TokenSelect,
		"FROM":   token.TokenFrom,
		"WHERE":  token.TokenWhere,
		"INSERT": token.TokenInsert,
		"INTO":   token.TokenInto,
		"VALUES": token.TokenValues,
		"CREATE": token.TokenCreate,
		"TABLE":  token.TokenTable,
		"DELETE": token.TokenDelete,
		"ORDER":  token.TokenOrder,
		"BY":     token.TokenBy,
		"AS":     token.TokenAs,
		"AND":    token.TokenAnd,
		"OR":     token.TokenOr,
		"TRUE":   token.TokenTrue,
		"FALSE":  token.TokenFalse,
	}
	for in, want := range cases {
		toks, err := lexTokens(t, in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if len(toks) != 2 {
			t.Fatalf("%q: got %d tokens, want 2", in, len(toks))
		}
		if toks[0].GetType() != want {
			t.Fatalf("%q: got token %d, want %d", in, toks[0].GetType(), want)
		}
		if toks[1].GetType() != token.TokenEnd {
			t.Fatalf("%q: last token must be TokenEnd", in)
		}
	}
}

func TestKeywordsAreCaseInsensitive(t *testing.T) {
	toks, err := lexTokens(t, "select SeLeCt")
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}
	if len(toks) != 3 {
		t.Fatalf("got %d tokens, want 3", len(toks))
	}
	for i := 0; i < 2; i++ {
		if toks[i].GetType() != token.TokenSelect {
			t.Fatalf("token %d: got %d, want TokenSelect", i, toks[i].GetType())
		}
	}
}

func TestTokenizeOperators(t *testing.T) {
	cases := map[string]token.TokenType{
		"+":  token.TokenPlus,
		"-":  token.TokenMinus,
		"*":  token.TokenAsterisk,
		"/":  token.TokenDiv,
		"//": token.TokenDivI,
		"**": token.TokenPow,
		"==": token.TokenEq,
		"!=": token.TokenNeq,
		"<":  token.TokenLt,
		"<=": token.TokenLte,
		">":  token.TokenGt,
		">=": token.TokenGte,
		"&&": token.TokenAnd,
		"||": token.TokenOr,
		";":  token.TokenSemicolon,
		",":  token.TokenComma,
		"(":  token.TokenLparent,
		")":  token.TokenRparent,
	}
	for in, want := range cases {
		toks, err := lexTokens(t, in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if len(toks) != 2 || toks[0].GetType() != want || toks[1].GetType() != token.TokenEnd {
			t.Fatalf("%q: got %v, want type %d then TokenEnd", in, toks, want)
		}
	}
}

func TestTokenizeNumbers(t *testing.T) {
	toks, err := lexTokens(t, "42 3.14")
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}
	if len(toks) != 3 {
		t.Fatalf("got %d tokens, want 3", len(toks))
	}
	if toks[0].GetType() != token.TokenInt || toks[0].GetContent() != "42" {
		t.Fatalf("token 0: got (%d, %q), want TokenInt 42", toks[0].GetType(), toks[0].GetContent())
	}
	if toks[1].GetType() != token.TokenDecimal || toks[1].GetContent() != "3.14" {
		t.Fatalf("token 1: got (%d, %q), want TokenDecimal 3.14", toks[1].GetType(), toks[1].GetContent())
	}
}

func TestTokenizeStrings(t *testing.T) {
	for _, in := range []string{"'ana'", `"bob"`} {
		toks, err := lexTokens(t, in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if len(toks) != 2 || toks[0].GetType() != token.TokenString {
			t.Fatalf("%q: got %v, want TokenString then TokenEnd", in, toks)
		}
		if toks[0].GetContent() != in {
			t.Fatalf("%q: content %q preserved, got %q (quotes included)", in, in, toks[0].GetContent())
		}
	}
}

func TestTokenizeUnknownCharacterFails(t *testing.T) {
	_, err := lexTokens(t, "select @ from t")
	if err == nil {
		t.Fatalf("expected an error for unknown character")
	}
}