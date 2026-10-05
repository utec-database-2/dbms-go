package main

import "testing"

func TestPointLiteralFromCell(t *testing.T) {
	good := map[string]string{
		"-12.0464 -77.0428":             "POINT(-12.0464, -77.0428)",
		"  -12.0464   -77.0428 ":        "POINT(-12.0464, -77.0428)",
		"-12.0464;-77.0428":             "POINT(-12.0464, -77.0428)",
		"-12.0464|-77.0428":             "POINT(-12.0464, -77.0428)",
		"-12.0464,-77.0428":             "POINT(-12.0464, -77.0428)",
		"POINT(-12.0464, -77.0428)":     "POINT(-12.0464, -77.0428)",
		"point(-12.0464,-77.0428)":      "POINT(-12.0464, -77.0428)",
		"0 0":                           "POINT(0, 0)",
		"-12.0464321987 -77.0428123456": "POINT(-12.0464321987, -77.0428123456)",
		"90 180":                        "POINT(90, 180)",
		"-90 -180":                      "POINT(-90, -180)",
	}
	for in, want := range good {
		got, err := pointLiteralFromCell(in)
		if err != nil {
			t.Errorf("%q: error inesperado: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q: obtuve %q, esperaba %q", in, got, want)
		}
	}

	for _, in := range []string{
		"", "   ", "abc", "1", "1 2 3", "a b", "1 b", "a 2", "NaN 1", "1 Inf",
		"91 0", "-91 0", "0 181", "0 -181",
		"POINT(1)", "POINT(1, 2", "POINT 1, 2", "POINT()", "POINT(1, 2, 3)",
	} {
		if got, err := pointLiteralFromCell(in); err == nil {
			t.Errorf("%q debería fallar, pero devolvió %q", in, got)
		}
	}
}
