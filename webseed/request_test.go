package webseed

import (
	"net/url"
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestTrailingPath(t *testing.T) {
	c := qt.New(t)
	test := func(parts []string, result string) {
		unescaped, err := url.QueryUnescape(trailingPath(parts[0], parts[1:], url.QueryEscape))
		if !c.Check(err, qt.IsNil) {
			return
		}
		c.Check(unescaped, qt.Equals, result)
	}
	test([]string{"a_b-c", "d + e.f"}, "a_b-c/d + e.f")
	test([]string{"a_1-b_c2", "d 3. (e, f).g"},
		"a_1-b_c2/d 3. (e, f).g",
	)
}

func TestTrailingPathForEmptyInfoName(t *testing.T) {
	qt.Check(t, trailingPath("", []string{`ノ┬─┬ノ ︵ ( \o°o)\`}, url.QueryEscape), qt.Equals, "%E3%83%8E%E2%94%AC%E2%94%80%E2%94%AC%E3%83%8E+%EF%B8%B5+%28+%5Co%C2%B0o%29%5C")
	qt.Check(t, trailingPath("", []string{"hello", "world"}, url.QueryEscape), qt.Equals, "hello/world")
	qt.Check(t, trailingPath("war", []string{"and", "peace"}, url.QueryEscape), qt.Equals, "war/and/peace")
}
