package bencode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"strings"
	"testing"

	qt "github.com/go-quicktest/qt"
	"github.com/google/go-cmp/cmp"
)

type random_decode_test struct {
	data     string
	expected interface{}
}

var random_decode_tests = []random_decode_test{
	{"i57e", int64(57)},
	{"i-9223372036854775808e", int64(-9223372036854775808)},
	{"5:hello", "hello"},
	{"29:unicode test проверка", "unicode test проверка"},
	{"d1:ai5e1:b5:helloe", map[string]interface{}{"a": int64(5), "b": "hello"}},
	{
		"li5ei10ei15ei20e7:bencodee",
		[]interface{}{int64(5), int64(10), int64(15), int64(20), "bencode"},
	},
	{"ldedee", []interface{}{map[string]interface{}{}, map[string]interface{}{}}},
	{"le", []interface{}{}},
	{"i604919719469385652980544193299329427705624352086e", func() *big.Int {
		ret, _ := big.NewInt(-1).SetString("604919719469385652980544193299329427705624352086", 10)
		return ret
	}()},
	{"d1:rd6:\xd4/\xe2F\x00\x01i42ee1:t3:\x9a\x87\x011:v4:TR%=1:y1:re", map[string]interface{}{
		"r": map[string]interface{}{"\xd4/\xe2F\x00\x01": int64(42)},
		"t": "\x9a\x87\x01",
		"v": "TR%=",
		"y": "r",
	}},
	{"d0:i420ee", map[string]interface{}{"": int64(420)}},
}

func TestRandomDecode(t *testing.T) {
	for _, test := range random_decode_tests {
		var value interface{}
		err := Unmarshal([]byte(test.data), &value)
		if err != nil {
			t.Error(err, test.data)
			continue
		}
		qt.Check(t, qt.CmpEquals(value, test.expected, cmp.Comparer(func(a, b *big.Int) bool {
			return a.Cmp(b) == 0
		})))
	}
}

func TestLoneE(t *testing.T) {
	var v int
	err := Unmarshal([]byte("e"), &v)
	se := err.(*SyntaxError)
	qt.Assert(t, qt.Equals(se.Offset, 0))
}

func TestDecoderConsecutive(t *testing.T) {
	d := NewDecoder(bytes.NewReader([]byte("i1ei2e")))
	var i int
	err := d.Decode(&i)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(i, 1))
	err = d.Decode(&i)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(i, 2))
	err = d.Decode(&i)
	qt.Assert(t, qt.Equals(err, io.EOF))
}

func TestDecoderConsecutiveDicts(t *testing.T) {
	bb := bytes.NewBufferString("d4:herp4:derped3:wat1:ke17:oh baby a triple!")

	d := NewDecoder(bb)
	qt.Check(t, qt.Equals(bb.String(), "d4:herp4:derped3:wat1:ke17:oh baby a triple!"))
	qt.Check(t, qt.Equals(d.Offset, 0))

	var m map[string]interface{}

	qt.Assert(t, qt.IsNil(d.Decode(&m)))
	qt.Check(t, qt.HasLen(m, 1))
	qt.Check(t, qt.Equals(m["herp"], "derp"))
	qt.Check(t, qt.Equals(bb.String(), "d3:wat1:ke17:oh baby a triple!"))
	qt.Check(t, qt.Equals(d.Offset, 14))

	qt.Assert(t, qt.IsNil(d.Decode(&m)))
	qt.Check(t, qt.Equals(m["wat"], "k"))
	qt.Check(t, qt.Equals(bb.String(), "17:oh baby a triple!"))
	qt.Check(t, qt.Equals(d.Offset, 24))

	var s string
	qt.Assert(t, qt.IsNil(d.Decode(&s)))
	qt.Check(t, qt.Equals(s, "oh baby a triple!"))
	qt.Check(t, qt.Equals(d.Offset, 44))
}

func check_error(t *testing.T, err error) {
	if err != nil {
		t.Error(err)
	}
}

func assert_equal(t *testing.T, x, y interface{}) {
	if !reflect.DeepEqual(x, y) {
		t.Errorf("got: %v (%T), expected: %v (%T)\n", x, x, y, y)
	}
}

type unmarshalerInt struct {
	x int
}

func (me *unmarshalerInt) UnmarshalBencode(data []byte) error {
	return Unmarshal(data, &me.x)
}

type unmarshalerString struct {
	x string
}

func (me *unmarshalerString) UnmarshalBencode(data []byte) error {
	me.x = string(data)
	return nil
}

func TestUnmarshalerBencode(t *testing.T) {
	var i unmarshalerInt
	var ss []unmarshalerString
	check_error(t, Unmarshal([]byte("i71e"), &i))
	assert_equal(t, i.x, 71)
	check_error(t, Unmarshal([]byte("l5:hello5:fruit3:waye"), &ss))
	assert_equal(t, ss[0].x, "5:hello")
	assert_equal(t, ss[1].x, "5:fruit")
	assert_equal(t, ss[2].x, "3:way")
}

func TestIgnoreUnmarshalTypeError(t *testing.T) {
	s := struct {
		Ignore int `bencode:",ignore_unmarshal_type_error"`
		Normal int
	}{}
	qt.Assert(t, qt.IsNotNil(Unmarshal([]byte("d6:Normal5:helloe"), &s)))
	qt.Check(t, qt.IsNil(Unmarshal([]byte("d6:Ignore5:helloe"), &s)))
	qt.Assert(t, qt.IsNil(Unmarshal([]byte("d6:Ignorei42ee"), &s)))
	qt.Check(t, qt.Equals(s.Ignore, 42))
}

// Test unmarshalling []byte into something that has the same kind but
// different type.
func TestDecodeCustomSlice(t *testing.T) {
	type flag byte
	var fs3, fs2 []flag
	// We do a longer slice then a shorter slice to see if the buffers are
	// shared.
	d := NewDecoder(bytes.NewBufferString("3:\x01\x10\xff2:\x04\x0f"))
	qt.Assert(t, qt.IsNil(d.Decode(&fs3)))
	qt.Assert(t, qt.IsNil(d.Decode(&fs2)))
	qt.Check(t, qt.DeepEquals(fs3, []flag{1, 16, 255}))
	qt.Check(t, qt.DeepEquals(fs2, []flag{4, 15}))
}

func TestUnmarshalUnusedBytes(t *testing.T) {
	var i int
	qt.Assert(t, qt.Equals(Unmarshal([]byte("i42ee"), &i), error(ErrUnusedTrailingBytes{1})))
	qt.Check(t, qt.Equals(i, 42))
}

func TestUnmarshalByteArray(t *testing.T) {
	var ba [2]byte
	qt.Check(t, qt.IsNil(Unmarshal([]byte("2:hi"), &ba)))
	qt.Check(t, qt.Equals(string(ba[:]), "hi"))
}

func TestDecodeDictIntoUnsupported(t *testing.T) {
	// Any type that a dict shouldn't be unmarshallable into.
	var i int
	err := Unmarshal([]byte("d1:a1:be"), &i)
	t.Log(err)
	qt.Check(t, qt.IsNotNil(err))
}

func TestUnmarshalDictKeyNotString(t *testing.T) {
	// Any type that a dict shouldn't be unmarshallable into.
	var i int
	err := Unmarshal([]byte("di42e3:yese"), &i)
	t.Log(err)
	qt.Check(t, qt.IsNotNil(err))
}

type arbitraryReader struct{}

func (arbitraryReader) Read(b []byte) (int, error) {
	return len(b), nil
}

func decodeHugeString(t *testing.T, strLen int64, header, tail string, v interface{}, maxStrLen MaxStrLen) error {
	r, w := io.Pipe()
	go func() {
		fmt.Fprintf(w, header, strLen)
		io.CopyN(w, arbitraryReader{}, strLen)
		w.Write([]byte(tail))
		w.Close()
	}()
	d := NewDecoder(r)
	d.MaxStrLen = maxStrLen
	return d.Decode(v)
}

// Ensure that bencode strings in various places obey the Decoder.MaxStrLen field.
func TestDecodeMaxStrLen(t *testing.T) {
	t.Parallel()
	test := func(header, tail string, v interface{}, maxStrLen MaxStrLen) {
		strLen := maxStrLen
		if strLen == 0 {
			strLen = DefaultDecodeMaxStrLen
		}
		qt.Assert(t, qt.IsNil(decodeHugeString(t, strLen, header, tail, v, maxStrLen)))
		qt.Assert(t, qt.IsNotNil(decodeHugeString(t, strLen+1, header, tail, v, maxStrLen)))
	}
	test("d%d:", "i0ee", new(interface{}), 0)
	test("%d:", "", new(interface{}), DefaultDecodeMaxStrLen)
	test("%d:", "", new([]byte), 1)
	test("d3:420%d:", "e", new(struct {
		Hi []byte `bencode:"420"`
	}), 69)
}

// This is for the "github.com/anacrolix/torrent/metainfo".Info.Private field.
func TestDecodeStringIntoBoolPtr(t *testing.T) {
	var m struct {
		Private *bool `bencode:"private,omitempty"`
	}
	check := func(msg string, expectNil, expectTrue bool) {
		m.Private = nil
		qt.Check(t, qt.IsNil(Unmarshal([]byte(msg), &m)), qt.Commentf("%q", msg))
		if expectNil {
			qt.Check(t, qt.IsNil(m.Private))
		} else {
			if qt.Check(t, qt.IsNotNil(m.Private), qt.Commentf("%q", msg)) {
				qt.Check(t, qt.Equals(*m.Private, expectTrue), qt.Commentf("%q", msg))
			}
		}
	}
	check("d7:privatei1ee", false, true)
	check("d7:privatei0ee", false, false)
	check("d7:privatei42ee", false, true)
	// This is a weird case. We could not allocate the bool to indicate it was bad (maybe a bad
	// serializer which isn't uncommon), but that requires reworking the decoder to handle
	// automatically. I think if we cared enough we'd create a custom Unmarshaler. Also if we were
	// worried enough about performance I'd completely rewrite this package.
	check("d7:private0:e", false, false)
	check("d7:private1:te", false, true)
	check("d7:private5:falsee", false, false)
	check("d7:private1:Fe", false, false)
	check("d7:private11:bunnyfoofooe", false, true)
}

// To set expectations about how our Decoder should work.
func TestJsonDecoderBehaviour(t *testing.T) {
	test := func(input string, items int, finalErr error) {
		d := json.NewDecoder(strings.NewReader(input))
		actualItems := 0
		var firstErr error
		for {
			var discard any
			firstErr = d.Decode(&discard)
			if firstErr != nil {
				break
			}
			actualItems++
		}
		qt.Check(t, qt.Equals(firstErr, finalErr))
		qt.Check(t, qt.Equals(actualItems, items))
	}
	test("", 0, io.EOF)
	test("{}", 1, io.EOF)
	test("{} {", 1, io.ErrUnexpectedEOF)
}

// deepList returns a bencode list nested n levels deep around the integer 0.
func deepList(n int) string {
	return strings.Repeat("l", n) + "i0e" + strings.Repeat("e", n)
}

// deepDict returns a bencode dict nested n levels deep around the integer 0, with key "a" at each
// level.
func deepDict(n int) string {
	return strings.Repeat("d1:a", n) + "i0e" + strings.Repeat("e", n)
}

// Ensure that deeply nested dicts and lists are rejected with a SyntaxError instead of recursing
// unboundedly, and that the Decoder.MaxDepth field overrides the default.
func TestDecodeMaxDepth(t *testing.T) {
	decode := func(t *testing.T, input string) error {
		t.Helper()
		var d interface{}
		return Unmarshal([]byte(input), &d)
	}
	checkErr := func(t *testing.T, input string, wantErr bool) {
		t.Helper()
		err := decode(t, input)
		if !wantErr {
			if err != nil {
				t.Fatalf("expected %d-deep input to decode, got %v", strings.Count(input, "d")+strings.Count(input, "l"), err)
			}
			return
		}
		var se *SyntaxError
		if !errors.As(err, &se) {
			t.Fatalf("expected SyntaxError, got %T: %v", err, err)
		}
		qt.Check(t, qt.StringContains(se.Error(), "nesting depth"))
	}
	// The default limit allows exactly DefaultMaxDepth levels of nesting.
	checkErr(t, deepList(DefaultMaxDepth), false)
	checkErr(t, deepList(DefaultMaxDepth+1), true)
	checkErr(t, deepDict(DefaultMaxDepth), false)
	checkErr(t, deepDict(DefaultMaxDepth+1), true)
	// A custom MaxDepth is honored.
	for _, depth := range []int{1, 2, 10, 100} {
		in := deepDict(depth)
		d := NewDecoder(strings.NewReader(in))
		d.MaxDepth = depth
		var v interface{}
		if err := d.Decode(&v); err != nil {
			t.Errorf("MaxDepth=%d: expected %d-deep input to decode, got %v", depth, depth, err)
		}
		d = NewDecoder(strings.NewReader(deepDict(depth + 1)))
		d.MaxDepth = depth
		var se *SyntaxError
		if err := d.Decode(&v); !errors.As(err, &se) {
			t.Errorf("MaxDepth=%d: expected SyntaxError for %d-deep input, got %T: %v", depth, depth+1, err, err)
		}
	}
}

// Values routed through the Unmarshaler (readOneValue) path are depth-bounded too, so that e.g.
// metainfo.InfoBytes cannot carry a stack-overflowing nested value.
func TestDecodeMaxDepthUnmarshaler(t *testing.T) {
	decode := func(innerDepth int) error {
		var m struct {
			Info Bytes `bencode:"info"`
		}
		// The envelope dict adds one nesting level.
		return Unmarshal([]byte("d4:info"+deepList(innerDepth)+"e"), &m)
	}
	if err := decode(DefaultMaxDepth - 1); err != nil {
		t.Fatalf("expected info nested %d levels deep to decode, got %v", DefaultMaxDepth-1, err)
	}
	var se *SyntaxError
	if err := decode(DefaultMaxDepth); !errors.As(err, &se) {
		t.Fatalf("expected SyntaxError for info nested %d levels deep, got %T: %v", DefaultMaxDepth, err, err)
	}
}

// The depth counter stays balanced across Decodes and across the panic used to surface a depth
// error: the same Decoder can be reused, as TestDecoderConsecutive does, and must not inherit any
// leftover depth.
func TestDecodeMaxDepthConsecutive(t *testing.T) {
	// Two max-depth values back to back: if depth leaked out of the first Decode, the second would
	// fail.
	d := NewDecoder(bytes.NewBufferString(deepDict(DefaultMaxDepth) + deepList(DefaultMaxDepth)))
	var v1 interface{}
	qt.Assert(t, qt.IsNil(d.Decode(&v1)))
	var v2 interface{}
	qt.Assert(t, qt.IsNil(d.Decode(&v2)))
	// A depth error must unwind the counter too: after rejecting one too-deep value, the same
	// Decoder must still accept a value at the limit.
	d.r = strings.NewReader(deepDict(DefaultMaxDepth + 1))
	var v3 interface{}
	var se *SyntaxError
	qt.Assert(t, qt.ErrorAs(d.Decode(&v3), &se))
	d.r = strings.NewReader(deepDict(DefaultMaxDepth))
	var v4 interface{}
	qt.Assert(t, qt.IsNil(d.Decode(&v4)), qt.Commentf("depth counter leaked out of the failed Decode"))
}

// The readOneValue path (values decoded for Unmarshaler fields) must apply Decoder.MaxStrLen too,
// otherwise oversized bulk fields bypass the limit entirely.
func TestDecodeMaxStrLenUnmarshaler(t *testing.T) {
	in := "d1:b10:abcdefghije" // key "b" -> 10-byte string
	decode := func(maxStrLen MaxStrLen) error {
		d := NewDecoder(strings.NewReader(in))
		d.MaxStrLen = maxStrLen
		var m struct {
			B Bytes `bencode:"b"`
		}
		return d.Decode(&m)
	}
	if err := decode(10); err != nil {
		t.Fatalf("expected 10-byte string at limit 10 to decode, got %v", err)
	}
	var se *SyntaxError
	if err := decode(9); !errors.As(err, &se) {
		t.Fatalf("expected SyntaxError for 10-byte string at limit 9, got %T: %v", err, err)
	}
	qt.Check(t, qt.StringContains(se.Error(), "exceeds limit"))
}
