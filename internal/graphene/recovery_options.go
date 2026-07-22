package graphene

import (
	"fmt"

	"github.com/alexghr/graphene/internal/flagparse"
)

type continueOptions struct {
	acceptCurrent bool
}

type abortOptions struct {
	force bool
}

func parseContinueArgs(args []string) (continueOptions, error) {
	var opts continueOptions
	cursor := flagparse.New(args)
	for arg, ok := cursor.Next(); ok; arg, ok = cursor.Next() {
		if flag, ok := arg.Long(); ok {
			if value, matched, err := flag.Bool("accept-current"); matched {
				if err != nil {
					return opts, err
				}
				opts.acceptCurrent = value
				continue
			}
		}
		return opts, fmt.Errorf("unsupported argument %q; usage: graphene continue [--accept-current]", arg.Raw())
	}
	return opts, nil
}

func parseAbortArgs(args []string) (abortOptions, error) {
	var opts abortOptions
	cursor := flagparse.New(args)
	for arg, ok := cursor.Next(); ok; arg, ok = cursor.Next() {
		if flag, ok := arg.Long(); ok {
			if value, matched, err := flag.Bool("force"); matched {
				if err != nil {
					return opts, err
				}
				opts.force = value
				continue
			}
		}
		if arg.ShortBoolCluster("f", func(byte) { opts.force = true }) {
			continue
		}
		return opts, fmt.Errorf("unsupported argument %q; usage: graphene abort [-f|--force]", arg.Raw())
	}
	return opts, nil
}
