package graphene

import "fmt"

var Version = "dev"

func (a *App) version(args []string, gitVersion gitVersion) error {
	if len(args) != 0 {
		return fmt.Errorf("graphene version does not accept arguments")
	}
	_, err := fmt.Fprintf(a.stdout, "graphene %s\ngit %s\n", Version, gitVersion)
	return err
}
