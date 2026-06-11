package flagparse

import (
	"fmt"
	"strconv"
	"strings"
)

type Validator func(string) bool

type Parser struct {
	args            []string
	index           int
	positionalsOnly bool
}

type Arg struct {
	raw        string
	positional bool
}

type LongFlag struct {
	name     string
	value    string
	hasValue bool
}

func New(args []string) *Parser {
	return &Parser{args: args}
}

func (p *Parser) Next() (Arg, bool) {
	for p.index < len(p.args) {
		raw := p.args[p.index]
		p.index++
		if !p.positionalsOnly && raw == "--" {
			p.positionalsOnly = true
			continue
		}
		return Arg{
			raw:        raw,
			positional: p.positionalsOnly || raw == "-" || !strings.HasPrefix(raw, "-"),
		}, true
	}
	return Arg{}, false
}

func (p *Parser) Value(valid Validator, missing error) (string, error) {
	if p.index >= len(p.args) || !valid(p.args[p.index]) {
		return "", missing
	}
	value := p.args[p.index]
	p.index++
	return value, nil
}

func (p *Parser) OptionalPositionalValue() (string, bool) {
	if p.index >= len(p.args) {
		return "", false
	}
	value := p.args[p.index]
	if !p.positionalsOnly && (value == "--" || strings.HasPrefix(value, "-")) {
		return "", false
	}
	p.index++
	return value, true
}

func (a Arg) Raw() string {
	return a.raw
}

func (a Arg) Positional() bool {
	return a.positional
}

func (a Arg) Long() (LongFlag, bool) {
	if a.positional || !strings.HasPrefix(a.raw, "--") || a.raw == "--" {
		return LongFlag{}, false
	}
	name := strings.TrimPrefix(a.raw, "--")
	value, hasValue := "", false
	if before, after, ok := strings.Cut(name, "="); ok {
		name = before
		value = after
		hasValue = true
	}
	return LongFlag{name: name, value: value, hasValue: hasValue}, true
}

func (a Arg) ShortText() (string, bool) {
	if a.positional || !strings.HasPrefix(a.raw, "-") || strings.HasPrefix(a.raw, "--") || a.raw == "-" {
		return "", false
	}
	return strings.TrimPrefix(a.raw, "-"), true
}

func (a Arg) ShortBoolCluster(allowed string, handle func(byte)) bool {
	text, ok := a.ShortText()
	if !ok {
		return false
	}
	for i := 0; i < len(text); i++ {
		if !strings.ContainsRune(allowed, rune(text[i])) {
			return false
		}
	}
	for i := 0; i < len(text); i++ {
		handle(text[i])
	}
	return true
}

func (a Arg) AttachedShortValue(short byte, valid Validator) (string, bool) {
	text, ok := a.ShortText()
	if !ok || len(text) <= 1 || text[0] != short {
		return "", false
	}
	value := text[1:]
	if !valid(value) {
		return "", false
	}
	return value, true
}

func (f LongFlag) Name() string {
	return f.name
}

func (f LongFlag) Value() string {
	return f.value
}

func (f LongFlag) HasValue() bool {
	return f.hasValue
}

func (f LongFlag) Bool(name string) (bool, bool, error) {
	if f.name == name {
		if !f.hasValue {
			return true, true, nil
		}
		value, err := strconv.ParseBool(f.value)
		if err != nil {
			return false, true, fmt.Errorf("invalid boolean value %q for --%s", f.value, name)
		}
		return value, true, nil
	}
	if f.name == "no-"+name {
		if f.hasValue {
			return false, true, fmt.Errorf("--no-%s does not take a value", name)
		}
		return false, true, nil
	}
	return false, false, nil
}

func AcceptAny(string) bool {
	return true
}

func AcceptNonFlag(value string) bool {
	return value != "" && !strings.HasPrefix(value, "-")
}

func AcceptPath(value string) bool {
	return value != "" && (value == "-" || !strings.HasPrefix(value, "-"))
}

func AcceptDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
