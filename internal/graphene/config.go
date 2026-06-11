package graphene

import (
	"fmt"
	"strings"
)

const defaultBranchPrefix = "stack"

type Config struct {
	BranchPrefix string
}

func (a *App) loadConfig() (Config, error) {
	cfg := Config{BranchPrefix: defaultBranchPrefix}
	prefix, err := a.git.Output("config", "--get", "graphene.branchPrefix")
	if err == nil {
		cfg.BranchPrefix = prefix
		return cfg, nil
	}
	if isGitExit(err, 1) {
		return cfg, nil
	}
	return Config{}, err
}

func (a *App) config(args []string) error {
	opts, err := parseConfigArgs(args)
	if err != nil {
		return err
	}

	gitArgs := []string{"config"}
	if opts.scope != "" {
		gitArgs = append(gitArgs, "--"+opts.scope)
	}

	switch opts.action {
	case "get":
		key, err := grapheneConfigKey(opts.key)
		if err != nil {
			return err
		}
		out, err := a.git.Output(append(gitArgs, "--get", key)...)
		if err != nil {
			if isGitExit(err, 1) {
				return fmt.Errorf("config key %q is not set", opts.key)
			}
			return err
		}
		fmt.Fprintln(a.stdout, out)
		return nil
	case "set":
		key, err := grapheneConfigKey(opts.key)
		if err != nil {
			return err
		}
		return a.git.OutputErr(append(gitArgs, key, opts.value)...)
	case "unset":
		key, err := grapheneConfigKey(opts.key)
		if err != nil {
			return err
		}
		err = a.git.OutputErr(append(gitArgs, "--unset", key)...)
		if err != nil && isGitExit(err, 5) {
			return fmt.Errorf("config key %q is not set", opts.key)
		}
		return err
	default:
		return fmt.Errorf("usage: graphene config <get|set|unset> [--global|--local] <key> [value]")
	}
}

type configOptions struct {
	action string
	scope  string
	key    string
	value  string
}

func parseConfigArgs(args []string) (configOptions, error) {
	if len(args) == 0 {
		return configOptions{}, fmt.Errorf("usage: graphene config <get|set|unset> [--global|--local] <key> [value]")
	}
	opts := configOptions{action: args[0]}
	rest := args[1:]

	if opts.action != "get" && opts.action != "set" && opts.action != "unset" {
		return configOptions{}, fmt.Errorf("usage: graphene config <get|set|unset> [--global|--local] <key> [value]")
	}

	for len(rest) > 0 {
		if rest[0] == "--" {
			rest = rest[1:]
			break
		}
		switch rest[0] {
		case "--global":
			if opts.scope != "" {
				return configOptions{}, fmt.Errorf("graphene config accepts one scope")
			}
			opts.scope = "global"
			rest = rest[1:]
		case "--local":
			if opts.scope != "" {
				return configOptions{}, fmt.Errorf("graphene config accepts one scope")
			}
			opts.scope = "local"
			rest = rest[1:]
		default:
			goto positional
		}
	}

positional:
	switch opts.action {
	case "get", "unset":
		if len(rest) != 1 {
			return configOptions{}, fmt.Errorf("usage: graphene config %s [--global|--local] <key>", opts.action)
		}
		opts.key = rest[0]
	case "set":
		if len(rest) < 2 {
			return configOptions{}, fmt.Errorf("usage: graphene config set [--global|--local] <key> <value>")
		}
		opts.key = rest[0]
		opts.value = strings.Join(rest[1:], " ")
	}
	return opts, nil
}

func grapheneConfigKey(key string) (string, error) {
	if key == "" || strings.HasPrefix(key, "-") {
		return "", fmt.Errorf("invalid config key %q", key)
	}
	if strings.HasPrefix(key, "graphene.") {
		key = strings.TrimPrefix(key, "graphene.")
	}
	if key == "state" {
		return "", fmt.Errorf("graphene.state is managed internally")
	}
	for _, part := range strings.Split(key, ".") {
		if part == "" {
			return "", fmt.Errorf("invalid config key %q", key)
		}
		for _, r := range part {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return "", fmt.Errorf("invalid config key %q", key)
		}
	}
	return "graphene." + key, nil
}
