package utHolepunch

import (
	"math/rand"
	"testing"
)

func TestUnknownErrCodeError(t *testing.T) {
	ErrCode(rand.Uint32()).Error()
}
