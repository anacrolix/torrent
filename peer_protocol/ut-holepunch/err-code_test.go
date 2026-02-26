package utHolepunch

import (
	"math/rand"
	"testing"
)

func TestUnknownErrCodeError(t *testing.T) {
	_ = ErrCode(rand.Uint32()).Error()
}
