package common

import (
	"errors"
	"fmt"
	"io"
)

// errs is a list of errors.
type Errs []error

// Combine combines multiple non-empty errors into a single error.
func CombineErrs(errs ...error) error {
	var group Errs
	group.Add(errs...)
	return group.Err()
}

// Add adds non-empty errors to the Group.
func (group *Errs) Add(errs ...error) {
	for _, err := range errs {
		if err != nil {
			*group = append(*group, err)
		}
	}
}

// Err returns an error containing all of the non-nil errors.
// If there was only one error, it will return it.
// If there were none, it returns nil.
func (group Errs) Err() error {
	sanitized := group.sanitize()
	if len(sanitized) == 0 {
		return nil
	}
	if len(sanitized) == 1 {
		return sanitized[0]
	}
	return combinedError(sanitized)
}

// sanitize returns group that doesn't contain nil-s
func (group Errs) sanitize() Errs {
	// sanity check for non-nil errors
	for i, err := range group {
		if err == nil {
			sanitized := make(Errs, 0, len(group)-1)
			sanitized = append(sanitized, group[:i]...)
			sanitized.Add(group[i+1:]...)
			return sanitized
		}
	}

	return group
}

// combinedError is a list of non-empty errors
type combinedError []error

// Unwrap returns the first error.
func (group combinedError) Unwrap() []error { return group }

// Error returns error string delimited by semicolons.
func (group combinedError) Error() string { return fmt.Sprintf("%v", group) }

// Format handles the formatting of the error. Using a "+" on the format
// string specifier will cause the errors to be formatted with "+" and
// delimited by newlines. They are delimited by semicolons otherwise.
func (group combinedError) Format(f fmt.State, c rune) {
	delim := "; "
	if f.Flag(int('+')) {
		io.WriteString(f, "errs:\n--- ")
		delim = "\n--- "
	}

	for i, err := range group {
		if i != 0 {
			io.WriteString(f, delim)
		}
		if formatter, ok := err.(fmt.Formatter); ok {
			formatter.Format(f, c)
		} else {
			fmt.Fprintf(f, "%v", err)
		}
	}
}

type eofReadCloser struct{}

func (eofReadCloser) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (eofReadCloser) Close() error {
	return nil
}

type multiReadCloser struct {
	readers []io.ReadCloser
}

// MultiReadCloser is a MultiReader extension that returns a ReaderCloser
// that's the logical concatenation of the provided input readers.
// They're read sequentially. Once all inputs have returned EOF,
// Read will return EOF.  If any of the readers return a non-nil,
// non-EOF error, Read will return that error.
func MultiReadCloser(readers ...io.ReadCloser) io.ReadCloser {
	r := make([]io.ReadCloser, len(readers))
	copy(r, readers)
	return &multiReadCloser{r}
}

func (mr *multiReadCloser) Read(p []byte) (n int, err error) {
	for len(mr.readers) > 0 {
		// Optimization to flatten nested multiReaders.
		if len(mr.readers) == 1 {
			if r, ok := mr.readers[0].(*multiReadCloser); ok {
				mr.readers = r.readers
				continue
			}
		}
		n, err = mr.readers[0].Read(p)
		if errors.Is(err, io.EOF) {
			err = mr.readers[0].Close()
			// Use eofReader instead of nil to avoid nil panic
			mr.readers[0] = eofReadCloser{}
			mr.readers = mr.readers[1:]
		}
		if n > 0 || !errors.Is(err, io.EOF) {
			if errors.Is(err, io.EOF) && len(mr.readers) > 0 {
				// Don't return EOF yet. More readers remain.
				err = nil
			}
			return
		}
	}
	return 0, io.EOF
}

func (mr *multiReadCloser) Close() error {
	errlist := make([]error, len(mr.readers))
	for i, r := range mr.readers {
		errlist[i] = r.Close()
	}
	return CombineErrs(errlist...)
}
