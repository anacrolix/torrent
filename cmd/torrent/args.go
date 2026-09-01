package main

import (
	"encoding"
	"fmt"
	"strconv"
	"strings"

	"github.com/anacrolix/bargle/v2"
	"github.com/anacrolix/tagflag"
)

// Work to do once the entire command line has parsed successfully.
type action func() error

// A subcommand keyword, and the parser for everything that follows it.
type subcommand struct {
	name string
	desc string
	// Parses the rest of the arguments for the subcommand, returning the work to do afterwards.
	parse func(p *bargle.Parser) action
}

// Parses whichever of the given subcommands comes next, failing the parser if none of them match.
func parseSubcommand(p *bargle.Parser, subs ...subcommand) action {
	for _, sub := range subs {
		if p.Parse(desc(sub.desc, bargle.Keyword(sub.name))) {
			return sub.parse(p)
		}
	}
	if p.Ok() {
		p.Fail()
	}
	return nil
}

// Adds a description to an argument for the help output. Unlike bargle.WithDesc it doesn't hide
// the wrapped argument's metavar.
func desc(s string, arg bargle.Arg) bargle.Arg {
	described := describedArg{arg, s}
	if metavar, ok := arg.(bargle.Metavar); ok {
		return describedMetavarArg{described, metavar}
	}
	return described
}

type describedArg struct {
	bargle.Arg
	desc string
}

func (me describedArg) ArgDesc() string {
	return me.desc
}

type describedMetavarArg struct {
	describedArg
	metavar bargle.Metavar
}

func (me describedMetavarArg) Metavar() string {
	return me.metavar.Metavar()
}

// A long option for a type the bargle builtin unmarshaler handles.
func builtinLong[T bargle.BuiltinUnmarshalerType](name string, target *T) bargle.Arg {
	return bargle.Long(name, bargle.BuiltinUnmarshaler(target))
}

// A long option that unmarshals into a value implementing encoding.TextUnmarshaler.
func textLong(name string, target encoding.TextUnmarshaler) bargle.Arg {
	return bargle.Long(name, bargle.TextUnmarshaler(target))
}

// A repeatable long option, appending each occurrence to a slice.
func sliceLong[T any](name string, target *[]T, uc func(*T) bargle.Unmarshaler) bargle.Arg {
	return bargle.Long(name, bargle.AppendSlice(target, uc))
}

// A boolean switch. As in bargle v1, it can be negated with a "no-" prefix, and takes an optional
// explicit value ("--flag=false").
func boolFlag(name string, target *bool) bargle.Arg {
	return flag{name: name, set: func(value bool) {
		*target = value
	}, get: func() any {
		return *target
	}}
}

// A boolean switch that allocates its target when given, so that it's distinguishable from not
// being given at all.
func boolPtrFlag(name string, target **bool) bargle.Arg {
	return flag{name: name, set: func(value bool) {
		*target = &value
	}, get: func() any {
		if *target == nil {
			return nil
		}
		return **target
	}}
}

type flag struct {
	name string
	set  func(value bool)
	get  func() any
}

var _ interface {
	bargle.Arg
	bargle.ArgValuer
} = flag{}

func (me flag) ArgInfo() bargle.ArgInfo {
	return bargle.ArgInfo{
		MatchingForms: []string{fmt.Sprintf("--[no-]%[1]s, --[no-]%[1]s=bool", me.name)},
		ArgType:       bargle.ArgTypeSwitch,
	}
}

func (me flag) Value() any {
	return me.get()
}

func (me flag) Parse(ctx bargle.ParseContext) bool {
	arg, ok := ctx.Pop()
	if !ok {
		return false
	}
	key, ok := strings.CutPrefix(arg, "--")
	if !ok {
		return false
	}
	key, value, haveValue := strings.Cut(key, "=")
	var negate bool
	switch key {
	case me.name:
	case "no-" + me.name:
		negate = true
	default:
		return false
	}
	u := flagUnmarshaler{set: me.set, negate: negate}
	if haveValue {
		return ctx.UnmarshalArg(u, value)
	}
	return ctx.Unmarshal(u)
}

type flagUnmarshaler struct {
	set    func(value bool)
	negate bool
}

func (me flagUnmarshaler) ArgTypes() []string {
	return []string{"?bool"}
}

func (me flagUnmarshaler) Unmarshal(ctx bargle.UnmarshalContext) error {
	value := true
	if ctx.HaveExplicitValue() {
		arg, err := ctx.Pop()
		if err != nil {
			return err
		}
		value, err = strconv.ParseBool(arg)
		if err != nil {
			return err
		}
	}
	me.set(value != me.negate)
	return nil
}

// A positional argument that records whether it matched, so that required arguments can be
// checked once parsing is done.
func positional(metavar string, matched *bool, u bargle.Unmarshaler) bargle.Arg {
	return bargle.Positional(metavar, markUnmarshaled(matched, u))
}

// A positional argument that matches repeatedly, appending each value to a slice.
func positionals[T any](metavar string, target *[]T, uc func(*T) bargle.Unmarshaler) bargle.Arg {
	return repeatedPositional{metavar, bargle.AppendSlice(target, uc)}
}

type repeatedPositional struct {
	metavar string
	u       bargle.Unmarshaler
}

var _ interface {
	bargle.Arg
	bargle.Metavar
} = repeatedPositional{}

func (me repeatedPositional) Metavar() string {
	return me.metavar
}

func (me repeatedPositional) ArgInfo() bargle.ArgInfo {
	return bargle.ArgInfo{
		ArgType:       bargle.ArgTypePos,
		MatchingForms: me.u.ArgTypes(),
	}
}

func (me repeatedPositional) Parse(ctx bargle.ParseContext) bool {
	if ctx.NumArgs() < 1 {
		return false
	}
	// Don't consume what looks like a switch, unless we've been told there are no more of them.
	if !ctx.PositionalOnly() && strings.HasPrefix(ctx.PeekArgs()[0], "-") {
		return false
	}
	return ctx.Unmarshal(me.u)
}

// Fails the parser if a required argument wasn't given. Does nothing if the parser is already
// unhappy, or help was requested. Arguments we couldn't handle are reported first, since they're
// the more likely reason we're missing something.
func requireArg(p *bargle.Parser, name string, given bool) {
	if !p.Ok() || given {
		return
	}
	p.FailIfArgsRemain()
	if p.Ok() {
		p.SetError(fmt.Errorf("%q required and not given", name))
	}
}

// Unmarshals into a newly allocated value, assigning it to the target on success. This keeps
// optional values distinguishable from their zero value.
func pointerUnmarshaler[T any](target **T, uc func(*T) bargle.Unmarshaler) bargle.Unmarshaler {
	var value T
	u := uc(&value)
	return unmarshalerFunc{
		f: func(ctx bargle.UnmarshalContext) error {
			err := u.Unmarshal(ctx)
			if err == nil {
				*target = &value
			}
			return err
		},
		argTypes: u.ArgTypes(),
	}
}

// Sets a flag when the wrapped unmarshaler succeeds.
func markUnmarshaled(matched *bool, u bargle.Unmarshaler) bargle.Unmarshaler {
	return unmarshalerFunc{
		f: func(ctx bargle.UnmarshalContext) error {
			err := u.Unmarshal(ctx)
			if err == nil {
				*matched = true
			}
			return err
		},
		argTypes: u.ArgTypes(),
	}
}

type unmarshalerFunc struct {
	f        func(ctx bargle.UnmarshalContext) error
	argTypes []string
}

func (me unmarshalerFunc) Unmarshal(ctx bargle.UnmarshalContext) error {
	return me.f(ctx)
}

func (me unmarshalerFunc) ArgTypes() []string {
	return me.argTypes
}

// Overrides the argument types an unmarshaler reports for the help output.
func withArgTypes(u bargle.Unmarshaler, argTypes ...string) bargle.Unmarshaler {
	return unmarshalerFunc{f: u.Unmarshal, argTypes: argTypes}
}

func bytesUnmarshaler(target *tagflag.Bytes) bargle.Unmarshaler {
	return withArgTypes(bargle.TextUnmarshaler(target), "bytes")
}

func uint16Unmarshaler(target *uint16) bargle.Unmarshaler {
	return bargle.UnaryUnmarshalFunc(target, func(s string) (_ uint16, err error) {
		u, err := strconv.ParseUint(s, 0, 16)
		return uint16(u), err
	})
}
