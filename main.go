package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dzonerzy/go-snap/snap"
	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/html"
	"github.com/gomarkdown/markdown/parser"
	"github.com/ktr0731/go-fuzzyfinder"
)

const (
	flowName              = "fgo"
	flowVersion           = "1.0.0"
	symlinkCandidateLimit = 8000
)

var buildTime = "unknown"

var (
	errSymlinkSelectionAborted = errors.New("symlink selection aborted")
	errSymlinkCandidateLimit   = errors.New("symlink candidate limit reached")
)

var symlinkSkipDirectories = map[string]struct{}{
	".git":         {},
	".idea":        {},
	".vscode":      {},
	".cache":       {},
	"node_modules": {},
	"vendor":       {},
	"dist":         {},
	"build":        {},
	"target":       {},
}

type symlinkOption struct {
	Path   string
	Label  string
	Custom bool
}

func main() {
	app := snap.New(flowName, "fgo is CLI to do things fast").
		Version(flowVersion).
		DisableHelp()

	app.Command("updateGoVersion", "Upgrade Go using the workspace script").
		Action(func(ctx *snap.Context) error {
			scriptPath, err := determineUpgradeScriptPath()
			if err != nil {
				return err
			}

			if _, err := os.Stat(scriptPath); err != nil {
				return fmt.Errorf("unable to access %s: %w", scriptPath, err)
			}

			cmd := exec.Command(scriptPath)
			cmd.Stdout = ctx.Stdout()
			cmd.Stderr = ctx.Stderr()
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("running %s: %w", scriptPath, err)
			}

			return nil
		})

	app.Command("killPort", "Kill a process by the port it listens on, optionally with fuzzy finder").
		Action(func(ctx *snap.Context) error {
			if ctx.NArgs() > 1 {
				fmt.Fprintf(ctx.Stderr(), "Usage: %s killPort [port]\n", flowName)
				return fmt.Errorf("expected at most 1 argument, got %d", ctx.NArgs())
			}

			processes, err := listListeningProcesses()
			if err != nil {
				return err
			}

			if len(processes) == 0 {
				fmt.Fprintln(ctx.Stdout(), "No listening TCP ports found.")
				return nil
			}

			targets := processes
			if ctx.NArgs() == 1 {
				rawPort := strings.TrimSpace(ctx.Arg(0))
				if rawPort == "" {
					fmt.Fprintf(ctx.Stderr(), "Usage: %s killPort [port]\n", flowName)
					return fmt.Errorf("port cannot be empty")
				}

				targets = uniqueByPID(filterProcessesByPort(processes, rawPort))
				if len(targets) == 0 {
					fmt.Fprintf(ctx.Stdout(), "No listening process found on port %s.\n", rawPort)
					return nil
				}

				if len(targets) == 1 {
					selected := targets[0]
					if err := killProcess(selected.PID); err != nil {
						return fmt.Errorf("kill pid %d: %w", selected.PID, err)
					}
					fmt.Fprintf(ctx.Stdout(), "Killed %s (pid %d) listening on %s\n", selected.Command, selected.PID, selected.Address)
					return nil
				}
			}

			idx, err := fuzzyfinder.Find(
				targets,
				func(i int) string {
					p := targets[i]
					return fmt.Sprintf("%s (%d) %s", p.Command, p.PID, p.Address)
				},
				fuzzyfinder.WithPromptString("killPort> "),
			)
			if err != nil {
				if errors.Is(err, fuzzyfinder.ErrAbort) {
					return nil
				}
				return fmt.Errorf("select port: %w", err)
			}

			selected := targets[idx]
			if err := killProcess(selected.PID); err != nil {
				return fmt.Errorf("kill pid %d: %w", selected.PID, err)
			}

			fmt.Fprintf(ctx.Stdout(), "Killed %s (pid %d) listening on %s\n", selected.Command, selected.PID, selected.Address)
			return nil
		})

	app.Command("tasks", "List Taskfile tasks with descriptions").
		Action(func(ctx *snap.Context) error {
			return tasksCmd(ctx)
		})

	app.Command("workspacePaths", "List/add/remove path lists inside RepoPrompt workspace.json").
		Action(func(ctx *snap.Context) error {
			return workspacePathsCmd(ctx)
		})

	app.Command("checkoutPR", "Checkout a GitHub pull request by URL or number").
		Action(func(ctx *snap.Context) error {
			if ctx.NArgs() != 1 {
				fmt.Fprintf(ctx.Stderr(), "Usage: %s checkoutPR <github-pr-url-or-number>\n", flowName)
				return fmt.Errorf("expected 1 argument, got %d", ctx.NArgs())
			}

			input := strings.TrimSpace(ctx.Arg(0))
			if input == "" {
				fmt.Fprintf(ctx.Stderr(), "Usage: %s checkoutPR <github-pr-url-or-number>\n", flowName)
				return fmt.Errorf("pull request reference cannot be empty")
			}

			prNumber, err := extractPullRequestNumber(input)
			if err != nil {
				return err
			}

			if _, err := exec.LookPath("gh"); err != nil {
				return fmt.Errorf("gh CLI not found in PATH: %w", err)
			}

			cmd := exec.Command("gh", "pr", "checkout", strconv.Itoa(prNumber))
			cmd.Stdout = ctx.Stdout()
			cmd.Stderr = ctx.Stderr()
			cmd.Stdin = ctx.Stdin()
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("gh pr checkout %d: %w", prNumber, err)
			}

			return nil
		})

	app.Command("clonePR", "Clone a GitHub pull request into ~/pr with an interactive flow").
		Action(func(ctx *snap.Context) error {
			if ctx.NArgs() > 1 {
				fmt.Fprintf(ctx.Stderr(), "Usage: %s clonePR [github-pr-ref]\n", flowName)
				return fmt.Errorf("expected at most 1 argument, got %d", ctx.NArgs())
			}

			if _, err := exec.LookPath("gh"); err != nil {
				return fmt.Errorf("gh CLI not found in PATH: %w", err)
			}

			initialInput := ""
			if ctx.NArgs() == 1 {
				initialInput = strings.TrimSpace(ctx.Arg(0))
			}
			if initialInput == "" {
				if clip := clipboardPullRequestRef(); clip != "" {
					fmt.Fprintf(ctx.Stdout(), "Using PR from clipboard: %s\n", clip)
					initialInput = clip
				}
			}

			repo, prNumber, err := promptPullRequestDetails(ctx.Stdout(), ctx.Stdin(), initialInput)
			if err != nil {
				return err
			}

			dest, err := pullRequestDestination(repo, prNumber)
			if err != nil {
				return err
			}

			fmt.Fprintf(ctx.Stdout(), "\nClone %s PR #%d into %s\n", repo, prNumber, dest)
			proceed, err := promptYesNo(ctx.Stdout(), ctx.Stdin(), "Proceed", true)
			if err != nil {
				return err
			}
			if !proceed {
				fmt.Fprintln(ctx.Stdout(), "Aborted.")
				return nil
			}

			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return fmt.Errorf("create destination parent: %w", err)
			}

			if _, err := os.Stat(dest); err == nil {
				return fmt.Errorf("destination %s already exists", dest)
			} else if !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("check destination %s: %w", dest, err)
			}

			fmt.Fprintf(ctx.Stdout(), "\nCloning %s...\n", repo)
			cloneCmd := exec.Command("gh", "repo", "clone", repo, dest)
			cloneCmd.Stdout = ctx.Stdout()
			cloneCmd.Stderr = ctx.Stderr()
			cloneCmd.Stdin = ctx.Stdin()
			if err := cloneCmd.Run(); err != nil {
				return fmt.Errorf("gh repo clone %s: %w", repo, err)
			}

			fmt.Fprintf(ctx.Stdout(), "\nChecking out PR #%d...\n", prNumber)
			checkoutCmd := exec.Command("gh", "pr", "checkout", strconv.Itoa(prNumber))
			checkoutCmd.Dir = dest
			checkoutCmd.Stdout = ctx.Stdout()
			checkoutCmd.Stderr = ctx.Stderr()
			checkoutCmd.Stdin = ctx.Stdin()
			if err := checkoutCmd.Run(); err != nil {
				return fmt.Errorf("gh pr checkout %d: %w", prNumber, err)
			}

			fmt.Fprintf(ctx.Stdout(), "\nDone. Repo ready at %s\n", dest)
			return nil
		})

	app.Command("symlink", "Create a symbolic link with an interactive picker for the original path").
		Action(func(ctx *snap.Context) error {
			if ctx.NArgs() != 1 {
				fmt.Fprintf(ctx.Stderr(), "Usage: %s symlink <link-path>\n", flowName)
				return fmt.Errorf("expected 1 argument, got %d", ctx.NArgs())
			}

			rawLink := strings.TrimSpace(ctx.Arg(0))
			if rawLink == "" {
				fmt.Fprintf(ctx.Stderr(), "Usage: %s symlink <link-path>\n", flowName)
				return fmt.Errorf("link path cannot be empty")
			}

			linkPath, err := expandUserPath(rawLink)
			if err != nil {
				return fmt.Errorf("expand link path: %w", err)
			}
			linkPath = filepath.Clean(linkPath)

			fmt.Fprintf(ctx.Stdout(), "Select the original file or directory for %s\n", linkPath)
			original, err := selectSymlinkSource(ctx)
			if err != nil {
				if errors.Is(err, errSymlinkSelectionAborted) {
					fmt.Fprintln(ctx.Stdout(), "Aborted.")
					return nil
				}
				return err
			}
			original = filepath.Clean(original)

			if _, err := os.Lstat(original); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return fmt.Errorf("original path %s does not exist", original)
				}
				return fmt.Errorf("stat original %s: %w", original, err)
			}

			if _, err := os.Lstat(linkPath); err == nil {
				return fmt.Errorf("destination %s already exists", linkPath)
			} else if !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("stat destination %s: %w", linkPath, err)
			}

			parent := filepath.Dir(linkPath)
			if parent != "" && parent != "." {
				if err := os.MkdirAll(parent, 0o755); err != nil {
					return fmt.Errorf("create parent directory %s: %w", parent, err)
				}
			}

			if err := os.Symlink(original, linkPath); err != nil {
				return fmt.Errorf("create symlink %s -> %s: %w", linkPath, original, err)
			}

			fmt.Fprintf(ctx.Stdout(), "Created %s -> %s\n", linkPath, original)
			return nil
		})

	app.Command("tryBranch", "Create a new try-N git branch using the next available number").
		Action(func(ctx *snap.Context) error {
			if ctx.NArgs() != 0 {
				fmt.Fprintf(ctx.Stderr(), "Usage: %s tryBranch\n", flowName)
				return fmt.Errorf("expected 0 arguments, got %d", ctx.NArgs())
			}

			name, err := determineNextTryBranchName()
			if err != nil {
				return err
			}

			fmt.Fprintf(ctx.Stdout(), "Creating branch %s\n", name)

			cmd := exec.Command("git", "checkout", "-b", name)
			cmd.Stdout = ctx.Stdout()
			cmd.Stderr = ctx.Stderr()
			cmd.Stdin = ctx.Stdin()
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("create branch %s: %w", name, err)
			}

			return nil
		})

	app.Command("try", "Create a numbered scratch directory in ~/t and open a shell there").
		Action(func(ctx *snap.Context) error {
			if ctx.NArgs() != 0 {
				fmt.Fprintf(ctx.Stderr(), "Usage: %s try\n", flowName)
				return fmt.Errorf("expected 0 arguments, got %d", ctx.NArgs())
			}

			base, err := tryBaseDir()
			if err != nil {
				return err
			}

			dir, err := createRandomTryDir(base)
			if err != nil {
				return err
			}

			fmt.Fprintf(ctx.Stdout(), "Created %s\n", dir)

			shell := detectShell()
			fmt.Fprintf(ctx.Stdout(), "Launching shell in %s (exit to return)\n\n", dir)

			cmd := exec.Command(shell)
			cmd.Dir = dir
			cmd.Stdout = ctx.Stdout()
			cmd.Stderr = ctx.Stderr()
			cmd.Stdin = ctx.Stdin()
			cmd.Env = os.Environ()
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("start shell in %s: %w", dir, err)
			}

			return nil
		})

	app.Command("version", "Reports the current version of flow").
		Action(func(ctx *snap.Context) error {
			fmt.Fprintf(ctx.Stdout(), "%s (built %s)\n", flowVersion, buildTime)
			return nil
		})

	app.Command("openMd", "Convert a markdown file to HTML and open it in the browser").
		Action(func(ctx *snap.Context) error {
			fmt.Fprintln(ctx.Stdout(), "openMd: starting")
			if ctx.NArgs() != 1 {
				fmt.Fprintf(ctx.Stderr(), "Usage: %s openMd <path-to-file.md>\n", flowName)
				return fmt.Errorf("expected 1 argument, got %d", ctx.NArgs())
			}

			mdPath := strings.TrimSpace(ctx.Arg(0))
			fmt.Fprintf(ctx.Stdout(), "openMd: mdPath=%s\n", mdPath)
			if mdPath == "" {
				fmt.Fprintf(ctx.Stderr(), "Usage: %s openMd <path-to-file.md>\n", flowName)
				return fmt.Errorf("file path cannot be empty")
			}

			if !strings.HasSuffix(mdPath, ".md") {
				mdPath = mdPath + ".md"
			}

			mdContent, err := os.ReadFile(mdPath)
			if err != nil {
				return fmt.Errorf("read %s: %w", mdPath, err)
			}

			htmlContent := mdToHTML(mdContent)

			baseName := filepath.Base(mdPath)
			htmlName := strings.TrimSuffix(baseName, ".md") + ".html"
			htmlPath := filepath.Join(os.TempDir(), htmlName)

			if err := os.WriteFile(htmlPath, htmlContent, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", htmlPath, err)
			}

			fmt.Fprintf(ctx.Stdout(), "Opening %s\n", htmlPath)

			openCmd := exec.Command("open", htmlPath)
			openCmd.Stdout = ctx.Stdout()
			openCmd.Stderr = ctx.Stderr()
			if err := openCmd.Run(); err != nil {
				return fmt.Errorf("open %s: %w", htmlPath, err)
			}

			return nil
		})

	args := os.Args[1:]
	if handled := handleTopLevel(args, os.Stdout); handled {
		return
	}

	app.RunAndExit()
}

func handleTopLevel(args []string, out io.Writer) bool {
	if len(args) == 0 {
		if err := openCurrentDirectory(out); err != nil {
			fmt.Fprintf(out, "open . failed: %v\n", err)
			printRootHelp(out)
		}
		return true
	}

	switch args[0] {
	case "--help", "-h", "h":
		printRootHelp(out)
		return true
	case "--version":
		fmt.Fprintf(out, "%s (built %s)\n", flowVersion, buildTime)
		return true
	case "help":
		if len(args) == 1 {
			printRootHelp(out)
			return true
		}
		if printCommandHelp(args[1], out) {
			return true
		}
		fmt.Fprintf(out, "Unknown help topic %q\n", args[1])
		return true
	}

	if len(args) > 1 {
		last := args[len(args)-1]
		if last == "--help" || last == "-h" {
			if printCommandHelp(args[0], out) {
				return true
			}
			printRootHelp(out)
			return true
		}
	}

	return false
}

func printCommandHelp(name string, out io.Writer) bool {
	switch name {
	case "clonePR":
		fmt.Fprintln(out, "Clone a GitHub pull request into ~/pr with an interactive flow")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Usage:")
		fmt.Fprintf(out, "  %s clonePR [github-pr-ref]\n", flowName)
		return true
	case "checkoutPR":
		fmt.Fprintln(out, "Checkout a GitHub pull request by URL or number")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Usage:")
		fmt.Fprintf(out, "  %s checkoutPR <github-pr-url-or-number>\n", flowName)
		return true
	case "killPort":
		fmt.Fprintln(out, "Kill a process by the port it listens on, optionally with fuzzy finder")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Usage:")
		fmt.Fprintf(out, "  %s killPort [port]\n", flowName)
		return true
	case "try":
		fmt.Fprintln(out, "Create a numbered scratch directory in ~/t and open a shell there")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Usage:")
		fmt.Fprintf(out, "  %s try\n", flowName)
		return true
	case "symlink":
		fmt.Fprintln(out, "Create a symbolic link with an interactive picker for the original path")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Usage:")
		fmt.Fprintf(out, "  %s symlink <link-path>\n", flowName)
		return true
	case "tryBranch":
		fmt.Fprintln(out, "Create a new try-N git branch using the next available number")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Usage:")
		fmt.Fprintf(out, "  %s tryBranch\n", flowName)
		return true
	case "updateGoVersion":
		fmt.Fprintln(out, "Upgrade Go using the workspace script")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Usage:")
		fmt.Fprintf(out, "  %s updateGoVersion\n", flowName)
		return true
	case "version":
		fmt.Fprintln(out, "Reports the current version of flow")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Usage:")
		fmt.Fprintf(out, "  %s version\n", flowName)
		return true
	case "tasks":
		fmt.Fprintln(out, "List Taskfile tasks with descriptions")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Usage:")
		fmt.Fprintf(out, "  %s tasks [-f|--file Taskfile.yml]\n", flowName)
		return true
	case "workspacePaths":
		fmt.Fprintln(out, "List/add/remove path lists inside RepoPrompt workspace.json")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Usage:")
		fmt.Fprintf(out, "  %s workspacePaths [list] [list|add|remove] [path] [-f|--file workspace.json]\n", flowName)
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Lists: repo (default), expanded, selection, files")
		return true
	case "openMd":
		fmt.Fprintln(out, "Convert a markdown file to HTML and open it in the browser")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Usage:")
		fmt.Fprintf(out, "  %s openMd <path-to-file>\n", flowName)
		fmt.Fprintln(out)
		fmt.Fprintln(out, "The .md extension is added automatically if not provided.")
		return true
	}

	return false
}

func printRootHelp(out io.Writer) {
	fmt.Fprintf(out, "%s is CLI to do things fast\n", flowName)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintf(out, "  %s [command]\n", flowName)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Available Commands:")
	fmt.Fprintln(out, "  help             Help about any command")
	fmt.Fprintln(out, "  clonePR          Clone a GitHub pull request into ~/pr with an interactive flow")
	fmt.Fprintln(out, "  checkoutPR       Checkout a GitHub pull request by URL or number")
	fmt.Fprintln(out, "  killPort         Kill a process by the port it listens on, optionally with fuzzy finder")
	fmt.Fprintln(out, "  symlink          Create a symbolic link with an interactive picker for the original path")
	fmt.Fprintln(out, "  try              Create a numbered scratch directory in ~/t and open a shell there")
	fmt.Fprintln(out, "  tryBranch        Create a new try-N git branch using the next available number")
	fmt.Fprintln(out, "  updateGoVersion  Upgrade Go using the workspace script")
	fmt.Fprintln(out, "  tasks            List Taskfile tasks with descriptions")
	fmt.Fprintln(out, "  workspacePaths   List/add/remove path lists inside RepoPrompt workspace.json")
	fmt.Fprintln(out, "  openMd           Convert a markdown file to HTML and open in browser")
	fmt.Fprintln(out, "  version          Reports the current version of flow")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Flags:")
	fmt.Fprintln(out, "  -h, --help   help for flow")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Use \"%s [command] --help\" for more information about a command.\n", flowName)
}

func determineUpgradeScriptPath() (string, error) {
	if path := os.Getenv("FLOW_UPGRADE_SCRIPT_PATH"); path != "" {
		return path, nil
	}

	if root := os.Getenv("FLOW_CONFIG_ROOT"); root != "" {
		return filepath.Join(root, "sh", "upgrade-go-version.sh"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}

	return filepath.Join(home, "src", "config", "sh", "upgrade-go-version.sh"), nil
}

func tryBaseDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	return filepath.Join(home, "t"), nil
}

func createRandomTryDir(base string) (string, error) {
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", fmt.Errorf("create base directory %s: %w", base, err)
	}

	rand.Seed(time.Now().UnixNano())
	for i := 0; i < 10; i++ {
		name := strconv.Itoa(rand.Intn(9000) + 1000) // 1000-9999
		full := filepath.Join(base, name)
		if _, err := os.Stat(full); errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(full, 0o755); err != nil {
				if errors.Is(err, fs.ErrExist) {
					continue
				}
				return "", fmt.Errorf("create directory %s: %w", full, err)
			}
			return full, nil
		}
	}

	return "", fmt.Errorf("unable to create unique directory in %s after several attempts", base)
}

func detectShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/bash"
}

func selectSymlinkSource(ctx *snap.Context) (string, error) {
	root, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("determine working directory: %w", err)
	}

	options, err := gatherSymlinkOptions(root)
	if err != nil {
		return "", fmt.Errorf("gather symlink options: %w", err)
	}

	options = append(options, symlinkOption{
		Label:  "Enter custom path…",
		Custom: true,
	})

	idx, err := fuzzyfinder.Find(
		options,
		func(i int) string {
			return options[i].Label
		},
		fuzzyfinder.WithPromptString("symlink source> "),
	)
	if err != nil {
		if errors.Is(err, fuzzyfinder.ErrAbort) {
			return "", errSymlinkSelectionAborted
		}
		return "", fmt.Errorf("select original path: %w", err)
	}

	selected := options[idx]
	if selected.Custom {
		return promptCustomSymlinkPath(ctx.Stdout(), ctx.Stdin())
	}
	return selected.Path, nil
}

func gatherSymlinkOptions(root string) ([]symlinkOption, error) {
	var options []symlinkOption
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if d.IsDir() && shouldSkipSymlinkDir(d.Name()) {
			return filepath.SkipDir
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.Clean(rel)
		display := filepath.ToSlash(rel)
		opt := symlinkOption{
			Path: rel,
		}
		if d.IsDir() {
			opt.Label = fmt.Sprintf("[D] %s/", display)
		} else {
			opt.Label = fmt.Sprintf("[F] %s", display)
		}
		options = append(options, opt)
		if len(options) >= symlinkCandidateLimit {
			return errSymlinkCandidateLimit
		}
		return nil
	})
	if err != nil && !errors.Is(err, errSymlinkCandidateLimit) {
		return nil, err
	}

	sort.Slice(options, func(i, j int) bool {
		return options[i].Label < options[j].Label
	})
	return options, nil
}

func shouldSkipSymlinkDir(name string) bool {
	_, skip := symlinkSkipDirectories[name]
	return skip
}

func promptCustomSymlinkPath(out io.Writer, in io.Reader) (string, error) {
	reader := bufio.NewReader(in)
	for {
		fmt.Fprint(out, "Enter path to the original file: ")
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				if strings.TrimSpace(line) == "" {
					return "", errSymlinkSelectionAborted
				}
			} else {
				return "", fmt.Errorf("read path: %w", err)
			}
		}
		path := strings.TrimSpace(line)
		if path == "" {
			fmt.Fprintln(out, "Path cannot be empty.")
			continue
		}
		expanded, err := expandUserPath(path)
		if err != nil {
			fmt.Fprintf(out, "Invalid path: %v\n", err)
			continue
		}
		return filepath.Clean(expanded), nil
	}
}

func expandUserPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path cannot be empty")
	}
	if path[0] != '~' {
		return path, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}

	if len(path) == 1 {
		return home, nil
	}

	switch path[1] {
	case '/', '\\':
		return filepath.Join(home, path[2:]), nil
	default:
		return "", fmt.Errorf("unsupported ~ expansion in %q", path)
	}
}

func determineNextTryBranchName() (string, error) {
	branches, err := listGitBranches()
	if err != nil {
		return "", err
	}

	max := 0
	for _, branch := range branches {
		candidate := branch
		if idx := strings.LastIndex(candidate, "/"); idx >= 0 {
			candidate = candidate[idx+1:]
		}
		if !strings.HasPrefix(candidate, "try-") {
			continue
		}
		number, err := strconv.Atoi(candidate[len("try-"):])
		if err != nil {
			continue
		}
		if number > max {
			max = number
		}
	}

	return fmt.Sprintf("try-%d", max+1), nil
}

func listGitBranches() ([]string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	cmd := exec.Command("git", "branch", "--format=%(refname:short)", "--all")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("list git branches: %s: %w", msg, err)
		}
		return nil, fmt.Errorf("list git branches: %w", err)
	}

	var branches []string
	scanner := bufio.NewScanner(&stdout)
	for scanner.Scan() {
		branch := strings.TrimSpace(scanner.Text())
		if branch != "" {
			branches = append(branches, branch)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan git branches: %w", err)
	}

	return branches, nil
}

func openCurrentDirectory(out io.Writer) error {
	cmd := exec.Command("open", ".")
	cmd.Stdout = out
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

type listeningProcess struct {
	Command string
	User    string
	PID     int
	Address string
	Port    string
	Raw     string
}

func listListeningProcesses() ([]listeningProcess, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	cmd := exec.Command("lsof", "-nP", "-iTCP", "-sTCP:LISTEN")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("list listening ports: %s: %w", msg, err)
		}
		return nil, fmt.Errorf("list listening ports: %w", err)
	}

	scanner := bufio.NewScanner(&stdout)
	var processes []listeningProcess
	firstLine := true
	for scanner.Scan() {
		line := scanner.Text()
		if firstLine {
			firstLine = false
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}

		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}

		address := fields[len(fields)-2]
		port := address
		if idx := strings.LastIndex(address, ":"); idx >= 0 && idx+1 < len(address) {
			port = address[idx+1:]
		}

		processes = append(processes, listeningProcess{
			Command: fields[0],
			User:    fields[2],
			PID:     pid,
			Address: address,
			Port:    port,
			Raw:     line,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan lsof output: %w", err)
	}

	return processes, nil
}

func killProcess(pid int) error {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	return nil
}

func filterProcessesByPort(processes []listeningProcess, targetPort string) []listeningProcess {
	var filtered []listeningProcess
	for _, p := range processes {
		if p.Port == targetPort {
			filtered = append(filtered, p)
		}
	}
	return filtered
}

func uniqueByPID(processes []listeningProcess) []listeningProcess {
	seen := make(map[int]struct{})
	var unique []listeningProcess
	for _, p := range processes {
		if _, ok := seen[p.PID]; ok {
			continue
		}
		seen[p.PID] = struct{}{}
		unique = append(unique, p)
	}
	return unique
}

func promptPullRequestDetails(out io.Writer, in io.Reader, initial string) (string, int, error) {
	reader := bufio.NewReader(in)
	current := strings.TrimSpace(initial)

	for {
		value, err := promptWithDefault(out, reader, "GitHub PR (URL or owner/repo#123)", current)
		if err != nil {
			return "", 0, err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			fmt.Fprintln(out, "Pull request reference cannot be empty.")
			current = ""
			continue
		}
		repoGuess, numberGuess, repoFound, numberFound := guessPullRequestDetails(value)

		repo := repoGuess
		if !repoFound {
			if repo, err = promptRepo(out, reader); err != nil {
				return "", 0, err
			}
		}

		prNumber := numberGuess
		if !numberFound {
			if prNumber, err = promptPullRequestNumber(out, reader); err != nil {
				return "", 0, err
			}
		}

		return repo, prNumber, nil
	}
}

func guessPullRequestDetails(input string) (string, int, bool, bool) {
	candidate := strings.TrimSpace(strings.TrimSuffix(input, "/"))
	if candidate == "" {
		return "", 0, false, false
	}

	var repo string
	var number int
	repoFound := false
	numberFound := false

	if idx := strings.Index(candidate, "#"); idx > 0 {
		repoPart := strings.TrimSpace(candidate[:idx])
		if isLikelyRepoSlug(repoPart) {
			repo = repoPart
			repoFound = true
		}
		if n, ok := parseNumericCandidate(candidate[idx+1:]); ok {
			number = n
			numberFound = true
		}
	}

	if strings.HasPrefix(candidate, "http://") || strings.HasPrefix(candidate, "https://") {
		if u, err := url.Parse(candidate); err == nil && u.Host != "" {
			segments := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(segments) >= 2 {
				repoPath := segments[0] + "/" + segments[1]
				if isLikelyRepoSlug(repoPath) {
					repo = repoPath
					repoFound = true
				}
			}
			for i := 0; i < len(segments); i++ {
				if segments[i] == "pull" || segments[i] == "pulls" {
					if i+1 < len(segments) {
						if n, ok := parseNumericCandidate(segments[i+1]); ok {
							number = n
							numberFound = true
						}
					}
				}
			}
		}
	}

	parts := strings.Split(candidate, "/")
	if len(parts) >= 2 {
		repoPath := parts[0] + "/" + parts[1]
		if isLikelyRepoSlug(repoPath) && !repoFound {
			repo = repoPath
			repoFound = true
		}
		for i := 0; i < len(parts); i++ {
			if parts[i] == "pull" || parts[i] == "pulls" {
				if i+1 < len(parts) {
					if n, ok := parseNumericCandidate(parts[i+1]); ok {
						number = n
						numberFound = true
					}
				}
			}
		}
	}

	if !numberFound {
		if n, ok := parseNumericCandidate(candidate); ok {
			number = n
			numberFound = true
		}
	}

	return repo, number, repoFound, numberFound
}

func isLikelyRepoSlug(repo string) bool {
	parts := strings.Split(repo, "/")
	return len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != ""
}

func pullRequestDestination(repo string, prNumber int) (string, error) {
	if !isLikelyRepoSlug(repo) {
		return "", fmt.Errorf("invalid repo %q", repo)
	}
	if prNumber <= 0 {
		return "", fmt.Errorf("invalid pull request number %d", prNumber)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}

	repoName := filepath.Base(repo)
	return filepath.Join(home, "pr", fmt.Sprintf("%s-pr%d", repoName, prNumber)), nil
}

func clipboardPullRequestRef() string {
	cmd := exec.Command("pbpaste")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return ""
	}

	text := strings.TrimSpace(stdout.String())
	if text == "" {
		return ""
	}

	_, _, repoFound, numberFound := guessPullRequestDetails(text)
	if repoFound && numberFound {
		return text
	}
	return ""
}

func promptRepo(out io.Writer, reader *bufio.Reader) (string, error) {
	for {
		value, err := promptWithDefault(out, reader, "Repository (owner/name)", "")
		if err != nil {
			return "", err
		}
		value = strings.TrimSpace(value)
		if !isLikelyRepoSlug(value) {
			fmt.Fprintln(out, "Enter repository as owner/name.")
			continue
		}
		return value, nil
	}
}

func promptPullRequestNumber(out io.Writer, reader *bufio.Reader) (int, error) {
	for {
		value, err := promptWithDefault(out, reader, "Pull request number", "")
		if err != nil {
			return 0, err
		}
		if number, ok := parseNumericCandidate(value); ok {
			return number, nil
		}
		fmt.Fprintln(out, "Pull request number must be a positive integer.")
	}
}

func promptWithDefault(out io.Writer, reader *bufio.Reader, label, defaultValue string) (string, error) {
	label = strings.TrimSpace(label)
	prompt := label
	if defaultValue != "" {
		prompt += fmt.Sprintf(" [%s]", defaultValue)
	}
	prompt += ": "
	fmt.Fprint(out, prompt)

	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read input: %w", err)
	}
	line = strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(line) == "" {
		return defaultValue, nil
	}
	return line, nil
}

func promptYesNo(out io.Writer, in io.Reader, label string, defaultYes bool) (bool, error) {
	reader := bufio.NewReader(in)
	yesOpt := "Y"
	noOpt := "n"
	if !defaultYes {
		yesOpt = "y"
		noOpt = "N"
	}
	for {
		fmt.Fprintf(out, "%s [%s/%s]: ", label, yesOpt, noOpt)
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, fmt.Errorf("read input: %w", err)
		}
		line = strings.ToLower(strings.TrimSpace(line))
		if line == "" {
			return defaultYes, nil
		}
		switch line {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Fprintln(out, "Please answer yes or no.")
		}
	}
}

func extractPullRequestNumber(input string) (int, error) {
	candidate := strings.TrimSpace(input)
	candidate = strings.TrimSuffix(candidate, "/")
	if candidate == "" {
		return 0, fmt.Errorf("pull request reference cannot be empty")
	}

	if number, ok := parseNumericCandidate(candidate); ok {
		return number, nil
	}

	if strings.HasPrefix(candidate, "http://") || strings.HasPrefix(candidate, "https://") {
		u, err := url.Parse(candidate)
		if err == nil && u.Host != "" {
			segments := strings.Split(strings.Trim(u.Path, "/"), "/")
			for i := 0; i < len(segments); i++ {
				segment := segments[i]
				if segment == "pull" || segment == "pulls" {
					if i+1 < len(segments) {
						if number, ok := parseNumericCandidate(segments[i+1]); ok {
							return number, nil
						}
					}
				}
			}
		}
	}

	if idx := strings.LastIndex(candidate, "#"); idx >= 0 && idx+1 < len(candidate) {
		if number, ok := parseNumericCandidate(candidate[idx+1:]); ok {
			return number, nil
		}
	}

	if idx := strings.LastIndex(candidate, "/"); idx >= 0 && idx+1 < len(candidate) {
		if number, ok := parseNumericCandidate(candidate[idx+1:]); ok {
			return number, nil
		}
	}

	return 0, fmt.Errorf("unable to determine pull request number from %q", input)
}

func parseNumericCandidate(raw string) (int, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, false
	}
	if idx := strings.IndexAny(trimmed, "?#"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return 0, false
	}
	number, err := strconv.Atoi(trimmed)
	if err != nil || number <= 0 {
		return 0, false
	}
	return number, true
}

func mdToHTML(md []byte) []byte {
	extensions := parser.CommonExtensions | parser.AutoHeadingIDs | parser.NoEmptyLineBeforeBlock
	p := parser.NewWithExtensions(extensions)
	doc := p.Parse(md)

	htmlFlags := html.CommonFlags | html.HrefTargetBlank | html.CompletePage
	opts := html.RendererOptions{Flags: htmlFlags}
	renderer := html.NewRenderer(opts)

	return markdown.Render(doc, renderer)
}
