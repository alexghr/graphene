package graphene

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// Values are true only when every change at the path is a gitlink change.
type checkoutPaths map[string]bool

func (paths checkoutPaths) add(mode, path string) {
	if mode == "000000" {
		return
	}
	gitlink, seen := paths[path]
	paths[path] = mode == "160000" && (!seen || gitlink)
}

func collectCheckoutPaths(paths checkoutPaths, out []byte) error {
	records := strings.Split(string(out), "\x00")
	for i := 0; i < len(records); i++ {
		header := strings.TrimSpace(records[i])
		if header == "" {
			continue
		}
		fields := strings.Fields(header)
		if len(fields) != 5 || !strings.HasPrefix(fields[0], ":") || i+1 >= len(records) {
			return fmt.Errorf("cannot read checkout path record %q", header)
		}
		i++
		paths.add(strings.TrimPrefix(fields[0], ":"), records[i])
		paths.add(fields[1], records[i])
	}
	return nil
}

func (g Git) changedCheckoutPaths(trees ...string) (checkoutPaths, error) {
	paths := checkoutPaths{}
	seen := map[string]bool{}
	for _, tree := range trees {
		if tree == "" || seen[tree] {
			continue
		}
		seen[tree] = true
		out, err := g.OutputBytes("diff", "--raw", "--no-abbrev", "--no-renames", "--no-ext-diff", "--no-relative", "--no-color", "--ignore-submodules=dirty", "-z", tree, "--", ":/")
		if err != nil {
			return nil, err
		}
		if err := collectCheckoutPaths(paths, out); err != nil {
			return nil, err
		}
	}
	return paths, nil
}

func (g Git) replayCheckoutPaths(paths checkoutPaths, upstream, top string) error {
	// Include temporary additions that disappear before the branch tip.
	out, err := g.OutputBytes("log", "--format=", "--raw", "--no-abbrev", "--no-renames", "--no-merges", "--no-ext-diff", "--no-relative", "--no-color", "--no-show-signature", "--root", "-z", upstream+".."+top, "--")
	if err != nil {
		return err
	}
	return collectCheckoutPaths(paths, out)
}

func (g Git) checkoutPathRisks(paths checkoutPaths, savedTree string) ([]nestedRepositoryRisk, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	root, err := g.Output("rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	g.Dir = root
	index, err := g.OutputBytes("ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	tracked := map[string]bool{}
	for entry := range strings.SplitSeq(string(index), "\x00") {
		_, path, ok := strings.Cut(entry, "\t")
		if ok {
			tracked[path] = true
		}
	}
	if savedTree != "" {
		saved, err := g.OutputBytes("ls-tree", "-r", "-z", savedTree)
		if err != nil {
			return nil, err
		}
		index = append(index, saved...)
	}
	gitlinks := map[string]bool{}
	for entry := range strings.SplitSeq(string(index), "\x00") {
		header, path, ok := strings.Cut(entry, "\t")
		if ok && strings.HasPrefix(header, "160000 ") {
			gitlinks[path] = true
		}
	}
	var untracked []string
	ordered := slices.Sorted(maps.Keys(paths))
	for _, ignored := range []bool{false, true} {
		// Limit Git's search to affected paths and collapse untracked directories.
		args := []string{"ls-files", "--others", "--exclude-standard", "--directory", "-z"}
		if ignored {
			args = append(args, "--ignored")
		}
		args = append(args, "--")
		for _, path := range ordered {
			args = append(args, ":(top,literal)"+path)
		}
		out, err := g.OutputBytes(args...)
		if err != nil {
			return nil, err
		}
		for path := range strings.SplitSeq(string(out), "\x00") {
			if path != "" {
				untracked = append(untracked, path)
			}
		}
	}
	risks := map[nestedRepositoryRisk]bool{}
	markers := map[string]bool{}
	checked := map[string]bool{}
	obstructions := map[string]bool{}
	for _, path := range ordered {
		if paths[path] && gitlinks[path] {
			continue
		}
		nested := false
		for parent := path; parent != "." && parent != ""; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if !checked[parent] {
				checked[parent] = true
				_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(parent), ".git"))
				if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
					return nil, err
				}
				markers[parent] = err == nil
				info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(parent)))
				if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
					return nil, err
				}
				obstructions[parent] = err == nil && !info.IsDir() && !tracked[parent]
			}
			if markers[parent] {
				risks[nestedRepositoryRisk{repository: parent, path: path}] = true
				nested = true
			}
			if parent != path && obstructions[parent] {
				risks[nestedRepositoryRisk{path: path}] = true
			}
		}
		if nested {
			continue
		}
		for _, local := range untracked {
			plain := strings.TrimSuffix(local, "/")
			if !pathsOverlap(path, plain) {
				continue
			}
			if strings.HasSuffix(local, "/") && path == plain {
				entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(path)))
				if errors.Is(err, os.ErrNotExist) || (err == nil && len(entries) == 0) {
					continue
				}
				if err != nil {
					return nil, err
				}
			}
			if strings.HasSuffix(local, "/") && strings.HasPrefix(path, local) {
				// A new file under an untracked directory is safe if absent.
				_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(path)))
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil && !errors.Is(err, syscall.ENOTDIR) {
					return nil, err
				}
			}
			risks[nestedRepositoryRisk{path: path}] = true
		}
	}
	return sortedRepositoryRisks(risks), nil
}

func (g Git) checkCheckoutPaths(savedTree string, trees ...string) error {
	paths, err := g.changedCheckoutPaths(trees...)
	if err != nil {
		return err
	}
	risks, err := g.checkoutPathRisks(paths, savedTree)
	if err != nil {
		return err
	}
	if len(risks) > 0 {
		if risk := risks[0]; risk.repository != "" {
			return nestedRepositoryCollision(risk.repository, risk.path)
		}
		return fmt.Errorf("untracked or ignored path %q would be overwritten; move it aside and retry", risks[0].path)
	}
	return nil
}
