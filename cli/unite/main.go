package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dzonerzy/go-snap/snap"
	fzf "github.com/junegunn/fzf/src"
	fzfutil "github.com/junegunn/fzf/src/util"
)

const (
	uniteVersion   = "1.0.0"
	defaultCommand = "unite"
	defaultSummary = "unite is a unified fuzzy search across all your CLIs"
)

var (
	commandName    = defaultCommand
	commandSummary = defaultSummary
)

type CommandSource struct {
	Name     string
	Binary   string
	Commands []Command
}

type Command struct {
	Name        string
	Description string
	Source      *CommandSource
}

var sources = []CommandSource{
	{
		Name:   "fgo",
		Binary: "/Users/nikiv/bin/fgo",
	},
	{
		Name:   "rflow",
		Binary: "/Users/nikiv/lang/rust/target/release/flow",
	},
}

func init() {
	if name, ok := os.LookupEnv("UNITE_COMMAND_NAME"); ok && strings.TrimSpace(name) != "" {
		commandName = strings.TrimSpace(name)
	} else {
		commandName = filepath.Base(os.Args[0])
	}
	if summary, ok := os.LookupEnv("UNITE_COMMAND_SUMMARY"); ok && strings.TrimSpace(summary) != "" {
		commandSummary = strings.TrimSpace(summary)
	}
}

func main() {
	app := snap.New(commandName, commandSummary).
		Version(uniteVersion).
		DisableHelp()

	app.Command("search", "Fuzzy search across all command sources").
		Action(func(ctx *snap.Context) error {
			return runSearch()
		})

	app.Command("list", "List all available commands from all sources").
		Action(func(ctx *snap.Context) error {
			return runList(ctx.Stdout())
		})

	app.Command("sources", "List configured command sources").
		Action(func(ctx *snap.Context) error {
			return runSources(ctx.Stdout())
		})

	app.Command("deploy", "Install unite into ~/bin").
		Action(func(ctx *snap.Context) error {
			return runDeploy(ctx.Stdout())
		})

	app.Command("version", "Reports the current version").
		Action(func(ctx *snap.Context) error {
			fmt.Fprintf(ctx.Stdout(), "%s version %s\n", commandName, uniteVersion)
			return nil
		})

	if len(os.Args) < 2 {
		if err := runSearch(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	app.RunAndExit()
}

func loadAllCommands() ([]Command, error) {
	var allCommands []Command

	for i := range sources {
		src := &sources[i]
		commands, err := loadCommandsFromSource(src)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to load commands from %s: %v\n", src.Name, err)
			continue
		}
		src.Commands = commands
		allCommands = append(allCommands, commands...)
	}

	return allCommands, nil
}

func loadCommandsFromSource(src *CommandSource) ([]Command, error) {
	if _, err := os.Stat(src.Binary); err != nil {
		return nil, fmt.Errorf("binary not found: %s", src.Binary)
	}

	cmd := exec.Command(src.Binary, "help")
	output, err := cmd.Output()
	if err != nil {
		cmd = exec.Command(src.Binary, "--help")
		output, err = cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("failed to get help output: %w", err)
		}
	}

	return parseHelpOutput(src, string(output)), nil
}

func parseHelpOutput(src *CommandSource, output string) []Command {
	var commands []Command
	scanner := bufio.NewScanner(strings.NewReader(output))
	inCommands := false

	for scanner.Scan() {
		line := scanner.Text()

		if strings.Contains(strings.ToLower(line), "commands:") ||
			strings.Contains(strings.ToLower(line), "subcommands:") {
			inCommands = true
			continue
		}

		if inCommands && strings.TrimSpace(line) == "" {
			continue
		}

		if inCommands {
			if strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t") {
				parts := strings.SplitN(strings.TrimSpace(line), " ", 2)
				if len(parts) >= 1 {
					name := strings.TrimSpace(parts[0])
					desc := ""
					if len(parts) > 1 {
						desc = strings.TrimSpace(parts[1])
					}
					if name != "" && name != "help" && !strings.HasPrefix(name, "-") {
						commands = append(commands, Command{
							Name:        name,
							Description: desc,
							Source:      src,
						})
					}
				}
			}
		}
	}

	if len(commands) == 0 {
		commands = parseFallback(src, output)
	}

	return commands
}

func parseFallback(src *CommandSource, output string) []Command {
	var commands []Command
	scanner := bufio.NewScanner(strings.NewReader(output))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if strings.Contains(line, ":") {
			parts := strings.SplitN(line, ":", 2)
			name := strings.TrimSpace(parts[0])
			desc := ""
			if len(parts) > 1 {
				desc = strings.TrimSpace(parts[1])
			}
			if name != "" && !strings.HasPrefix(name, "-") && len(name) < 40 {
				commands = append(commands, Command{
					Name:        name,
					Description: desc,
					Source:      src,
				})
			}
		}
	}

	return commands
}

func runSearch() error {
	if !fzfutil.IsTty(os.Stdin) || !fzfutil.IsTty(os.Stdout) {
		return fmt.Errorf("requires interactive terminal")
	}

	commands, err := loadAllCommands()
	if err != nil {
		return err
	}

	if len(commands) == 0 {
		return fmt.Errorf("no commands found from any source")
	}

	options, err := fzf.ParseOptions(true, []string{
		"--height=~50%",
		"--layout=reverse",
		"--border",
		"--prompt", commandName + "> ",
		"--info=inline",
		"--no-multi",
		"--header", "Select a command (Enter to run, ESC to cancel)",
	})
	if err != nil {
		return fmt.Errorf("initialize fzf: %w", err)
	}

	input := make(chan string, len(commands))
	options.Input = input

	var selections []string
	options.Printer = func(str string) {
		if str != "" {
			selections = append(selections, str)
		}
	}

	go func() {
		for _, cmd := range commands {
			line := fmt.Sprintf("[%s] %s\t%s", cmd.Source.Name, cmd.Name, cmd.Description)
			input <- line
		}
		close(input)
	}()

	code, runErr := fzf.Run(options)
	if runErr != nil {
		return fmt.Errorf("run fzf: %w", runErr)
	}
	if code != fzf.ExitOk {
		return nil
	}
	if len(selections) == 0 {
		return nil
	}

	selected := selections[0]
	return executeSelection(selected)
}

func executeSelection(selection string) error {
	if tab := strings.Index(selection, "\t"); tab >= 0 {
		selection = selection[:tab]
	}

	selection = strings.TrimPrefix(selection, "[")
	closeBracket := strings.Index(selection, "]")
	if closeBracket == -1 {
		return fmt.Errorf("invalid selection format")
	}

	sourceName := selection[:closeBracket]
	cmdName := strings.TrimSpace(selection[closeBracket+1:])

	var src *CommandSource
	for i := range sources {
		if sources[i].Name == sourceName {
			src = &sources[i]
			break
		}
	}

	if src == nil {
		return fmt.Errorf("unknown source: %s", sourceName)
	}

	fmt.Printf("Running: %s %s\n", src.Binary, cmdName)

	cmd := exec.Command(src.Binary, cmdName)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

func runList(out io.Writer) error {
	commands, err := loadAllCommands()
	if err != nil {
		return err
	}

	currentSource := ""
	for _, cmd := range commands {
		if cmd.Source.Name != currentSource {
			if currentSource != "" {
				fmt.Fprintln(out)
			}
			fmt.Fprintf(out, "=== %s ===\n", cmd.Source.Name)
			currentSource = cmd.Source.Name
		}
		fmt.Fprintf(out, "  %s: %s\n", cmd.Name, cmd.Description)
	}

	return nil
}

func runSources(out io.Writer) error {
	fmt.Fprintln(out, "Configured command sources:")
	for _, src := range sources {
		exists := "✓"
		if _, err := os.Stat(src.Binary); err != nil {
			exists = "✗"
		}
		fmt.Fprintf(out, "  [%s] %s: %s\n", exists, src.Name, src.Binary)
	}

	return nil
}

func runDeploy(out io.Writer) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("failed to create bin directory: %w", err)
	}

	dest := filepath.Join(binDir, "unite")

	input, err := os.ReadFile(exe)
	if err != nil {
		return fmt.Errorf("failed to read executable: %w", err)
	}

	if err := os.WriteFile(dest, input, 0o755); err != nil {
		return fmt.Errorf("failed to write to %s: %w", dest, err)
	}

	fmt.Fprintf(out, "Installed %s to %s\n", commandName, dest)
	return nil
}
