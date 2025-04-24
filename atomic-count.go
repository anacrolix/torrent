package torrent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync/atomic"
)

type Count struct {
	n int64
}

var _ fmt.Stringer = (*Count)(nil)

func (me *Count) Add(n int64) {
	atomic.AddInt64(&me.n, n)
}

func (me *Count) Int64() int64 {
	return atomic.LoadInt64(&me.n)
}

func (me *Count) String() string {
	return fmt.Sprintf("%v", me.Int64())
}

func (me *Count) MarshalJSON() ([]byte, error) {
	return json.Marshal(me.n)
}

// TODO: Can this use more generics to speed it up? Should we be checking the field types?
func copyCountFields[T any](src *T) (dst T) {
	for i := 0; i < reflect.TypeFor[T]().NumField(); i++ {
		n := reflect.ValueOf(src).Elem().Field(i).Addr().Interface().(*Count).Int64()
		reflect.ValueOf(&dst).Elem().Field(i).Addr().Interface().(*Count).Add(n)
	}
	return
}
