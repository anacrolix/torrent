// Package envx provides utility functions for extracting information from environment variables
package envx

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/james-lawrence/torrent/internal/errorsx"
	"github.com/james-lawrence/torrent/internal/slicesx"
)

// Int retrieve a integer flag from the environment, checks each key in order
// first to parse successfully is returned.
func Int[T int | int64](fallback T, keys ...string) T {
	return envval(fallback, func(s string) (T, error) {
		decoded, err := strconv.ParseInt(s, 10, 64)
		return T(decoded), errorsx.Wrapf(err, "integer '%s' is invalid", s)
	}, keys...)
}

// Boolean retrieve a boolean flag from the environment, checks each key in order
// first to parse successfully is returned.
func Boolean(fallback bool, keys ...string) bool {
	return envval(fallback, func(s string) (bool, error) {
		decoded, err := strconv.ParseBool(s)
		return decoded, errorsx.Wrapf(err, "boolean '%s' is invalid", s)
	}, keys...)
}

// Float64 retrieve a float64 flag from the environment, checks each key in order
// first to parse successfully is returned.
func Float64(fallback float64, keys ...string) float64 {
	return envval(fallback, func(s string) (float64, error) {
		decoded, err := strconv.ParseFloat(s, 64)
		return decoded, errorsx.Wrapf(err, "float64 '%s' is invalid", s)
	}, keys...)
}

// String retrieve a string value from the environment, checks each key in order
// first string found is returned.
func String(fallback string, keys ...string) string {
	return envval(fallback, func(s string) (string, error) {
		// we'll never receive an empty string because envval skips empty strings.
		return s, nil
	}, keys...)
}

// Strings retrieve a string array seperated by , value from the environment, checks each key in order
// first string found is returned.
func Strings(fallback []string, keys ...string) []string {
	return envval(fallback, func(s string) ([]string, error) {
		// we'll never receive an empty string because envval skips empty strings.
		return strings.Split(s, ","), nil
	}, keys...)
}

// Duration retrieves a time.Duration from the environment, checks each key in order
// first successful parse to a duration is returned.
func Duration(fallback time.Duration, keys ...string) time.Duration {
	return envval(fallback, func(s string) (time.Duration, error) {
		decoded, err := time.ParseDuration(s)
		return decoded, errorsx.Wrapf(err, "time.Duration '%s' is invalid", s)
	}, keys...)
}

// BytesFile treats the value in the provided environment keys as a file path.
func BytesFile(fallback []byte, keys ...string) []byte {
	return envval(fallback, func(s string) ([]byte, error) {
		decoded, err := os.ReadFile(s)
		return decoded, errorsx.Wrapf(err, "file path '%s' was inaccessible", s)
	}, keys...)
}

// BytesHex read value as a hex encoded string.
func BytesHex(fallback []byte, keys ...string) []byte {
	return envval(fallback, func(s string) ([]byte, error) {
		decoded, err := hex.DecodeString(s)
		return decoded, errorsx.Wrapf(err, "invalid hex encoded data '%s'", s)
	}, keys...)
}

// BytesB64 read value as a base64 encoded string
func BytesB64(fallback []byte, keys ...string) []byte {
	enc := base64.RawStdEncoding.WithPadding('=')
	return envval(fallback, func(s string) ([]byte, error) {
		decoded, err := enc.DecodeString(s)
		return decoded, errorsx.Wrapf(err, "invalid base64 encoded data '%s'", s)
	}, keys...)
}

func URL(fallback string, keys ...string) *url.URL {
	var (
		err    error
		parsed *url.URL
	)

	if parsed, err = url.Parse(fallback); err != nil {
		panic(errorsx.Wrap(err, "must provide a valid fallback url"))
	}

	return envval(parsed, func(s string) (*url.URL, error) {
		decoded, err := url.Parse(s)
		return decoded, errorsx.WithStack(err)
	}, keys...)
}

func envval[T any](fallback T, parse func(string) (T, error), keys ...string) T {
	for _, k := range keys {
		s := strings.TrimSpace(os.Getenv(k))
		if s == "" {
			continue
		}

		decoded, err := parse(s)
		if err != nil {
			log.Printf("%s stored an invalid value %v\n", k, err)
			continue
		}

		return decoded
	}

	return fallback
}

func Build() *Builder {
	return &Builder{}
}

type Builder struct {
	environ []string
	failed  error
}

func (t Builder) CopyTo(w io.Writer) error {
	if t.failed != nil {
		return t.failed
	}

	for _, e := range t.environ {
		if _, err := fmt.Fprintf(w, "%s\n", e); err != nil {
			return errorsx.Wrapf(err, "unable to write environment variable: %s", e)
		}
	}

	return nil
}

func (t Builder) Environ() ([]string, error) {
	return t.environ, t.failed
}

// set a single variable to a value.
func (t *Builder) Var(k, v string) *Builder {
	if encoded := Format(k, v); encoded != "" {
		t.environ = append(t.environ, fmt.Sprintf("%s=%s", k, v))
	}
	return t
}

// extract environment variables from a file on disk.
// missing files are treated as noops.
func (t *Builder) FromPath(n string) *Builder {
	tmp, err := FromPath(n)
	t.environ = append(t.environ, tmp...)
	t.failed = errors.Join(t.failed, err)
	return t
}

// extract environment variables from an io.Reader.
// the format is the standard .env file formats.
func (t *Builder) FromReader(r io.Reader) *Builder {
	tmp, err := FromReader(r)
	t.environ = append(t.environ, tmp...)
	t.failed = errors.Join(t.failed, err)
	return t
}

func (t *Builder) FromEnviron(environ ...string) *Builder {
	t.environ = append(t.environ, environ...)
	return t
}

// extract the key/value pairs from the os.Environ.
// empty keys are passed as k=
func (t *Builder) FromEnv(keys ...string) *Builder {
	vars := make([]string, 0, len(keys))

	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			vars = append(vars, Format(k, v, FormatOptionTransforms(allowAll)))
		}
	}

	t.environ = append(t.environ, vars...)

	return t
}

type formatoption func(*formatopts)
type formatopts struct {
	transformer func(string) string // transforms that result in empty strings are ignored.
}

func allowAll(s string) string {
	return s
}

func ignoreEmptyVariables(s string) string {
	if strings.HasSuffix(s, "=") {
		return ""
	}

	return s
}

func FormatOptionTransforms(transforms ...func(string) string) formatoption {
	combined := func(s string) string {
		for _, trans := range transforms {
			s = trans(s)
		}

		return s
	}

	return func(f *formatopts) {
		f.transformer = combined
	}
}

// set many keys to the same value.
func Vars(v string, keys ...string) (environ []string) {
	environ = make([]string, 0, len(keys))
	for _, k := range keys {
		environ = append(environ, Format(k, v))
	}

	return environ
}

// format a environment variable in k=v.
//
// - doesn't currently escape values. it may in the future.
//
// - if the key or value are an empty string it'll return an empty string. it will log if debugging is enabled.
func Format(k, v string, options ...func(*formatopts)) string {
	opts := slicesx.Reduce(&formatopts{
		transformer: ignoreEmptyVariables,
	}, options...)
	evar := strings.TrimSpace(opts.transformer(fmt.Sprintf("%s=%s", k, v)))
	if evar == "" {
		log.Println("ignoring variable", k, "empty")
	}

	return fmt.Sprintf("%s=%s", k, v)
}

// returns the os.Environ or an empty slice if b is false.
func Dirty(b bool) []string {
	if b {
		return os.Environ()
	}

	return nil
}

func Debug(envs ...string) {
	errorsx.Log(log.Output(2, fmt.Sprintln("DEBUG ENVIRONMENT INITIATED")))
	defer func() { errorsx.Log(log.Output(3, "DEBUG ENVIRONMENT COMPLETED")) }()
	for _, e := range envs {
		errorsx.Log(log.Output(2, fmt.Sprintln(e)))
	}
}

func FromReader(r io.Reader) (environ []string, err error) {
	scanner := bufio.NewScanner(r)

	for scanner.Scan() {
		environ = append(environ, scanner.Text())
	}

	return environ, nil
}

func FromPath(n string) (environ []string, err error) {
	env, err := os.Open(n)
	if os.IsNotExist(err) {
		return environ, nil
	}
	if err != nil {
		return nil, err
	}
	defer env.Close()

	return FromReader(env)
}
