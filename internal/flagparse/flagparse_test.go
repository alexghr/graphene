package flagparse_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/alexghr/graphene/internal/flagparse"
)

func TestParserInterspersedFlagsAndTerminator(t *testing.T) {
	t.Parallel()
	parser := flagparse.New([]string{"branch", "--stack", "--", "--literal", "-x"})
	var got []struct {
		raw        string
		positional bool
	}
	for arg, ok := parser.Next(); ok; arg, ok = parser.Next() {
		got = append(got, struct {
			raw        string
			positional bool
		}{
			raw:        arg.Raw(),
			positional: arg.Positional(),
		})
	}

	want := []struct {
		raw        string
		positional bool
	}{
		{raw: "branch", positional: true},
		{raw: "--stack", positional: false},
		{raw: "--literal", positional: true},
		{raw: "-x", positional: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parser args = %#v, want %#v", got, want)
	}
}

func TestParserValueValidation(t *testing.T) {
	t.Parallel()
	parser := flagparse.New([]string{"value", "-flag"})
	got, err := parser.Value(flagparse.AcceptNonFlag, errors.New("missing value"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "value" {
		t.Fatalf("value = %q, want value", got)
	}
	if _, err := parser.Value(flagparse.AcceptNonFlag, errors.New("missing value")); err == nil {
		t.Fatal("parser accepted missing value")
	}
}

func TestParserOptionalPositionalValue(t *testing.T) {
	t.Parallel()
	parser := flagparse.New([]string{"up", "2", "-x"})
	if _, ok := parser.Next(); !ok {
		t.Fatal("missing first arg")
	}
	got, ok := parser.OptionalPositionalValue()
	if !ok || got != "2" {
		t.Fatalf("OptionalPositionalValue = %q, %v, want 2, true", got, ok)
	}
	if got, ok := parser.OptionalPositionalValue(); ok {
		t.Fatalf("OptionalPositionalValue consumed flag-like arg %q", got)
	}
}

func TestLongFlagAndBoolValue(t *testing.T) {
	t.Parallel()
	parser := flagparse.New([]string{"--stack=false", "--no-stack"})
	arg, ok := parser.Next()
	if !ok {
		t.Fatal("missing first arg")
	}
	flag, ok := arg.Long()
	if !ok || flag.Name() != "stack" || flag.Value() != "false" || !flag.HasValue() {
		t.Fatalf("Long = %#v, %v", flag, ok)
	}
	value, matched, err := flag.Bool("stack")
	if err != nil {
		t.Fatal(err)
	}
	if !matched || value {
		t.Fatalf("Bool = %v, %v, want false, true", value, matched)
	}

	arg, ok = parser.Next()
	if !ok {
		t.Fatal("missing second arg")
	}
	flag, ok = arg.Long()
	if !ok {
		t.Fatal("Long did not match --no-stack")
	}
	value, matched, err = flag.Bool("stack")
	if err != nil {
		t.Fatal(err)
	}
	if !matched || value {
		t.Fatalf("Bool --no-stack = %v, %v, want false, true", value, matched)
	}
}

func TestShortFlags(t *testing.T) {
	t.Parallel()
	var seen []byte
	parser := flagparse.New([]string{"-sn", "-c3"})
	arg, ok := parser.Next()
	if !ok {
		t.Fatal("missing first arg")
	}
	if !arg.ShortBoolCluster("sn", func(flag byte) { seen = append(seen, flag) }) {
		t.Fatal("ShortBoolCluster did not match -sn")
	}
	if !reflect.DeepEqual(seen, []byte{'s', 'n'}) {
		t.Fatalf("cluster flags = %#v", seen)
	}

	arg, ok = parser.Next()
	if !ok {
		t.Fatal("missing second arg")
	}
	value, ok := arg.AttachedShortValue('c', flagparse.AcceptDigits)
	if !ok || value != "3" {
		t.Fatalf("AttachedShortValue = %q, %v, want 3, true", value, ok)
	}
}
