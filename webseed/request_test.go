package webseed

import (
	"net/url"
	"path"
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestEscapePath(t *testing.T) {
	c := qt.New(t)
	test := func(
		parts []string, result string,
		escaper PathEscaper,
		unescaper func(string) (string, error),
	) {
		unescaped, err := unescaper(escapePath(parts, escaper))
		if !c.Check(err, qt.IsNil) {
			return
		}
		c.Check(unescaped, qt.Equals, result)
	}

	// Test with nil escapers (always uses url.QueryEscape)
	// ------
	test(
		[]string{"a_b-c", "d + e.f"},
		"a_b-c/d + e.f",
		nil,
		url.QueryUnescape,
	)
	test(
		[]string{"a_1-b_c2", "d 3. (e, f).g"},
		"a_1-b_c2/d 3. (e, f).g",
		nil,
		url.QueryUnescape,
	)

	// Test with custom escapers
	// ------
	test(
		[]string{"a_b-c", "d + e.f"},
		"a_b-c/d + e.f",
		func(s []string) string {
			var ret []string
			for _, comp := range s {
				ret = append(ret, url.PathEscape(comp))
			}
			return path.Join(ret...)
		},
		url.PathUnescape,
	)
	test(
		[]string{"a_1-b_c2", "d 3. (e, f).g"},
		"a_1-b_c2/d 3. (e, f).g",
		func(s []string) string {
			var ret []string
			for _, comp := range s {
				ret = append(ret, url.PathEscape(comp))
			}
			return path.Join(ret...)
		},
		url.PathUnescape,
	)
}

func TestEscapePathForEmptyInfoName(t *testing.T) {
	qt.Check(t, escapePath([]string{`ノ┬─┬ノ ︵ ( \o°o)\`}, nil), qt.Equals, "%E3%83%8E%E2%94%AC%E2%94%80%E2%94%AC%E3%83%8E+%EF%B8%B5+%28+%5Co%C2%B0o%29%5C")
	qt.Check(t, escapePath([]string{"hello", "world"}, nil), qt.Equals, "hello/world")
	qt.Check(t, escapePath([]string{"war", "and", "peace"}, nil), qt.Equals, "war/and/peace")
}
