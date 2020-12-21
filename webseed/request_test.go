package webseed

import (
	"net/url"
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestTrailingPath(t *testing.T) {
	c := qt.New(t)
	test := func(parts []string, result string) {
		unescaped, err := url.QueryUnescape(trailingPath(parts[0], parts[1:]))
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
