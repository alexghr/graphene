package graphene

import (
	"errors"
	"fmt"
	"os"
)

func (g Git) requireNoGitOperation() error {
	for _, name := range []string{"rebase-merge", "rebase-apply", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "sequencer"} {
		path, err := g.GitPath(name)
		if err != nil {
			return err
		}
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("finish or abort the existing Git operation (%s) first", name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
