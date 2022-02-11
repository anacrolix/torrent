package bencode

import (
	"bytes"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		assert.EqualValues(t, test.expected, value)
	}
}

func TestLoneE(t *testing.T) {
	var v int
	err := Unmarshal([]byte("e"), &v)
	se := err.(*SyntaxError)
	require.EqualValues(t, 0, se.Offset)
}

func TestDecoderConsecutive(t *testing.T) {
	d := NewDecoder(bytes.NewReader([]byte("i1ei2e")))
	var i int
	err := d.Decode(&i)
	require.NoError(t, err)
	require.EqualValues(t, 1, i)
	err = d.Decode(&i)
	require.NoError(t, err)
	require.EqualValues(t, 2, i)
	err = d.Decode(&i)
	require.Equal(t, io.EOF, err)
}

func TestDecoderConsecutiveDicts(t *testing.T) {
	bb := bytes.NewBufferString("d4:herp4:derped3:wat1:ke17:oh baby a triple!")

	d := NewDecoder(bb)
	assert.EqualValues(t, "d4:herp4:derped3:wat1:ke17:oh baby a triple!", bb.Bytes())
	assert.EqualValues(t, 0, d.Offset)

	var m map[string]interface{}

	require.NoError(t, d.Decode(&m))
	assert.Len(t, m, 1)
	assert.Equal(t, "derp", m["herp"])
	assert.Equal(t, "d3:wat1:ke17:oh baby a triple!", bb.String())
	assert.EqualValues(t, 14, d.Offset)

	require.NoError(t, d.Decode(&m))
	assert.Equal(t, "k", m["wat"])
	assert.Equal(t, "17:oh baby a triple!", bb.String())
	assert.EqualValues(t, 24, d.Offset)

	var s string
	require.NoError(t, d.Decode(&s))
	assert.Equal(t, "oh baby a triple!", s)
	assert.EqualValues(t, 44, d.Offset)
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
	require.Error(t, Unmarshal([]byte("d6:Normal5:helloe"), &s))
	assert.NoError(t, Unmarshal([]byte("d6:Ignore5:helloe"), &s))
	qt.Assert(t, Unmarshal([]byte("d6:Ignorei42ee"), &s), qt.IsNil)
	assert.EqualValues(t, 42, s.Ignore)
}

// Test unmarshalling []byte into something that has the same kind but
// different type.
func TestDecodeCustomSlice(t *testing.T) {
	type flag byte
	var fs3, fs2 []flag
	// We do a longer slice then a shorter slice to see if the buffers are
	// shared.
	d := NewDecoder(bytes.NewBufferString("3:\x01\x10\xff2:\x04\x0f"))
	require.NoError(t, d.Decode(&fs3))
	require.NoError(t, d.Decode(&fs2))
	assert.EqualValues(t, []flag{1, 16, 255}, fs3)
	assert.EqualValues(t, []flag{4, 15}, fs2)
}

func TestUnmarshalUnusedBytes(t *testing.T) {
	var i int
	require.EqualValues(t, ErrUnusedTrailingBytes{1}, Unmarshal([]byte("i42ee"), &i))
	assert.EqualValues(t, 42, i)
}

func TestUnmarshalByteArray(t *testing.T) {
	var ba [2]byte
	assert.NoError(t, Unmarshal([]byte("2:hi"), &ba))
	assert.EqualValues(t, "hi", ba[:])
}

func TestDecodeDictIntoUnsupported(t *testing.T) {
	// Any type that a dict shouldn't be unmarshallable into.
	var i int
	c := qt.New(t)
	err := Unmarshal([]byte("d1:a1:be"), &i)
	t.Log(err)
	c.Check(err, qt.Not(qt.IsNil))
}

func TestUnmarshalDictKeyNotString(t *testing.T) {
	// Any type that a dict shouldn't be unmarshallable into.
	var i int
	c := qt.New(t)
	err := Unmarshal([]byte("di42e3:yese"), &i)
	t.Log(err)
	c.Check(err, qt.Not(qt.IsNil))
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
	c := qt.New(t)
	test := func(header, tail string, v interface{}, maxStrLen MaxStrLen) {
		strLen := maxStrLen
		if strLen == 0 {
			strLen = DefaultDecodeMaxStrLen
		}
		c.Assert(decodeHugeString(t, strLen, header, tail, v, maxStrLen), qt.IsNil)
		c.Assert(decodeHugeString(t, strLen+1, header, tail, v, maxStrLen), qt.IsNotNil)
	}
	test("d%d:", "i0ee", new(interface{}), 0)
	test("%d:", "", new(interface{}), DefaultDecodeMaxStrLen)
	test("%d:", "", new([]byte), 1)
	test("d3:420%d:", "e", new(struct {
		Hi []byte `bencode:"420"`
	}), 69)
}
