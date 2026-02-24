package webseed

import (
	"net/url"
	"testing"

	qt "github.com/go-quicktest/qt"
)

func TestDefaultPathEscaper(t *testing.T) {
	test := func(unescaped string, parts ...string) {
		assertPartsUnescape(t, unescaped, parts...)
	}
	for _, tc := range defaultPathEscapeTestCases {
		test(tc.escaped, tc.parts...)
	}
}

// So we can manually check, and use these to seed fuzzing.
var defaultPathEscapeTestCases = []struct {
	escaped string
	parts   []string
}{
	{"/", []string{"", ""}},
	{"a_b-c/d + e.f", []string{"a_b-c", "d + e.f"}},
	{"a_1-b_c2/d 3. (e, f).g", []string{"a_1-b_c2", "d 3. (e, f).g"}},
	{"a_b-c/d + e.f", []string{"a_b-c", "d + e.f"}},
	{"a_1-b_c2/d 3. (e, f).g", []string{"a_1-b_c2", "d 3. (e, f).g"}},
	{"war/and/peace", []string{"war", "and", "peace"}},
	{"he//o#world/world", []string{"he//o#world", "world"}},
	{`ノ┬─┬ノ ︵ ( \o°o)\`, []string{`ノ┬─┬ノ ︵ ( \o°o)\`}},
	{
		`%aa + %bb/Parsi Tv - سرقت و باز کردن در ماشین در کم‌تر از ۳ ثانیه + فیلم.webm`,
		[]string{`%aa + %bb`, `Parsi Tv - سرقت و باز کردن در ماشین در کم‌تر از ۳ ثانیه + فیلم.webm`},
	},
}

func assertPartsUnescape(t *testing.T, unescaped string, parts ...string) {
	escaped := defaultPathEscaper(parts)
	pathUnescaped, err := url.PathUnescape(escaped)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(pathUnescaped, unescaped))
	queryUnescaped, err := url.QueryUnescape(escaped)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(queryUnescaped, unescaped))
}

func FuzzDefaultPathEscaper(f *testing.F) {
	for _, tc := range defaultPathEscapeTestCases {
		if len(tc.parts) == 2 {
			f.Add(tc.parts[0], tc.parts[1])
		}
	}
	// I think a single separator is enough to test special handling around /. Also fuzzing doesn't
	// let us take []string as an input.
	f.Fuzz(func(t *testing.T, first, second string) {
		assertPartsUnescape(t, first+"/"+second, first, second)
	})
}
