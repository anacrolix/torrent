package ctxrw

import (
	"context"
	"io"

	g "github.com/anacrolix/generics"
)

type contextedReader struct {
	ctx context.Context
	r   io.Reader
}

func (me contextedReader) Read(p []byte) (n int, err error) {
	return contextedReadOrWrite(me.ctx, me.r.Read, p)
}

type contextedWriter struct {
	ctx context.Context
	w   io.Writer
}

// This is problematic. If you return with a context error, a read or write is still pending, and
// could mess up the stream.
func contextedReadOrWrite(ctx context.Context, method func(b []byte) (int, error), b []byte) (_ int, err error) {
	asyncCh := make(chan g.Result[int], 1)
	go func() {
		asyncCh <- g.ResultFromTuple(method(b))
	}()
	select {
	case <-ctx.Done():
		err = context.Cause(ctx)
		return
	case res := <-asyncCh:
		return res.AsTuple()
	}

}

func (me contextedWriter) Write(p []byte) (n int, err error) {
	return contextedReadOrWrite(me.ctx, me.w.Write, p)
}

func WrapReadWriter(ctx context.Context, rw io.ReadWriter) io.ReadWriter {
	return struct {
		io.Reader
		io.Writer
	}{
		contextedReader{
			ctx: ctx,
			r:   rw,
		},
		contextedWriter{
			ctx: ctx,
			w:   rw,
		},
	}
}
