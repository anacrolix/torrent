package panicif

import "fmt"

func NotEqual[T comparable](a, b T) {
	if a != b {
		panic(fmt.Sprintf("%v != %v", a, b))
	}
}
