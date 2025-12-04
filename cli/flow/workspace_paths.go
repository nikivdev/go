package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dzonerzy/go-snap/snap"
	"github.com/ktr0731/go-fuzzyfinder"
)

type workspaceList string

const (
	workspaceListRepoPaths  workspaceList = "repoPaths"
	workspaceListExpanded   workspaceList = "workingExpandedFolders"
	workspaceListSelection  workspaceList = "workingStoredSelection.selectedPaths"
	workspaceListFileBuffer workspaceList = "workingFilePaths"
)

var workspaceListAliases = map[string]workspaceList{
	"repo":          workspaceListRepoPaths,
	"repos":         workspaceListRepoPaths,
	"repopaths":     workspaceListRepoPaths,
	"paths":         workspaceListRepoPaths,
	"expanded":      workspaceListExpanded,
	"expandedpaths": workspaceListExpanded,
	"folders":       workspaceListExpanded,
	"selection":     workspaceListSelection,
	"selected":      workspaceListSelection,
	"files":         workspaceListFileBuffer,
	"open":          workspaceListFileBuffer,
	"workingfiles":  workspaceListFileBuffer,
}

var workspaceListLabels = map[workspaceList]string{
	workspaceListRepoPaths:  "repoPaths",
	workspaceListExpanded:   "workingExpandedFolders",
	workspaceListSelection:  "workingStoredSelection.selectedPaths",
	workspaceListFileBuffer: "workingFilePaths",
}

type workspaceSelection struct {
	SelectedPaths []string `json:"selectedPaths"`
	Extra         map[string]json.RawMessage
}

func (s *workspaceSelection) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	s.Extra = make(map[string]json.RawMessage)
	for key, value := range raw {
		switch key {
		case "selectedPaths":
			if err := json.Unmarshal(value, &s.SelectedPaths); err != nil {
				return err
			}
		default:
			s.Extra[key] = value
		}
	}
	return nil
}

func (s workspaceSelection) MarshalJSON() ([]byte, error) {
	raw := make(map[string]json.RawMessage, len(s.Extra)+1)
	for key, value := range s.Extra {
		raw[key] = value
	}

	selected, err := json.Marshal(s.SelectedPaths)
	if err != nil {
		return nil, err
	}
	raw["selectedPaths"] = selected
	return json.Marshal(raw)
}

type workspaceDocument struct {
	RepoPaths              []string           `json:"repoPaths"`
	WorkingExpandedFolders []string           `json:"workingExpandedFolders"`
	WorkingFilePaths       []string           `json:"workingFilePaths"`
	WorkingStoredSelection workspaceSelection `json:"workingStoredSelection"`
	Extra                  map[string]json.RawMessage
}

func (w *workspaceDocument) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	w.Extra = make(map[string]json.RawMessage)
	for key, value := range raw {
		switch key {
		case "repoPaths":
			if err := json.Unmarshal(value, &w.RepoPaths); err != nil {
				return err
			}
		case "workingExpandedFolders":
			if err := json.Unmarshal(value, &w.WorkingExpandedFolders); err != nil {
				return err
			}
		case "workingFilePaths":
			if err := json.Unmarshal(value, &w.WorkingFilePaths); err != nil {
				return err
			}
		case "workingStoredSelection":
			if err := json.Unmarshal(value, &w.WorkingStoredSelection); err != nil {
				return err
			}
		default:
			w.Extra[key] = value
		}
	}
	return nil
}

func (w workspaceDocument) MarshalJSON() ([]byte, error) {
	raw := make(map[string]json.RawMessage, len(w.Extra)+4)
	for key, value := range w.Extra {
		raw[key] = value
	}

	var err error
	if raw["repoPaths"], err = json.Marshal(w.RepoPaths); err != nil {
		return nil, err
	}
	if raw["workingExpandedFolders"], err = json.Marshal(w.WorkingExpandedFolders); err != nil {
		return nil, err
	}
	if raw["workingFilePaths"], err = json.Marshal(w.WorkingFilePaths); err != nil {
		return nil, err
	}
	if raw["workingStoredSelection"], err = json.Marshal(w.WorkingStoredSelection); err != nil {
		return nil, err
	}

	return json.Marshal(raw)
}

func runWorkspacePaths(ctx *snap.Context) error {
	var args []string
	for i := 0; i < ctx.NArgs(); i++ {
		arg := strings.TrimSpace(ctx.Arg(i))
		if arg != "" {
			args = append(args, arg)
		}
	}

	var workspacePathArg string
	var cleanedArgs []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--file" || args[i] == "-f" {
			if i+1 >= len(args) {
				return fmt.Errorf("missing value for %s", args[i])
			}
			workspacePathArg = args[i+1]
			i++
			continue
		}
		cleanedArgs = append(cleanedArgs, args[i])
	}
	args = cleanedArgs

	listKind := workspaceListRepoPaths
	if len(args) > 0 {
		if parsed, ok := workspaceListFromArg(args[0]); ok {
			listKind = parsed
			args = args[1:]
		}
	}

	action := "list"
	if len(args) > 0 {
		action = strings.ToLower(args[0])
		args = args[1:]
	}

	var pathArg string
	if len(args) > 0 {
		pathArg = args[0]
	}

	workspaceFile, err := resolveWorkspaceFilePath(workspacePathArg)
	if err != nil {
		return err
	}

	doc, err := loadWorkspaceFile(workspaceFile)
	if err != nil {
		return fmt.Errorf("load workspace %s: %w", workspaceFile, err)
	}

	label := workspaceListLabels[listKind]
	switch action {
	case "list":
		return workspaceListPaths(ctx.Stdout(), doc.list(listKind), label, workspaceFile)
	case "add":
		return workspaceAddPath(ctx, doc, listKind, pathArg, workspaceFile)
	case "remove", "rm", "delete":
		return workspaceRemovePath(ctx, doc, listKind, pathArg, workspaceFile)
	default:
		return fmt.Errorf("unknown action %q (use list, add, remove)", action)
	}
}

func workspaceListFromArg(arg string) (workspaceList, bool) {
	arg = strings.TrimSpace(strings.ToLower(arg))
	list, ok := workspaceListAliases[arg]
	return list, ok
}

func loadWorkspaceFile(path string) (*workspaceDocument, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var doc workspaceDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse workspace file: %w", err)
	}
	return &doc, nil
}

func (w *workspaceDocument) save(path string) error {
	content, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}

	mode := fs.FileMode(0o644)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	return os.WriteFile(path, content, mode)
}

func (w *workspaceDocument) list(kind workspaceList) []string {
	switch kind {
	case workspaceListRepoPaths:
		return cloneStrings(w.RepoPaths)
	case workspaceListExpanded:
		return cloneStrings(w.WorkingExpandedFolders)
	case workspaceListSelection:
		return cloneStrings(w.WorkingStoredSelection.SelectedPaths)
	case workspaceListFileBuffer:
		return cloneStrings(w.WorkingFilePaths)
	default:
		return nil
	}
}

func (w *workspaceDocument) set(kind workspaceList, values []string) error {
	switch kind {
	case workspaceListRepoPaths:
		w.RepoPaths = cloneStrings(values)
	case workspaceListExpanded:
		w.WorkingExpandedFolders = cloneStrings(values)
	case workspaceListSelection:
		w.WorkingStoredSelection.SelectedPaths = cloneStrings(values)
	case workspaceListFileBuffer:
		w.WorkingFilePaths = cloneStrings(values)
	default:
		return fmt.Errorf("unknown workspace list %q", kind)
	}
	return nil
}

func resolveWorkspaceFilePath(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		if env := os.Getenv("FLOW_WORKSPACE_FILE"); strings.TrimSpace(env) != "" {
			path = env
		}
	}
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("determine home directory: %w", err)
		}
		path = filepath.Join(
			home,
			"Library",
			"Application Support",
			"RepoPrompt",
			"Workspaces",
			"Workspace-main-7E67B1B3-FCB7-4C1A-AD0E-476742996DB4",
			"workspace.json",
		)
	}

	expanded, err := expandUserPath(path)
	if err != nil {
		return "", fmt.Errorf("expand workspace file path: %w", err)
	}
	final, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("resolve workspace file path: %w", err)
	}
	return filepath.Clean(final), nil
}

func normalizeWorkspacePath(raw string) (string, error) {
	expanded, err := expandUserPath(raw)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func workspaceListPaths(out io.Writer, paths []string, label, file string) error {
	fmt.Fprintf(out, "Paths in %s (%s):\n", label, file)
	if len(paths) == 0 {
		fmt.Fprintln(out, "  (none)")
		return nil
	}
	for idx, p := range paths {
		fmt.Fprintf(out, "  %d. %s\n", idx+1, p)
	}
	return nil
}

func workspaceAddPath(ctx *snap.Context, doc *workspaceDocument, listKind workspaceList, rawPath, workspaceFile string) error {
	pathValue := strings.TrimSpace(rawPath)
	if pathValue == "" {
		current, _ := os.Getwd()
		reader := bufio.NewReader(ctx.Stdin())
		value, err := promptWithDefault(ctx.Stdout(), reader, fmt.Sprintf("Path to add to %s", workspaceListLabels[listKind]), current)
		if err != nil {
			return err
		}
		pathValue = value
	}

	normalized, err := normalizeWorkspacePath(pathValue)
	if err != nil {
		return fmt.Errorf("normalize path: %w", err)
	}

	paths := doc.list(listKind)
	if containsString(paths, normalized) {
		fmt.Fprintf(ctx.Stdout(), "Path already present in %s: %s\n", workspaceListLabels[listKind], normalized)
		return nil
	}

	paths = append(paths, normalized)
	if err := doc.set(listKind, paths); err != nil {
		return err
	}
	if err := doc.save(workspaceFile); err != nil {
		return fmt.Errorf("save workspace: %w", err)
	}

	fmt.Fprintf(ctx.Stdout(), "Added to %s: %s\n", workspaceListLabels[listKind], normalized)
	return nil
}

func workspaceRemovePath(ctx *snap.Context, doc *workspaceDocument, listKind workspaceList, rawPath, workspaceFile string) error {
	paths := doc.list(listKind)
	if len(paths) == 0 {
		fmt.Fprintf(ctx.Stdout(), "No paths to remove from %s\n", workspaceListLabels[listKind])
		return nil
	}

	target := strings.TrimSpace(rawPath)
	if target == "" {
		idx, err := fuzzyfinder.Find(
			paths,
			func(i int) string { return paths[i] },
			fuzzyfinder.WithPromptString(fmt.Sprintf("remove from %s> ", workspaceListLabels[listKind])),
		)
		if err != nil {
			if errors.Is(err, fuzzyfinder.ErrAbort) {
				fmt.Fprintln(ctx.Stdout(), "Aborted.")
				return nil
			}
			return fmt.Errorf("select path: %w", err)
		}
		target = paths[idx]
	} else {
		normalized, err := normalizeWorkspacePath(target)
		if err == nil {
			target = normalized
		} else {
			target = filepath.Clean(target)
		}
	}

	filtered, removed := removeString(paths, target)
	if !removed {
		fmt.Fprintf(ctx.Stdout(), "Path not found in %s: %s\n", workspaceListLabels[listKind], target)
		return nil
	}

	if err := doc.set(listKind, filtered); err != nil {
		return err
	}
	if err := doc.save(workspaceFile); err != nil {
		return fmt.Errorf("save workspace: %w", err)
	}

	fmt.Fprintf(ctx.Stdout(), "Removed from %s: %s\n", workspaceListLabels[listKind], target)
	return nil
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}

func containsString(list []string, candidate string) bool {
	for _, v := range list {
		if v == candidate {
			return true
		}
	}
	return false
}

func removeString(list []string, target string) ([]string, bool) {
	var out []string
	removed := false
	for _, v := range list {
		if v == target && !removed {
			removed = true
			continue
		}
		out = append(out, v)
	}
	return out, removed
}

func expandUserPath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("path cannot be empty")
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

func promptWithDefault(out io.Writer, reader *bufio.Reader, label, defaultValue string) (string, error) {
	prompt := label
	if defaultValue != "" {
		prompt = fmt.Sprintf("%s [%s]", label, defaultValue)
	}
	fmt.Fprintf(out, "%s: ", prompt)

	text, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}

	value := strings.TrimSpace(text)
	if value == "" {
		return defaultValue, nil
	}
	return value, nil
}
