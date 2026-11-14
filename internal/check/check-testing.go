//go:build go1.21

package check

import "testing"

func init() {
	if testing.Testing() {
		Enabled = true
	}
}
