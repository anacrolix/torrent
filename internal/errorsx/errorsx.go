package errorsx

import (
	"errors"
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/james-lawrence/torrent/internal/langx"
)

// Zero logs that the error occurred but otherwise ignores it.
func Zero[T any](v T, err error) T {
	if err == nil {
		return v
	}

	if cause := log.Output(2, fmt.Sprintln(err)); cause != nil {
		panic(cause)
	}

	return v
}

func Log(err error) {
	if err == nil {
		return
	}

	if cause := log.Output(2, fmt.Sprintln(err)); cause != nil {
		log.Println(cause)
	}
}

func Must[T any](v T, err error) T {
	if err == nil {
		return v
	}

	panic(err)
}

// panic if zero value.
func PanicZero[T comparable](v T) T {
	var (
		x T
	)

	if v == x {
		panic("zero value detected")
	}

	return v
}

// Compact returns the first error in the set, if any.
func Compact(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}

	return nil
}

// String useful wrapper for string constants as errors.
type String string

func (t String) Error() string {
	return string(t)
}

func Authorization(cause error) error {
	return unauthorized{
		error: cause,
	}
}

type Unauthorized interface {
	Unauthorized()
}

type unauthorized struct {
	error
}

func (t unauthorized) Unauthorized() {

}

// Timeout error.
type Timeout interface {
	error
	Timedout() time.Duration
}

// Timedout represents a timeout. the duration is a suggestion
// on how long to wait before attempting again.
func Timedout(cause error, d time.Duration) error {
	return timeout{
		error: cause,
		d:     d,
	}
}

// convert stdlib errors into timeout errors.
func StdlibTimeout(err error, d time.Duration, additional ...error) error {
	var timedout Timeout

	// dont rewrap errors that are already marked as a Timeout
	if errors.Is(err, timedout) {
		return err
	}

	type timeout interface {
		error
		Timeout() bool
	}

	var to timeout
	if errors.Is(err, to) {
		return Timedout(err, d)
	}

	for _, target := range additional {
		if errors.Is(err, target) {
			return Timedout(err, d)
		}
	}

	return err
}

type timeout struct {
	error
	d time.Duration
}

func (t timeout) Timedout() time.Duration {
	return t.d
}

func (t timeout) Timeout() bool {
	return true
}

// Notification presents an error that will be displayed to the user
// to provide notifications.
func Notification(err error) error {
	return notification{
		error: err,
	}
}

type notification struct {
	error
}

func (t notification) Notification() {}
func (t notification) Unwrap() error {
	return t.error
}
func (t notification) Cause() error {
	return t.error
}

// UserFriendly represents an error that will be displayed to users.
func UserFriendly(err error) error {
	return userfriendly{
		error: err,
	}
}

type userfriendly struct {
	error
}

// user friendly error
func (t userfriendly) UserFriendly() {}
func (t userfriendly) Unwrap() error {
	return t.error
}
func (t userfriendly) Cause() error {
	return t.error
}

type AlertableOption func(*Alertable)

func AlertableOptionRate(r float64) AlertableOption {
	return func(a *Alertable) {
		a.rate = r
	}
}

type Alertable struct {
	cause error
	rate  float64
}

func (t Alertable) Alertable() float64 {
	return t.rate
}

func (t Alertable) Unwrap() error {
	return t.cause
}

func (t Alertable) Error() string {
	return t.cause.Error()
}

func (t Alertable) Is(target error) bool {
	type sampler interface {
		Alertable() float64
	}

	_, ok := target.(sampler)
	return ok
}

func (t Alertable) As(target any) bool {
	type sampler interface {
		Alertable() float64
	}

	if x, ok := target.(*sampler); ok {
		*x = t
		return ok
	}

	return false
}

// mark an error as alertable iff it isn't already marked.
func WrapAlert(err error, options ...AlertableOption) error {
	var alertable Alertable
	if errors.Is(err, alertable) {
		return err
	}

	return NewAlert(err, options...)
}

func NewAlert(err error, options ...AlertableOption) error {
	return langx.Clone(Alertable{
		cause: err,
		rate:  0.0,
	}, options...)
}

// Determines if an error should be sampled by checking if the error
// is alertable and has a rate.
func Sample(cause error) error {
	var (
		a Alertable
	)

	if errors.As(cause, &a) && rand.Float64() < a.rate {
		return cause
	}

	return nil
}

type Contextual interface {
	Unwrap() error
	Context() map[string]any
}

type contextual struct {
	cause   error
	details map[string]any
}

func (t *contextual) Add(k string, v any) *contextual {
	t.details[k] = v
	return t
}

func (t contextual) Context() map[string]any {
	return t.details
}

func (t contextual) Unwrap() error {
	return t.cause
}

func (t contextual) Error() string {
	return t.cause.Error()
}

func (t contextual) Is(target error) bool {
	_, ok := target.(Contextual)
	return ok
}

func (t contextual) As(target any) bool {
	if x, ok := target.(*contextual); ok {
		*x = t
		return ok
	}

	return false
}

func NewContext(cause error) *contextual {
	return &contextual{
		cause:   cause,
		details: make(map[string]any),
	}
}

func Context(cause error) map[string]any {
	var c contextual

	if errors.As(cause, &c) {
		return c.details
	}

	return make(map[string]any)
}

type Unrecoverable struct {
	cause error
}

func (t Unrecoverable) Unrecoverable() {}

func (t Unrecoverable) Unwrap() error {
	return t.cause
}

func (t Unrecoverable) Error() string {
	return t.cause.Error()
}

func (t Unrecoverable) Is(target error) bool {
	type unrecoverable interface {
		Unrecoverable()
	}

	_, ok := target.(unrecoverable)
	return ok
}

func (t Unrecoverable) As(target any) bool {
	type unrecoverable interface {
		Unrecoverable()
	}

	if x, ok := target.(*unrecoverable); ok {
		*x = t
		return ok
	}

	return false
}

func NewUnrecoverable(err error) error {
	return Unrecoverable{
		cause: err,
	}
}

func Panic(err error) {
	if err == nil {
		return
	}

	panic(err)
}

// returns nil if the error matches any of the targets
func Ignore(err error, targets ...error) error {
	for _, target := range targets {
		if errors.Is(err, target) {
			return nil
		}
	}

	return err
}

// returns true if the error matches any of the targets.
func Is(err error, targets ...error) bool {
	for _, target := range targets {
		if errors.Is(err, target) {
			return true
		}
	}

	return false
}
