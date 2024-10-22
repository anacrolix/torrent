// Package langx provides small utility functions to extend the standard golang language.
package langx

// Autoptr converts a value into a pointer
func Autoptr[T any](a T) *T {
	return &a
}

// Autoderef safely converts a pointer to its value, uses the zero value for nil.
func Autoderef[T any](a *T) (zero T) {
	if a == nil {
		return zero
	}

	return *a
}

func DerefOrZero[T any](a *T) (zero T) {
	if a == nil {
		return zero
	}

	return *a
}

func DefaultIfZero[T comparable](fallback T, v T) T {
	var (
		x T
	)

	if v != x {
		return v
	}

	return fallback
}

func Must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}

	return v
}

func MustZero[T any](v T, err error) T {
	var (
		x T
	)
	if err != nil {
		return x
	}

	return v
}

func Clone[T any, Y ~func(*T)](v T, options ...Y) T {
	dup := v
	for _, opt := range options {
		opt(&dup)
	}

	return dup
}
