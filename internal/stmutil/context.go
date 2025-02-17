package stmutil

import (
	"context"

	"github.com/anacrolix/stm"
)

type ctxkey int

const (
	contextkey ctxkey = iota
)

// Returns an STM var that contains a bool equal to `ctx.Err != nil`, and a cancel function to be
// called when the user is no longer interested in the var.
func ContextDoneVar(ctx context.Context) (context.Context, context.CancelFunc, *stm.Var[bool]) {
	ctx, done := context.WithCancel(ctx)
	if v := ctx.Value(contextkey); v != nil {
		return ctx, done, v.(*stm.Var[bool])
	}

	v := stm.NewBuiltinEqVar(ctx.Err() != nil)
	go func() {
		<-ctx.Done()
		stm.AtomicSet(v, true)
	}()
	return context.WithValue(ctx, contextkey, v), done, v
}
