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

type taskfile struct {
	Tasks map[string]taskfileEntry `yaml:"tasks"`
}

type taskfileEntry struct {
	Desc string `yaml:"desc"`
}

func tasksCmd(ctx *snap.Context) error {
	taskfilePath, err := resolveTaskfilePath(ctx)
	if err != nil {
		return err
	}

	content, err := os.ReadFile(taskfilePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", taskfilePath, err)
	}

	var tf taskfile
	if err := yaml.Unmarshal(content, &tf); err != nil {
		return fmt.Errorf("parse %s: %w", taskfilePath, err)
	}

	names := make([]string, 0, len(tf.Tasks))
	for name := range tf.Tasks {
		names = append(names, name)
	}
	sort.Strings(names)

	fmt.Fprintf(ctx.Stdout(), "Tasks from %s:\n", taskfilePath)
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

func resolveTaskfilePath(ctx *snap.Context) (string, error) {
	var fileFlag string
	args := make([]string, 0, ctx.NArgs())
	for i := 0; i < ctx.NArgs(); i++ {
		arg := strings.TrimSpace(ctx.Arg(i))
		if arg == "" {
			continue
		}
		args = append(args, arg)
	}

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-f", "--file":
			if i+1 >= len(args) {
				return "", fmt.Errorf("missing value for %s", args[i])
			}
			fileFlag = args[i+1]
			i++
		}
	}

	if fileFlag != "" {
		path, err := expandUserPath(fileFlag)
		if err != nil {
			return "", fmt.Errorf("expand taskfile path: %w", err)
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve taskfile path: %w", err)
		}
		return filepath.Clean(abs), nil
	}

	candidates := []string{"Taskfile.yml", "Taskfile.yaml"}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			abs, err := filepath.Abs(candidate)
			if err != nil {
				return "", err
			}
			return filepath.Clean(abs), nil
		}
	}

	return "", fmt.Errorf("Taskfile.yml not found (use --file to specify path)")
}
