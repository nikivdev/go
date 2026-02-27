package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	version     = "0.1.0"
	commandName = "spec"
)

type section struct {
	Title   string
	Content string
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "--version", "-v":
			fmt.Println(version)
			return
		case "deploy":
			if err := runDeploy(); err != nil {
				fatal(err)
			}
			return
		case "help", "--help", "-h":
			printUsage()
			return
		}
	}

	if err := runPrompt(os.Args[1:]); err != nil {
		fatal(err)
	}
}

func runPrompt(args []string) error {
	fs := flag.NewFlagSet(commandName, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	inPathFlag := fs.String("in", "", "Path to markdown spec file")
	printPrompt := fs.Bool("stdout", false, "Also print generated prompt to stdout")
	noClipboard := fs.Bool("no-clipboard", false, "Do not copy generated prompt to clipboard")
	mode := fs.String("mode", "codex", "Prompt target mode (currently only: codex)")

	if err := fs.Parse(normalizeArgs(args)); err != nil {
		return err
	}

	if *mode != "codex" {
		return fmt.Errorf("unsupported mode %q (supported: codex)", *mode)
	}

	path := strings.TrimSpace(*inPathFlag)
	if path == "" {
		rest := fs.Args()
		if len(rest) > 0 {
			path = strings.TrimSpace(rest[0])
		}
	}
	if path == "" {
		printUsage()
		return errors.New("missing markdown spec file path")
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve absolute path: %w", err)
	}

	contentBytes, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("read spec file %s: %w", absPath, err)
	}
	content := string(contentBytes)
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("spec file %s is empty", absPath)
	}

	repoRoot := findGitRoot(filepath.Dir(absPath))
	branch := currentBranch(repoRoot)
	sections := parseSections(content)
	title := parseTitle(content)

	prompt := buildCodexPrompt(absPath, repoRoot, branch, title, sections)

	copied := false
	if !*noClipboard {
		if err := copyToClipboard(prompt); err != nil {
			if !*printPrompt {
				fmt.Println(prompt)
			}
			return fmt.Errorf("copy to clipboard failed: %w", err)
		}
		copied = true
	}

	if *printPrompt || *noClipboard {
		fmt.Print(prompt)
	}

	fmt.Fprintf(os.Stderr, "Generated prompt from %s (%d chars)\n", absPath, len(prompt))
	if copied {
		fmt.Fprintln(os.Stderr, "Copied prompt to clipboard.")
	}

	return nil
}

func normalizeArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}

	options := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positionals = append(positionals, arg)
			continue
		}

		options = append(options, arg)

		if optionRequiresValue(arg) && i+1 < len(args) {
			i++
			options = append(options, args[i])
		}
	}

	return append(options, positionals...)
}

func optionRequiresValue(arg string) bool {
	if strings.HasPrefix(arg, "-in=") || strings.HasPrefix(arg, "--in=") {
		return false
	}
	if strings.HasPrefix(arg, "-mode=") || strings.HasPrefix(arg, "--mode=") {
		return false
	}

	switch arg {
	case "-in", "--in", "-mode", "--mode":
		return true
	default:
		return false
	}
}

func buildCodexPrompt(specPath, repoRoot, branch, title string, sections map[string]section) string {
	var b strings.Builder

	writeLine := func(s string) {
		b.WriteString(s)
		b.WriteByte('\n')
	}

	writeLine("You are Codex in execution mode. Implement exactly from the provided spec with high rigor and minimal drift.")
	writeLine("")
	writeLine("Execution context:")
	writeLine(fmt.Sprintf("- Spec file: %s", specPath))
	if repoRoot != "" {
		writeLine(fmt.Sprintf("- Repo root: %s", repoRoot))
	}
	if branch != "" {
		writeLine(fmt.Sprintf("- Target branch: %s", branch))
	}
	if title != "" {
		writeLine(fmt.Sprintf("- Spec title: %s", title))
	}
	writeLine("")
	writeLine("Primary instructions:")
	writeLine("1. Read the spec file completely before editing any code.")
	writeLine("2. Treat the spec as source of truth for scope, order, and acceptance criteria.")
	writeLine("3. Do not expand scope beyond the spec unless required to satisfy an explicit invariant.")
	writeLine("4. When spec and code disagree, prioritize spec intent and record the exact deviation in your final summary.")
	writeLine("5. Keep changes minimal, coherent, and reviewer-friendly.")
	writeLine("6. Run verification commands/tests defined by the spec and report results succinctly.")
	writeLine("")
	writeLine("Output contract for your final response:")
	writeLine("1. What changed (grouped by file)")
	writeLine("2. Invariants checklist (pass/fail)")
	writeLine("3. Exact verification commands run + concise outputs")
	writeLine("4. Commits created")
	writeLine("5. Residual risks / follow-ups")
	writeLine("")

	ordered := prioritizedSections(sections)
	if len(ordered) > 0 {
		writeLine("Key spec excerpts (authoritative):")
		for _, s := range ordered {
			writeLine("")
			writeLine(fmt.Sprintf("## %s", s.Title))
			writeLine(strings.TrimSpace(s.Content))
		}
		writeLine("")
	}

	writeLine("Mandatory execution gates:")
	writeLine("- Follow the spec's implementation order and file manifest.")
	writeLine("- Preserve all non-negotiable invariants in the spec.")
	writeLine("- Execute required post-edit assertions and tests from the spec.")
	writeLine("- If blocked by true ambiguity, ask exactly one concise question and stop.")

	return strings.TrimSpace(b.String()) + "\n"
}

func parseTitle(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

func parseSections(content string) map[string]section {
	result := make(map[string]section)
	lines := strings.Split(content, "\n")
	re := regexp.MustCompile(`^##\s+(.+)$`)

	var currentTitle string
	var currentBody bytes.Buffer
	flush := func() {
		if currentTitle == "" {
			return
		}
		result[currentTitle] = section{Title: currentTitle, Content: strings.TrimSpace(currentBody.String())}
		currentBody.Reset()
	}

	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		if m := re.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			flush()
			currentTitle = strings.TrimSpace(m[1])
			continue
		}

		if currentTitle != "" {
			currentBody.WriteString(line)
			currentBody.WriteByte('\n')
		}
	}
	flush()

	return result
}

func prioritizedSections(sections map[string]section) []section {
	if len(sections) == 0 {
		return nil
	}

	want := []string{
		"Execution Playbook",
		"Implementation Sequence",
		"File Manifest",
		"Ambiguity Defaults",
		"Non-Negotiable Invariants",
		"Definition of Done",
		"Reviewer Checklist",
		"Handoff Deliverables",
	}

	used := make(map[string]bool)
	out := make([]section, 0, len(want))

	for _, keyword := range want {
		if s, ok := findSectionByKeyword(sections, keyword); ok {
			out = append(out, clipSection(s, 7000))
			used[s.Title] = true
		}
	}

	if len(out) == 0 {
		all := make([]string, 0, len(sections))
		for title := range sections {
			all = append(all, title)
		}
		sort.Strings(all)
		for _, title := range all {
			out = append(out, clipSection(sections[title], 3500))
			if len(out) >= 3 {
				break
			}
		}
	}

	return out
}

func findSectionByKeyword(sections map[string]section, keyword string) (section, bool) {
	needle := strings.ToLower(keyword)
	for title, s := range sections {
		t := strings.ToLower(title)
		if strings.Contains(t, needle) {
			return s, true
		}
	}
	return section{}, false
}

func clipSection(s section, maxRunes int) section {
	r := []rune(strings.TrimSpace(s.Content))
	if len(r) <= maxRunes {
		return s
	}
	clipped := strings.TrimSpace(string(r[:maxRunes]))
	clipped += "\n\n[...truncated by spec CLI for prompt size; source file is authoritative...]"
	return section{Title: s.Title, Content: clipped}
}

func findGitRoot(start string) string {
	dir := start
	for {
		if dir == "" || dir == "/" || dir == "." {
			return ""
		}
		if fileExists(filepath.Join(dir, ".git")) {
			return dir
		}
		next := filepath.Dir(dir)
		if next == dir {
			return ""
		}
		dir = next
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func currentBranch(repoRoot string) string {
	if repoRoot == "" {
		return ""
	}
	cmd := exec.Command("git", "-C", repoRoot, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	branch := strings.TrimSpace(string(out))
	if branch == "HEAD" {
		return ""
	}
	return branch
}

func copyToClipboard(text string) error {
	candidates := [][]string{
		{"pbcopy"},
		{"wl-copy"},
		{"xclip", "-selection", "clipboard"},
		{"xsel", "--clipboard", "--input"},
		{"clip"},
	}

	for _, candidate := range candidates {
		bin := candidate[0]
		if _, err := exec.LookPath(bin); err != nil {
			continue
		}

		cmd := exec.Command(bin, candidate[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			return nil
		}
	}

	return errors.New("no clipboard command available (tried pbcopy, wl-copy, xclip, xsel, clip)")
}

func runDeploy() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	dst := filepath.Join(home, "bin", commandName)

	cmd := exec.Command("go", "build", "-o", dst, ".")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build %s: %w", commandName, err)
	}

	fmt.Printf("Installed: %s\n", dst)
	return nil
}

func printUsage() {
	fmt.Printf("%s - Generate Codex-optimized execution prompts from Markdown specs\n\n", commandName)
	fmt.Println("Usage:")
	fmt.Printf("  %s <spec.md>\n", commandName)
	fmt.Printf("  %s -in <spec.md> [--stdout] [--no-clipboard]\n", commandName)
	fmt.Printf("  %s deploy\n", commandName)
	fmt.Printf("  %s version\n", commandName)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "Error:", err)
	os.Exit(1)
}
