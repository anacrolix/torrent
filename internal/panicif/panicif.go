package panicif

import "fmt"

func NotEqual[T comparable](a, b T) {
	if a != b {
		panic(fmt.Sprintf("%v != %v", a, b))
	}
}

func False(b bool) {
	if !b {
		panic("is false")
	}
}

func True(b bool) {
	if b {
		panic("is true")
	}
}
