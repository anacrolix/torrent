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
	srcValue := reflect.ValueOf(src).Elem()
	dstValue := reflect.ValueOf(&dst).Elem()
	for i := 0; i < reflect.TypeFor[T]().NumField(); i++ {
		n := srcValue.Field(i).Addr().Interface().(*Count).Int64()
		dstValue.Field(i).Addr().Interface().(*Count).Add(n)
	}
	return
}
