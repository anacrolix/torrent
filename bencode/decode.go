package bencode

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"runtime"
	"strconv"
	"sync"
)

// The default bencode string length limit. This is a poor attempt to prevent excessive memory
// allocation when parsing, but also leaves the window open to implement a better solution.
const DefaultDecodeMaxStrLen = 1<<27 - 1 // ~128MiB

// Maximum length in bytes of an integer decoded into a *big.Int. That's far beyond what any known
// use of bencode needs.
const maxBigIntLen = 1 << 10

// The default maximum nesting depth of bencode dicts and lists. The decoder recurses once per
// nesting level, so without a limit a few million levels of nesting would exhaust the goroutine
// stack (a fatal, unrecoverable error in Go). Real-world metainfo nests only a handful of levels
// deep, so this bound is not restrictive in practice.
const DefaultMaxDepth = 64

type MaxStrLen = int64

type Decoder struct {
	// Maximum parsed bencode string length. Defaults to DefaultMaxStrLen if zero.
	MaxStrLen MaxStrLen
	// Maximum nesting depth of bencode dicts and lists. Defaults to DefaultMaxDepth if zero.
	// Input nested deeper than this is rejected with a SyntaxError. The decoder recurses once per
	// nesting level, so raising this trades stack-safety against accepting deeper input.
	MaxDepth int

	r interface {
		io.ByteScanner
		io.Reader
	}
	// Sum of bytes used to Decode values.
	Offset int64
	// Current nesting depth of dicts and lists during a Decode. It is 0 outside of a Decode call.
	depth int
	buf   bytes.Buffer
}

func (d *Decoder) Decode(v interface{}) (err error) {
	defer func() {
		if err != nil {
			return
		}
		r := recover()
		if r == nil {
			return
		}
		_, ok := r.(runtime.Error)
		if ok {
			panic(r)
		}
		if err, ok = r.(error); !ok {
			panic(r)
		}
		// Errors thrown from deeper in parsing are unexpected. At value boundaries, errors should
		// be returned directly (at least until all the panic nonsense is removed entirely).
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
	}()

	pv := reflect.ValueOf(v)
	if pv.Kind() != reflect.Pointer || pv.IsNil() {
		return &UnmarshalInvalidArgError{reflect.TypeOf(v)}
	}

	ok, err := d.parseValue(pv.Elem())
	if err != nil {
		return
	}
	if !ok {
		d.throwSyntaxError(d.Offset-1, errors.New("unexpected 'e'"))
	}
	return
}

// Check for EOF in the decoder input stream. Used to assert the input ends on a clean message
// boundary.
func (d *Decoder) ReadEOF() error {
	_, err := d.r.ReadByte()
	if err == nil {
		err := d.r.UnreadByte()
		if err != nil {
			panic(err)
		}
		return errors.New("expected EOF")
	}
	if err == io.EOF {
		return nil
	}
	return fmt.Errorf("expected EOF, got %w", err)
}

func checkForUnexpectedEOF(err error, offset int64) {
	if err == io.EOF {
		panic(&SyntaxError{
			Offset: offset,
			What:   io.ErrUnexpectedEOF,
		})
	}
}

// Returns the next input byte, and whether there was one, without the error handling of readByte.
func (d *Decoder) nextByte() (byte, error) {
	b, err := d.r.ReadByte()
	if err == nil {
		d.Offset++
	}
	return b, err
}

// Steps back over the byte just returned by nextByte.
func (d *Decoder) unreadByte() {
	d.Offset--
	if err := d.r.UnreadByte(); err != nil {
		panic(err)
	}
}

func (d *Decoder) readByte() byte {
	b, err := d.r.ReadByte()
	if err != nil {
		checkForUnexpectedEOF(err, d.Offset)
		panic(err)
	}

	d.Offset++
	return b
}

// Reads up to and including sep, and returns the token before it, including any bytes already
// written to d.buf. The token is only valid until d.buf is next modified.
func (d *Decoder) readToken(sep byte) []byte {
	d.readUntil(sep)
	return d.buf.Bytes()
}

// reads data writing it to 'd.buf' until 'sep' byte is encountered, 'sep' byte
// is consumed, but not included into the 'd.buf'
func (d *Decoder) readUntil(sep byte) {
	for {
		b := d.readByte()
		if b == sep {
			return
		}
		d.buf.WriteByte(b)
	}
}

func checkForIntParseError(err error, offset int64) {
	if err != nil {
		panic(&SyntaxError{
			Offset: offset,
			What:   err,
		})
	}
}

func (d *Decoder) throwSyntaxError(offset int64, err error) {
	panic(&SyntaxError{
		Offset: offset,
		What:   err,
	})
}

// Assume the 'i' is already consumed. Read and validate the rest of an int, returning its digits.
func (d *Decoder) readInt() ([]byte, error) {
	b := d.readToken('e')
	if err := checkIntToken(b); err != nil {
		return nil, err
	}
	return b, nil
}

// called when 'i' was consumed, for the integer type in v.
func (d *Decoder) parseInt(v reflect.Value) error {
	start := d.Offset - 1

	// Type errors return early, and must not leave the integer in the buffer.
	defer d.buf.Reset()
	tok, err := d.readInt()
	if err != nil {
		return err
	}
	s := bytesAsString(tok)

	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, 64)
		checkForIntParseError(err, start)

		if v.OverflowInt(n) {
			return &UnmarshalTypeError{
				BencodeTypeName:     "int",
				UnmarshalTargetType: v.Type(),
			}
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(s, 10, 64)
		checkForIntParseError(err, start)

		if v.OverflowUint(n) {
			return &UnmarshalTypeError{
				BencodeTypeName:     "int",
				UnmarshalTargetType: v.Type(),
			}
		}
		v.SetUint(n)
	case reflect.Bool:
		v.SetBool(s != "0")
	default:
		return &UnmarshalTypeError{
			BencodeTypeName:     "int",
			UnmarshalTargetType: v.Type(),
		}
	}
	d.buf.Reset()
	return nil
}

func checkIntToken(b []byte) error {
	if len(b) <= 1 {
		return nil
	}
	if b[0] == '-' {
		b = b[1:]
	}
	if b[0] < '1' || b[0] > '9' {
		return errors.New("invalid leading digit")
	}
	return nil
}

func (d *Decoder) parseStringLength() (int, error) {
	// We should have already consumed the first byte of the length into the Decoder buf.
	start := d.Offset - 1
	tok := d.readToken(':')
	if err := checkIntToken(tok); err != nil {
		return 0, err
	}
	// Really the limit should be the uint size for the platform. But we can't pass in an allocator,
	// or limit total memory use in Go, the best we might hope to do is limit the size of a single
	// decoded value (by reading it in in-place and then operating on a view).
	length, err := strconv.ParseInt(bytesAsString(tok), 10, 0)
	checkForIntParseError(err, start)
	if int64(length) > d.getMaxStrLen() {
		err = fmt.Errorf("parsed string length %v exceeds limit (%v)", length, d.getMaxStrLen())
	}
	d.buf.Reset()
	return int(length), err
}

// Reads a string of the given length, into buf if it's set. The length comes from the input, so it's
// only allocated up front if the reader is a bytes.Reader that holds the whole string, or if it's no
// more than io.ReadAll would allocate anyway. Otherwise the string grows as the data arrives.
func (d *Decoder) readString(length int, buf *bytes.Buffer) (b []byte) {
	var n int
	var err error
	if r, ok := d.r.(*bytes.Reader); length <= bytes.MinRead || ok && r.Len() >= length {
		if buf == nil {
			b = make([]byte, length)
		} else {
			buf.Grow(length)
			b = buf.AvailableBuffer()[:length]
		}
		n, err = io.ReadFull(d.r, b)
	} else {
		lr := io.LimitReader(d.r, int64(length))
		if buf == nil {
			b, err = io.ReadAll(lr)
		} else {
			_, err = buf.ReadFrom(lr)
			b = buf.Bytes()
		}
		n = len(b)
		if err == nil && n < length {
			// The limited reader hides EOF. Report the short read as io.ReadFull would.
			err = io.EOF
			if n > 0 {
				err = io.ErrUnexpectedEOF
			}
		}
	}
	d.Offset += int64(n)
	if err != nil {
		checkForUnexpectedEOF(err, d.Offset)
		panic(&SyntaxError{
			Offset: d.Offset,
			What:   errors.New("unexpected I/O error: " + err.Error()),
		})
	}
	return b
}

func (d *Decoder) parseString(v reflect.Value) error {
	length, err := d.parseStringLength()
	if err != nil {
		return err
	}
	defer d.buf.Reset()

	switch v.Kind() {
	case reflect.String:
		v.SetString(bytesAsString(d.readString(length, nil)))
		return nil
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.Uint8 {
			break
		}
		v.SetBytes(d.readString(length, nil))
		return nil
	case reflect.Array:
		if v.Type().Elem().Kind() != reflect.Uint8 {
			break
		}
		reflect.Copy(v, reflect.ValueOf(d.readString(length, &d.buf)))
		return nil
	case reflect.Bool:
		x, err := strconv.ParseBool(bytesAsString(d.readString(length, &d.buf)))
		if err != nil {
			x = length != 0
		}
		v.SetBool(x)
		return nil
	}
	// Can't move this into default clause because some cases above fail through to here after
	// additional checks.
	d.readString(length, &d.buf)
	// I believe we return here to support "ignore_unmarshal_type_error".
	return &UnmarshalTypeError{
		BencodeTypeName:     "string",
		UnmarshalTargetType: v.Type(),
	}
}

// Info for parsing a struct field from a dict value.
type dictField struct {
	Type reflect.Type
	// The dict key.
	Key string
	// Field index path from the outer struct, through any embedded structs.
	Index []int
	Tags  tag
}

var structFields sync.Map // reflect.Type -> map[string]dictField

func parseStructFields(struct_ reflect.Type, index []int, each func(key string, df dictField)) {
	for i, n := 0, struct_.NumField(); i < n; i++ {
		f := struct_.Field(i)
		fieldIndex := append(index[:len(index):len(index)], i)
		if f.Anonymous {
			t := f.Type
			if t.Kind() == reflect.Pointer {
				t = t.Elem()
			}
			parseStructFields(t, fieldIndex, each)
			continue
		}
		tagStr := f.Tag.Get("bencode")
		if tagStr == "-" {
			continue
		}
		tag := parseTag(tagStr)
		key := tag.Key()
		if key == "" {
			key = f.Name
		}
		each(key, dictField{f.Type, key, fieldIndex, tag})
	}
}

func getStructFields(struct_ reflect.Type) map[string]dictField {
	if m, ok := structFields.Load(struct_); ok {
		return m.(map[string]dictField)
	}
	m := make(map[string]dictField)
	parseStructFields(struct_, nil, func(key string, sf dictField) {
		m[key] = sf
	})
	actual, _ := structFields.LoadOrStore(struct_, m)
	return actual.(map[string]dictField)
}

// Returns the struct field at index, allocating any nil embedded struct pointers on the way.
func fieldByIndexAlloc(v reflect.Value, index []int) reflect.Value {
	for i, x := range index {
		if i > 0 && v.Kind() == reflect.Pointer {
			if v.IsNil() {
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		v = v.Field(x)
	}
	return v
}

func (d *Decoder) parseDict(v reflect.Value) error {
	// At this point 'd' byte was consumed, now read key/value pairs.
	switch v.Kind() {
	case reflect.Struct:
		return d.parseDictStruct(v)
	case reflect.Map:
		return d.parseDictMap(v)
	default:
		return fmt.Errorf("cannot parse dicts into %v", v.Type())
	}
}

func (d *Decoder) parseDictMap(v reflect.Value) error {
	// The key type does not need to be a string for maps.
	mapType := v.Type()
	for {
		keyValue := reflect.New(mapType.Key()).Elem()
		ok, err := d.parseValue(keyValue)
		if err != nil {
			return fmt.Errorf("error parsing dict key: %w", err)
		}
		if !ok {
			return nil
		}
		setValue := reflect.New(mapType.Elem()).Elem()
		ok, err = d.parseValue(setValue)
		if err != nil {
			return fmt.Errorf("parsing value for key %q: %w", keyValue, err)
		}
		if !ok {
			return fmt.Errorf("missing value for key %q", keyValue)
		}
		if v.IsNil() {
			v.Set(reflect.MakeMap(mapType))
		}
		v.SetMapIndex(keyValue, setValue)
	}
}

// Parses a dict key for a struct. ok is false if the dict ended instead. The key is only valid until
// d.buf is next modified.
func (d *Decoder) parseStructKey() (key []byte, ok bool, err error) {
	b, err := d.nextByte()
	if err != nil {
		return nil, false, err
	}
	if b == 'e' {
		return nil, false, nil
	}
	if b >= '0' && b <= '9' {
		// The common case, a string key. This is what parseValue does for a string target.
		d.buf.Reset()
		d.buf.WriteByte(b)
		length, err := d.parseStringLength()
		if err != nil {
			return nil, false, err
		}
		return d.readString(length, &d.buf), true, nil
	}
	d.unreadByte()
	var s string
	ok, err = d.parseValue(reflect.ValueOf(&s).Elem())
	return []byte(s), ok, err
}

func (d *Decoder) parseDictStruct(v reflect.Value) error {
	fields := getStructFields(v.Type())
	for {
		key, ok, err := d.parseStructKey()
		if err != nil {
			return fmt.Errorf("error parsing dict key: %w", err)
		}
		if !ok {
			return nil
		}
		df, ok := fields[string(key)]
		if !ok {
			// The key is in d.buf, which parsing the value will overwrite.
			key = bytes.Clone(key)
		}
		// A key read through the buffer (one longer than bytes.MinRead from a stream) is left in it,
		// and would corrupt the next token read.
		d.buf.Reset()
		if !ok {
			// There's nowhere to put the value, but it's still parsed (and validated) as it would
			// be for an interface{}.
			if_, _ := d.parseValueInterface()
			if if_ == nil {
				return fmt.Errorf("error parsing value for key %q", key)
			}
			continue
		}
		fv := fieldByIndexAlloc(v, df.Index)
		// Decode straight into the field if it's still zero, which is equivalent to decoding into
		// a new value and then assigning it, but saves an allocation. Otherwise (a duplicate key, or
		// a non-empty target), use a fresh value so the result is the same as for a zero target.
		setValue := fv
		inPlace := fv.IsZero()
		if !inPlace {
			setValue = reflect.New(df.Type).Elem()
		}
		ok, err = d.parseValue(setValue)
		if err != nil {
			var target *UnmarshalTypeError
			if !errors.As(err, &target) || !df.Tags.IgnoreUnmarshalTypeError() {
				return fmt.Errorf("parsing value for key %q: %w", df.Key, err)
			}
		}
		if !ok {
			return fmt.Errorf("missing value for key %q", df.Key)
		}
		if !inPlace {
			fv.Set(setValue)
		}
	}
}

func (d *Decoder) parseList(v reflect.Value) error {
	switch v.Kind() {
	default:
		// If the list is a singleton of the expected type, use that value. See
		// https://github.com/anacrolix/torrent/issues/297.
		l := reflect.New(reflect.SliceOf(v.Type()))
		if err := d.parseList(l.Elem()); err != nil {
			return err
		}
		if l.Elem().Len() != 1 {
			return &UnmarshalTypeError{
				BencodeTypeName:     "list",
				UnmarshalTargetType: v.Type(),
			}
		}
		v.Set(l.Elem().Index(0))
		return nil
	case reflect.Array, reflect.Slice:
		// We can work with this. Normal case, fallthrough.
	}

	i := 0
	for ; ; i++ {
		if v.Kind() == reflect.Slice && i >= v.Len() {
			v.Set(reflect.Append(v, reflect.Zero(v.Type().Elem())))
		}

		if i < v.Len() {
			ok, err := d.parseValue(v.Index(i))
			if err != nil {
				return err
			}
			if !ok {
				break
			}
		} else {
			_, ok := d.parseValueInterface()
			if !ok {
				break
			}
		}
	}

	if i < v.Len() {
		if v.Kind() == reflect.Array {
			z := reflect.Zero(v.Type().Elem())
			for n := v.Len(); i < n; i++ {
				v.Index(i).Set(z)
			}
		} else {
			v.SetLen(i)
		}
	}

	if i == 0 && v.Kind() == reflect.Slice {
		v.Set(reflect.MakeSlice(v.Type(), 0, 0))
	}
	return nil
}

func (d *Decoder) readOneValue() bool {
	b, err := d.r.ReadByte()
	if err != nil {
		panic(err)
	}
	if b == 'e' {
		d.r.UnreadByte()
		return false
	} else {
		d.Offset++
		d.buf.WriteByte(b)
	}

	switch b {
	case 'd', 'l':
		defer d.leaveContainer()
		d.enterContainer(d.Offset - 1)
		// read until there is nothing to read
		for d.readOneValue() {
		}
		// consume 'e' as well
		b = d.readByte()
		d.buf.WriteByte(b)
	case 'i':
		d.readUntil('e')
		d.buf.WriteString("e")
	default:
		if b >= '0' && b <= '9' {
			start := d.buf.Len() - 1
			d.readUntil(':')
			length, err := strconv.ParseInt(bytesAsString(d.buf.Bytes()[start:]), 10, 64)
			checkForIntParseError(err, d.Offset-1)

			d.buf.WriteString(":")
			if length > d.getMaxStrLen() {
				d.throwSyntaxError(d.Offset-1, fmt.Errorf("parsed string length %v exceeds limit (%v)", length, d.getMaxStrLen()))
			}
			n, err := io.CopyN(&d.buf, d.r, length)
			d.Offset += n
			if err != nil {
				checkForUnexpectedEOF(err, d.Offset)
				panic(&SyntaxError{
					Offset: d.Offset,
					What:   errors.New("unexpected I/O error: " + err.Error()),
				})
			}
			break
		}

		d.raiseUnknownValueType(b, d.Offset-1)
	}

	return true
}

type unmarshalerKind uint8

const (
	notUnmarshaler unmarshalerKind = iota
	valueUnmarshaler
	pointerUnmarshaler
)

var unmarshalerKinds sync.Map // reflect.Type -> unmarshalerKind

// Caches how values of type t implement Unmarshaler, since checking is relatively expensive.
func getUnmarshalerKind(t reflect.Type) unmarshalerKind {
	if k, ok := unmarshalerKinds.Load(t); ok {
		return k.(unmarshalerKind)
	}
	k := notUnmarshaler
	if t.Implements(unmarshalerType) {
		k = valueUnmarshaler
	} else if reflect.PointerTo(t).Implements(unmarshalerType) {
		k = pointerUnmarshaler
	}
	unmarshalerKinds.Store(t, k)
	return k
}

func (d *Decoder) parseUnmarshaler(v reflect.Value) bool {
	switch getUnmarshalerKind(v.Type()) {
	case notUnmarshaler:
		return false
	case pointerUnmarshaler:
		v = v.Addr()
	}
	d.buf.Reset()
	// Don't leave the raw value in the buffer, where it would corrupt the next integer read.
	defer d.buf.Reset()
	if !d.readOneValue() {
		return false
	}
	raw := d.buf.Bytes()
	m := v.Interface().(Unmarshaler)
	err := m.UnmarshalBencode(raw)
	if err != nil {
		panic(&UnmarshalerError{v.Type(), err})
	}
	return true
}

// Returns true if there was a value and it's now stored in 'v'. Otherwise, there was an end symbol
// ("e") and no value was stored.
func (d *Decoder) parseValue(v reflect.Value) (bool, error) {
	// we support one level of indirection at the moment
	if v.Kind() == reflect.Pointer {
		// if the pointer is nil, allocate a new element of the type it
		// points to
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		v = v.Elem()
	}

	if d.parseUnmarshaler(v) {
		return true, nil
	}

	// common case: interface{}
	if v.Kind() == reflect.Interface && v.NumMethod() == 0 {
		iface, _ := d.parseValueInterface()
		v.Set(reflect.ValueOf(iface))
		return true, nil
	}

	b, err := d.nextByte()
	if err != nil {
		return false, err
	}

	switch b {
	case 'e':
		return false, nil
	case 'd':
		defer d.leaveContainer()
		d.enterContainer(d.Offset - 1)
		return true, d.parseDict(v)
	case 'l':
		defer d.leaveContainer()
		d.enterContainer(d.Offset - 1)
		return true, d.parseList(v)
	case 'i':
		return true, d.parseInt(v)
	default:
		if b >= '0' && b <= '9' {
			// It's a string.
			d.buf.Reset()
			// Write the first digit of the length to the buffer.
			d.buf.WriteByte(b)
			return true, d.parseString(v)
		}

		d.raiseUnknownValueType(b, d.Offset-1)
	}
	panic("unreachable")
}

// An unknown bencode type character was encountered.
func (d *Decoder) raiseUnknownValueType(b byte, offset int64) {
	panic(&SyntaxError{
		Offset: offset,
		What:   fmt.Errorf("unknown value type %+q", b),
	})
}

func (d *Decoder) parseValueInterface() (interface{}, bool) {
	b, err := d.nextByte()
	if err != nil {
		panic(err)
	}

	switch b {
	case 'e':
		return nil, false
	case 'd':
		defer d.leaveContainer()
		d.enterContainer(d.Offset - 1)
		return d.parseDictInterface(), true
	case 'l':
		defer d.leaveContainer()
		d.enterContainer(d.Offset - 1)
		return d.parseListInterface(), true
	case 'i':
		return d.parseIntInterface(), true
	default:
		if b >= '0' && b <= '9' {
			// string
			// append first digit of the length to the buffer
			d.buf.WriteByte(b)
			return d.parseStringInterface(), true
		}

		d.raiseUnknownValueType(b, d.Offset-1)
		panic("unreachable")
	}
}

// Called after 'i', for an arbitrary integer size.
func (d *Decoder) parseIntInterface() (ret interface{}) {
	start := d.Offset - 1

	tok, err := d.readInt()
	if err != nil {
		panic(err)
	}
	n, err := strconv.ParseInt(bytesAsString(tok), 10, 64)
	if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
		// Converting decimal digits to a big.Int takes superlinear time, so bound the input.
		if len(tok) > maxBigIntLen {
			panic(&SyntaxError{
				Offset: start,
				What:   fmt.Errorf("integer length %v exceeds limit (%v)", len(tok), maxBigIntLen),
			})
		}
		i := new(big.Int)
		_, ok := i.SetString(string(tok), 10)
		if !ok {
			panic(&SyntaxError{
				Offset: start,
				What:   errors.New("failed to parse integer"),
			})
		}
		ret = i
	} else {
		checkForIntParseError(err, start)
		ret = n
	}

	d.buf.Reset()
	return
}

func (d *Decoder) readBytes(length int) []byte {
	b, err := io.ReadAll(io.LimitReader(d.r, int64(length)))
	if err != nil {
		panic(err)
	}
	if len(b) != length {
		panic(fmt.Errorf("read %v bytes expected %v", len(b), length))
	}
	return b
}

func (d *Decoder) parseStringInterface() string {
	length, err := d.parseStringLength()
	if err != nil {
		panic(err)
	}
	b := d.readBytes(int(length))
	d.Offset += int64(len(b))
	if err != nil {
		panic(&SyntaxError{Offset: d.Offset, What: err})
	}
	return bytesAsString(b)
}

func (d *Decoder) parseDictInterface() interface{} {
	dict := make(map[string]interface{})
	var lastKey string
	lastKeyOk := false
	for {
		start := d.Offset
		keyi, ok := d.parseValueInterface()
		if !ok {
			break
		}

		key, ok := keyi.(string)
		if !ok {
			panic(&SyntaxError{
				Offset: d.Offset,
				What:   errors.New("non-string key in a dict"),
			})
		}
		if lastKeyOk && key <= lastKey {
			d.throwSyntaxError(start, fmt.Errorf("dict keys unsorted: %q <= %q", key, lastKey))
		}
		start = d.Offset
		valuei, ok := d.parseValueInterface()
		if !ok {
			d.throwSyntaxError(start, fmt.Errorf("dict elem missing value [key=%v]", key))
		}

		lastKey = key
		lastKeyOk = true
		dict[key] = valuei
	}
	return dict
}

func (d *Decoder) parseListInterface() (list []interface{}) {
	list = []interface{}{}
	valuei, ok := d.parseValueInterface()
	for ok {
		list = append(list, valuei)
		valuei, ok = d.parseValueInterface()
	}
	return
}

func (d *Decoder) getMaxStrLen() int64 {
	if d.MaxStrLen == 0 {
		return DefaultDecodeMaxStrLen
	}
	return d.MaxStrLen
}

// Returns the effective maximum dict/list nesting depth for this Decoder.
func (d *Decoder) getMaxDepth() int {
	if d.MaxDepth == 0 {
		return DefaultMaxDepth
	}
	return d.MaxDepth
}

// enterContainer is called once the 'd' or 'l' byte starting a dict or list has been consumed (at
// offset). The caller must have already scheduled leaveContainer with defer: because the leave is
// registered before the depth check can panic, the counter stays balanced on error returns and
// panics alike.
func (d *Decoder) enterContainer(offset int64) {
	maxDepth := d.getMaxDepth()
	d.depth++
	if d.depth > maxDepth {
		d.throwSyntaxError(offset, fmt.Errorf("nesting depth %d exceeds limit (%d)", d.depth, maxDepth))
	}
}

func (d *Decoder) leaveContainer() {
	d.depth--
}
