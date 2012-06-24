package bencode

import "bytes"
import "bufio"
import "reflect"
import "strconv"
import "io"

//----------------------------------------------------------------------------
// Errors
//----------------------------------------------------------------------------

// In case if marshaler cannot encode a type in bencode, it will return this
// error. Typical example of such type is float32/float64 which has no bencode
// representation
type MarshalTypeError struct {
	Type reflect.Type
}

func (this *MarshalTypeError) Error() string {
	return "bencode: unsupported type: " + this.Type.String()
}

// Unmarshal argument must be a non-nil value of some pointer type.
type UnmarshalInvalidArgError struct {
	Type reflect.Type
}

func (e *UnmarshalInvalidArgError) Error() string {
	if e.Type == nil {
		return "bencode: Unmarshal(nil)"
	}

	if e.Type.Kind() != reflect.Ptr {
		return "bencode: Unmarshal(non-pointer " + e.Type.String() + ")"
	}
	return "bencode: Unmarshal(nil " + e.Type.String() + ")"
}

// Unmarshaler spotted a value that was not appropriate for a given specific Go
// value
type UnmarshalTypeError struct {
	Value string
	Type  reflect.Type
}

func (e *UnmarshalTypeError) Error() string {
	return "bencode: value (" + e.Value + ") is not appropriate for type: " +
		e.Type.String()
}

// Unmarshaler tried to write to an unexported (therefore unwritable) field.
type UnmarshalFieldError struct {
	Key   string
	Type  reflect.Type
	Field reflect.StructField
}

func (e *UnmarshalFieldError) Error() string {
	return "bencode: key \"" + e.Key + "\" led to an unexported field \"" +
		e.Field.Name + "\" in type: " + e.Type.String()
}

type SyntaxError struct {
	Offset int64  // location of the error
	what   string // error description
}

func (e *SyntaxError) Error() string {
	return "bencode: syntax error (offset: " +
		strconv.FormatInt(e.Offset, 10) +
		"): " + e.what
}

type MarshalerError struct {
	Type reflect.Type
	Err  error
}

func (e *MarshalerError) Error() string {
	return "bencode: error calling MarshalBencode for type " + e.Type.String() + ": " + e.Err.Error()
}

//----------------------------------------------------------------------------
// Interfaces
//----------------------------------------------------------------------------

// unused for now (TODO)
type Marshaler interface {
	MarshalBencode() ([]byte, error)
}

// unused for now (TODO)
type Unmarshaler interface {
	UnmarshalBencode([]byte) error
}

//----------------------------------------------------------------------------
// Stateless interface
//----------------------------------------------------------------------------

func Marshal(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	e := encoder{Writer: bufio.NewWriter(&buf)}
	err := e.encode(v)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func Unmarshal(data []byte, v interface{}) error {
	e := decoder{Reader: bufio.NewReader(bytes.NewBuffer(data))}
	return e.decode(v)
}

//----------------------------------------------------------------------------
// Stateful interface
//----------------------------------------------------------------------------

type Decoder struct {
	d decoder
}

func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{decoder{Reader: bufio.NewReader(r)}}
}

func (d *Decoder) Decode(v interface{}) error {
	return d.d.decode(v)
}

type Encoder struct {
	e encoder
}

func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{encoder{Writer: bufio.NewWriter(w)}}
}

func (e *Encoder) Encode(v interface{}) error {
	err := e.e.encode(v)
	if err != nil {
		return err
	}
	return nil
}
