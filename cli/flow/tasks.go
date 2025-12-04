package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dzonerzy/go-snap/snap"
	"gopkg.in/yaml.v3"
)

type taskFile struct {
	Tasks map[string]taskEntry `yaml:"tasks"`
}

type taskEntry struct {
	Desc string `yaml:"desc"`
}

func runTasks(ctx *snap.Context) error {
	taskfilePath, err := resolveTaskfilePathFromArgs(ctx)
	if err != nil {
		return err
	}

	content, err := os.ReadFile(taskfilePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", taskfilePath, err)
	}

	var tf taskFile
	if err := yaml.Unmarshal(content, &tf); err != nil {
		return fmt.Errorf("parse %s: %w", taskfilePath, err)
	}

	names := make([]string, 0, len(tf.Tasks))
	for name := range tf.Tasks {
		names = append(names, name)
	}
	sort.Strings(names)

	fmt.Fprintf(ctx.Stdout(), "Tasks in %s:\n", taskfilePath)
	if len(names) == 0 {
		fmt.Fprintln(ctx.Stdout(), "  (none)")
		return nil
	}

	for _, name := range names {
		desc := strings.TrimSpace(tf.Tasks[name].Desc)
		if desc == "" {
			desc = "(no description)"
		}
		fmt.Fprintf(ctx.Stdout(), "  %s: %s\n", name, desc)
	}

	return nil
}

func resolveTaskfilePathFromArgs(ctx *snap.Context) (string, error) {
	var fileFlag string
	for i := 0; i < ctx.NArgs(); i++ {
		arg := strings.TrimSpace(ctx.Arg(i))
		if arg == "" {
			continue
		}
		switch arg {
		case "-f", "--file":
			if i+1 >= ctx.NArgs() {
				return "", fmt.Errorf("missing value for %s", arg)
			}
			fileFlag = ctx.Arg(i + 1)
			i++
		}
	}

	if fileFlag != "" {
		path, err := expandUserTaskPath(fileFlag)
		if err != nil {
			return "", err
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve taskfile path: %w", err)
		}
		return filepath.Clean(abs), nil
	}

	candidates := []string{taskfilePath, "Taskfile.yaml"}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			abs, err := filepath.Abs(candidate)
			if err != nil {
				return "", fmt.Errorf("resolve taskfile path: %w", err)
			}
			return filepath.Clean(abs), nil
		}
	}

	return "", fmt.Errorf("Taskfile.yml not found (use --file to specify path)")
}

func expandUserTaskPath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("taskfile path cannot be empty")
	}
	if trimmed[0] != '~' {
		return trimmed, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}

	if len(trimmed) == 1 {
		return home, nil
	}

	switch trimmed[1] {
	case '/', '\\':
		return filepath.Join(home, trimmed[2:]), nil
	default:
		return "", fmt.Errorf("unsupported ~ expansion in %q", trimmed)
	}
}
