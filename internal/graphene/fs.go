package graphene

import "os"

func existsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
