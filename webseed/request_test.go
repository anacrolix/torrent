package webseed

import (
	"net/url"
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestDefaultPathEscaper(t *testing.T) {
	c := qt.New(t)
	test := func(unescaped string, parts ...string) {
		escaped := defaultPathEscaper(parts)
		pathUnescaped, err := url.PathUnescape(escaped)
		c.Assert(err, qt.IsNil)
		c.Assert(pathUnescaped, qt.Equals, unescaped)
		queryUnescaped, err := url.QueryUnescape(escaped)
		c.Assert(err, qt.IsNil)
		c.Assert(queryUnescaped, qt.Equals, unescaped)
	}
	test("a_b-c/d + e.f", "a_b-c", "d + e.f")
	test("a_1-b_c2/d 3. (e, f).g", "a_1-b_c2", "d 3. (e, f).g")
	test("a_b-c/d + e.f", "a_b-c", "d + e.f")
	test("a_1-b_c2/d 3. (e, f).g", "a_1-b_c2", "d 3. (e, f).g")
	test("war/and/peace", "war", "and", "peace")
	test("hello/world", "hello", "world")
	test(`ノ┬─┬ノ ︵ ( \o°o)\`, `ノ┬─┬ノ ︵ ( \o°o)\`)
	test(`%aa + %bb/Parsi Tv - سرقت و باز کردن در ماشین در کم‌تر از ۳ ثانیه + فیلم.webm`,
		`%aa + %bb`, `Parsi Tv - سرقت و باز کردن در ماشین در کم‌تر از ۳ ثانیه + فیلم.webm`)
}
