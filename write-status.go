package torrent

import (
	"fmt"
	"io"
)

type statusWriter struct {
	w    io.Writer
	line []any
}

func (me *statusWriter) a(a any) {
	me.line = append(me.line, a)
}

func (me *statusWriter) as(a ...any) {
	me.line = append(me.line, a...)
}

func (me *statusWriter) f(fmtStr string, args ...any) {
	me.line = append(me.line, fmt.Sprintf(fmtStr, args...))
}

func (me *statusWriter) nl() {
	fmt.Fprintln(me.w, me.line...)
	me.line = nil
}
