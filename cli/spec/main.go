package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	version     = "0.3.0"
	commandName = "spec"
)

type section struct {
	Title   string
	Content string
}

type promptBundle struct {
	SpecPath string
	RepoRoot string
	Branch   string
	Title    string
	Sections map[string]section
	Prompt   string
}

type reviewPublishPayload struct {
	Title       string `json:"title,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Markdown    string `json:"markdown"`
	SourcePath  string `json:"sourcePath,omitempty"`
	GeneratedAt string `json:"generatedAt,omitempty"`
}

type publishEnvelope struct {
	Content string `json:"content"`
}

type publishResponse struct {
	Hash string `json:"hash"`
	URL  string `json:"url"`
}

type feedbackRecord struct {
	ID        string `json:"id"`
	Hash      string `json:"hash"`
	BlockID   string `json:"blockId"`
	Message   string `json:"message"`
	Author    string `json:"author"`
	CreatedAt string `json:"createdAt"`
}

type feedbackResponse struct {
	Hash     string           `json:"hash"`
	Feedback []feedbackRecord `json:"feedback"`
}

type stringListFlag []string

func (s *stringListFlag) String() string {
	return strings.Join(*s, ",")
}

func (s *stringListFlag) Set(value string) error {
	*s = append(*s, value)
	return nil
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
		case "exec":
			if err := runExec(os.Args[2:]); err != nil {
				fatal(err)
			}
			return
		case "review":
			if err := runReview(os.Args[2:]); err != nil {
				fatal(err)
			}
			return
		case "review-comments":
			if err := runReviewComments(os.Args[2:]); err != nil {
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
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
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

	bundle, err := buildPromptBundle(path, *mode)
	if err != nil {
		return err
	}

	copied := false
	if !*noClipboard {
		if err := copyToClipboard(bundle.Prompt); err != nil {
			if !*printPrompt {
				fmt.Println(bundle.Prompt)
			}
			return fmt.Errorf("copy to clipboard failed: %w", err)
		}
		copied = true
	}

	if *printPrompt || *noClipboard {
		fmt.Print(bundle.Prompt)
	}

	fmt.Fprintf(os.Stderr, "Generated prompt from %s (%d chars)\n", bundle.SpecPath, len(bundle.Prompt))
	if copied {
		fmt.Fprintln(os.Stderr, "Copied prompt to clipboard.")
	}

	return nil
}

func runExec(args []string) error {
	localArgs, codexArgs := splitArgsAtDoubleDash(args)

	fs := flag.NewFlagSet(commandName+" exec", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	inPathFlag := fs.String("in", "", "Path to markdown spec file")
	mode := fs.String("mode", "codex", "Prompt target mode (currently only: codex)")
	printPrompt := fs.Bool("stdout", false, "Print generated prompt to stdout before launching Codex")
	copyPrompt := fs.Bool("copy", false, "Also copy generated prompt to clipboard")
	dryRun := fs.Bool("dry-run", false, "Print Codex command and exit without launching")
	appendFile := stringListFlag{}
	appendText := stringListFlag{}
	fs.Var(&appendText, "append", "Additional instruction text to append (repeatable)")
	fs.Var(&appendFile, "append-file", "Path to file with additional instructions (repeatable)")

	if err := fs.Parse(normalizeArgs(localArgs)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if len(codexArgs) > 0 && isCodexSubcommand(codexArgs[0]) {
		return fmt.Errorf("spec exec expects Codex option flags after --, not subcommands (got %q)", codexArgs[0])
	}

	path := strings.TrimSpace(*inPathFlag)
	remaining := fs.Args()
	if path == "" && len(remaining) > 0 {
		path = strings.TrimSpace(remaining[0])
		remaining = remaining[1:]
	}
	if path == "" {
		printUsage()
		return errors.New("missing markdown spec file path")
	}

	if len(remaining) > 0 {
		appendText = append(appendText, strings.Join(remaining, " "))
	}

	bundle, err := buildPromptBundle(path, *mode)
	if err != nil {
		return err
	}

	extraBlocks := make([]string, 0, len(appendText)+len(appendFile))
	for _, t := range appendText {
		trimmed := strings.TrimSpace(t)
		if trimmed != "" {
			extraBlocks = append(extraBlocks, trimmed)
		}
	}
	for _, filePath := range appendFile {
		contentBytes, err := os.ReadFile(strings.TrimSpace(filePath))
		if err != nil {
			return fmt.Errorf("read append-file %q: %w", filePath, err)
		}
		trimmed := strings.TrimSpace(string(contentBytes))
		if trimmed != "" {
			extraBlocks = append(extraBlocks, trimmed)
		}
	}

	prompt := appendAdditionalInstructions(bundle.Prompt, extraBlocks)

	if *copyPrompt {
		if err := copyToClipboard(prompt); err != nil {
			return fmt.Errorf("copy to clipboard failed: %w", err)
		}
	}
	if *printPrompt {
		fmt.Print(prompt)
	}

	launchArgs := make([]string, 0, len(codexArgs)+3)
	if bundle.RepoRoot != "" && !hasCodexCdArg(codexArgs) {
		launchArgs = append(launchArgs, "-C", bundle.RepoRoot)
	}
	if !hasCodexPermissionArg(codexArgs) {
		launchArgs = append(launchArgs, "--dangerously-bypass-approvals-and-sandbox")
	}
	launchArgs = append(launchArgs, codexArgs...)
	launchArgs = append(launchArgs, prompt)

	if *dryRun {
		fmt.Fprintf(os.Stderr, "Dry run: %s\n", formatCommand("codex", launchArgs))
		fmt.Fprintf(os.Stderr, "Prompt source: %s (%d chars)\n", bundle.SpecPath, len(prompt))
		return nil
	}

	if _, err := exec.LookPath("codex"); err != nil {
		return errors.New("codex binary not found in PATH")
	}

	fmt.Fprintf(os.Stderr, "Launching Codex from spec: %s\n", bundle.SpecPath)
	cmd := exec.Command("codex", launchArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to launch codex: %w", err)
	}

	return nil
}

func runReview(args []string) error {
	fs := flag.NewFlagSet(commandName+" review", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	inPathFlag := fs.String("in", "", "Path to markdown spec file")
	apiOrigin := fs.String("api-origin", defaultReviewOrigin(), "Unhash API origin")
	titleOverride := fs.String("title", "", "Override page title")
	noClipboard := fs.Bool("no-clipboard", false, "Do not copy review link to clipboard")
	openPage := fs.Bool("open", false, "Open review URL in browser")
	dryRun := fs.Bool("dry-run", false, "Print publish payload and exit without uploading")
	printPayload := fs.Bool("stdout", false, "Print publish payload to stdout")

	if err := fs.Parse(normalizeArgs(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
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
		return fmt.Errorf("resolve spec path: %w", err)
	}
	contentBytes, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("read spec file %s: %w", absPath, err)
	}
	markdown := string(contentBytes)
	if strings.TrimSpace(markdown) == "" {
		return fmt.Errorf("spec file %s is empty", absPath)
	}

	title := strings.TrimSpace(*titleOverride)
	if title == "" {
		title = parseTitle(markdown)
	}
	payload := reviewPublishPayload{
		Title:       title,
		Summary:     extractSummary(markdown),
		Markdown:    markdown,
		SourcePath:  absPath,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}

	payloadJSON, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}

	if *printPayload || *dryRun {
		fmt.Printf("%s\n", payloadJSON)
	}
	if *dryRun {
		fmt.Fprintf(os.Stderr, "Dry run: payload prepared for %s/api/publish\n", strings.TrimRight(*apiOrigin, "/"))
		return nil
	}

	response, err := publishReview(*apiOrigin, payloadJSON)
	if err != nil {
		return err
	}

	hash := strings.TrimSpace(response.Hash)
	reviewURL := strings.TrimSpace(response.URL)
	if hash == "" {
		return errors.New("publish response missing hash")
	}
	if reviewURL == "" {
		reviewURL = strings.TrimRight(*apiOrigin, "/") + "/" + hash
	}

	shareURL := canonicalShareURL(hash, reviewURL)
	feedbackURL := strings.TrimRight(*apiOrigin, "/") + "/api/hash/" + hash + "/feedback"

	if !*noClipboard {
		if err := copyToClipboard(shareURL); err != nil {
			return fmt.Errorf("copy review URL to clipboard failed: %w", err)
		}
	}

	fmt.Printf("%s\n", hash)
	fmt.Printf("%s\n", shareURL)
	if reviewURL != shareURL {
		fmt.Printf("Review URL: %s\n", reviewURL)
	}
	fmt.Printf("Feedback API: %s\n", feedbackURL)
	if !*noClipboard {
		fmt.Fprintln(os.Stderr, "Copied review URL to clipboard.")
	}

	if *openPage {
		if err := openBrowser(reviewURL); err != nil {
			return fmt.Errorf("open browser: %w", err)
		}
	}

	return nil
}

func runReviewComments(args []string) error {
	fs := flag.NewFlagSet(commandName+" review-comments", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	hashFlag := fs.String("hash", "", "Hash or unhash URL")
	apiOrigin := fs.String("api-origin", defaultReviewOrigin(), "Unhash API origin")
	asJSON := fs.Bool("json", false, "Print raw JSON instead of markdown summary")

	if err := fs.Parse(normalizeArgs(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	selector := strings.TrimSpace(*hashFlag)
	if selector == "" {
		rest := fs.Args()
		if len(rest) > 0 {
			selector = strings.TrimSpace(rest[0])
		}
	}
	if selector == "" {
		printUsage()
		return errors.New("missing hash or review URL")
	}

	hash := normalizeHashSelector(selector)
	if hash == "" {
		return fmt.Errorf("could not parse hash from %q", selector)
	}

	comments, err := fetchReviewComments(*apiOrigin, hash)
	if err != nil {
		return err
	}

	if *asJSON {
		out, err := json.MarshalIndent(comments, "", "  ")
		if err != nil {
			return fmt.Errorf("encode feedback json: %w", err)
		}
		fmt.Printf("%s\n", out)
		return nil
	}

	fmt.Print(renderFeedbackMarkdown(hash, comments))
	return nil
}

func defaultReviewOrigin() string {
	origin := strings.TrimSpace(os.Getenv("UNHASH_API_ORIGIN"))
	if origin == "" {
		origin = "https://unhash.sh"
	}
	return strings.TrimRight(origin, "/")
}

func extractSummary(markdown string) string {
	lines := strings.Split(markdown, "\n")
	inCodeFence := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			inCodeFence = !inCodeFence
			continue
		}
		if inCodeFence {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		return trimmed
	}

	return "Spec review payload"
}

func publishReview(apiOrigin string, payloadJSON []byte) (publishResponse, error) {
	origin := strings.TrimRight(strings.TrimSpace(apiOrigin), "/")
	if origin == "" {
		return publishResponse{}, errors.New("api origin is required")
	}

	envelopeBytes, err := json.Marshal(publishEnvelope{
		Content: string(payloadJSON),
	})
	if err != nil {
		return publishResponse{}, fmt.Errorf("encode publish envelope: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, origin+"/api/publish", bytes.NewReader(envelopeBytes))
	if err != nil {
		return publishResponse{}, fmt.Errorf("create publish request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return publishResponse{}, fmt.Errorf("failed to reach %s/api/publish: %w", origin, err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return publishResponse{}, fmt.Errorf("read publish response: %w", err)
	}

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return publishResponse{}, fmt.Errorf("publish failed (%d): %s", res.StatusCode, strings.TrimSpace(string(body)))
	}

	var response publishResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return publishResponse{}, fmt.Errorf("decode publish response: %w", err)
	}
	if strings.TrimSpace(response.Hash) == "" {
		return publishResponse{}, fmt.Errorf("publish response missing hash: %s", strings.TrimSpace(string(body)))
	}
	if strings.TrimSpace(response.URL) == "" {
		response.URL = origin + "/" + strings.TrimSpace(response.Hash)
	}

	return response, nil
}

func canonicalShareURL(hash, reviewURL string) string {
	trimmedHash := strings.TrimSpace(hash)
	trimmedURL := strings.TrimSpace(reviewURL)
	if trimmedHash == "" {
		return trimmedURL
	}

	parsed, err := url.Parse(trimmedURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		if trimmedURL == "" {
			return "https://unhash.sh/" + trimmedHash
		}
		if strings.HasSuffix(trimmedURL, "/"+trimmedHash) {
			return trimmedURL
		}
		return strings.TrimRight(trimmedURL, "/") + "/" + trimmedHash
	}

	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path = "/" + trimmedHash
	return parsed.String()
}

func openBrowser(target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return errors.New("missing URL")
	}

	var commands [][]string
	switch runtime.GOOS {
	case "darwin":
		commands = append(commands, []string{"open", target})
	case "linux":
		commands = append(commands, []string{"xdg-open", target})
	case "windows":
		commands = append(commands, []string{"rundll32", "url.dll,FileProtocolHandler", target})
	}
	commands = append(commands,
		[]string{"open", target},
		[]string{"xdg-open", target},
	)

	for _, parts := range commands {
		bin := parts[0]
		if _, err := exec.LookPath(bin); err != nil {
			continue
		}
		cmd := exec.Command(bin, parts[1:]...)
		if err := cmd.Start(); err != nil {
			continue
		}
		return nil
	}

	return errors.New("no browser opener found (tried open, xdg-open, rundll32)")
}

func normalizeHashSelector(selector string) string {
	token := strings.TrimSpace(selector)
	if token == "" {
		return ""
	}

	if parsed, err := url.Parse(token); err == nil && parsed.Host != "" {
		if q := strings.TrimSpace(parsed.Query().Get("hash")); isLikelyHash(q) {
			return q
		}
		path := strings.Trim(parsed.Path, "/")
		if path == "" {
			return ""
		}
		parts := strings.Split(path, "/")
		for i := 0; i < len(parts); i++ {
			if parts[i] == "hash" && i+1 < len(parts) && isLikelyHash(parts[i+1]) {
				return parts[i+1]
			}
		}
		last := parts[len(parts)-1]
		if isLikelyHash(last) {
			return last
		}
		return ""
	}

	token = strings.Trim(token, "/")
	if strings.Contains(token, "/") {
		parts := strings.Split(token, "/")
		token = parts[len(parts)-1]
	}
	if !isLikelyHash(token) {
		return ""
	}
	return token
}

func isLikelyHash(value string) bool {
	if value == "" {
		return false
	}
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

func fetchReviewComments(apiOrigin, hash string) ([]feedbackRecord, error) {
	origin := strings.TrimRight(strings.TrimSpace(apiOrigin), "/")
	if origin == "" {
		return nil, errors.New("api origin is required")
	}
	if hash == "" {
		return nil, errors.New("hash is required")
	}

	endpoint := origin + "/api/hash/" + url.PathEscape(hash) + "/feedback"
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create feedback request: %w", err)
	}
	req.Header.Set("accept", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to reach %s: %w", endpoint, err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("read feedback response: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("feedback request failed (%d): %s", res.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload feedbackResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode feedback response: %w", err)
	}

	comments := make([]feedbackRecord, 0, len(payload.Feedback))
	for _, item := range payload.Feedback {
		if strings.TrimSpace(item.Message) == "" {
			continue
		}
		comments = append(comments, item)
	}
	sort.Slice(comments, func(i, j int) bool {
		if comments[i].BlockID != comments[j].BlockID {
			return comments[i].BlockID < comments[j].BlockID
		}
		if comments[i].CreatedAt != comments[j].CreatedAt {
			return comments[i].CreatedAt < comments[j].CreatedAt
		}
		return comments[i].ID < comments[j].ID
	})

	return comments, nil
}

func renderFeedbackMarkdown(hash string, comments []feedbackRecord) string {
	var b strings.Builder
	trimmedHash := strings.TrimSpace(hash)
	if trimmedHash == "" {
		trimmedHash = "unknown-hash"
	}

	fmt.Fprintf(&b, "# Review Feedback for %s\n\n", trimmedHash)
	fmt.Fprintf(&b, "Total comments: %d\n\n", len(comments))

	if len(comments) == 0 {
		b.WriteString("No inline feedback yet.\n")
		return b.String()
	}

	byBlock := make(map[string][]feedbackRecord)
	blockIDs := make([]string, 0, len(comments))
	for _, comment := range comments {
		blockID := strings.TrimSpace(comment.BlockID)
		if blockID == "" {
			blockID = "general"
		}
		if _, exists := byBlock[blockID]; !exists {
			blockIDs = append(blockIDs, blockID)
		}
		byBlock[blockID] = append(byBlock[blockID], comment)
	}
	sort.Strings(blockIDs)

	for _, blockID := range blockIDs {
		fmt.Fprintf(&b, "## %s\n\n", blockID)
		for _, comment := range byBlock[blockID] {
			author := strings.TrimSpace(comment.Author)
			if author == "" {
				author = "anonymous"
			}

			ts := strings.TrimSpace(comment.CreatedAt)
			if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
				ts = parsed.UTC().Format(time.RFC3339)
			}
			message := strings.TrimSpace(comment.Message)
			message = strings.ReplaceAll(message, "\n", " ")
			fmt.Fprintf(&b, "- %s (%s): %s\n", author, ts, message)
		}
		b.WriteString("\n")
	}

	return b.String()
}

func splitArgsAtDoubleDash(args []string) ([]string, []string) {
	for i, arg := range args {
		if arg == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

func appendAdditionalInstructions(prompt string, blocks []string) string {
	if len(blocks) == 0 {
		return prompt
	}

	var b strings.Builder
	b.WriteString(strings.TrimSpace(prompt))
	b.WriteString("\n\nAdditional operator instructions:\n")
	for i, block := range blocks {
		trimmed := strings.TrimSpace(block)
		if trimmed == "" {
			continue
		}
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(fmt.Sprintf("%d) ", i+1))
		b.WriteString(trimmed)
		b.WriteString("\n")
	}

	return strings.TrimSpace(b.String()) + "\n"
}

func hasCodexCdArg(args []string) bool {
	for _, arg := range args {
		if arg == "-C" || arg == "--cd" || strings.HasPrefix(arg, "--cd=") {
			return true
		}
	}
	return false
}

func hasCodexPermissionArg(args []string) bool {
	for _, arg := range args {
		if arg == "--dangerously-bypass-approvals-and-sandbox" ||
			arg == "--full-auto" ||
			arg == "-a" ||
			arg == "--ask-for-approval" ||
			strings.HasPrefix(arg, "--ask-for-approval=") ||
			arg == "-s" ||
			arg == "--sandbox" ||
			strings.HasPrefix(arg, "--sandbox=") {
			return true
		}
	}
	return false
}

func isCodexSubcommand(arg string) bool {
	switch arg {
	case "exec", "review", "login", "logout", "mcp", "mcp-server", "app-server", "app", "completion", "sandbox", "debug", "apply", "resume", "fork", "cloud", "features", "help":
		return true
	default:
		return false
	}
}

func formatCommand(bin string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, shellQuote(bin))
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuote(arg string) string {
	if arg == "" {
		return "''"
	}
	if !strings.ContainsAny(arg, " \t\n\"'`$&|;()<>[]{}!*?\\") {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
}

func buildPromptBundle(path, mode string) (promptBundle, error) {
	if mode != "codex" {
		return promptBundle{}, fmt.Errorf("unsupported mode %q (supported: codex)", mode)
	}

	absPath, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return promptBundle{}, fmt.Errorf("resolve absolute path: %w", err)
	}

	contentBytes, err := os.ReadFile(absPath)
	if err != nil {
		return promptBundle{}, fmt.Errorf("read spec file %s: %w", absPath, err)
	}
	content := string(contentBytes)
	if strings.TrimSpace(content) == "" {
		return promptBundle{}, fmt.Errorf("spec file %s is empty", absPath)
	}

	repoRoot := findGitRoot(filepath.Dir(absPath))
	branch := currentBranch(repoRoot)
	sections := parseSections(content)
	title := parseTitle(content)

	prompt := buildCodexPrompt(absPath, repoRoot, branch, title, sections)

	return promptBundle{
		SpecPath: absPath,
		RepoRoot: repoRoot,
		Branch:   branch,
		Title:    title,
		Sections: sections,
		Prompt:   prompt,
	}, nil
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
	if strings.HasPrefix(arg, "--api-origin=") || strings.HasPrefix(arg, "--title=") || strings.HasPrefix(arg, "--hash=") {
		return false
	}
	if strings.HasPrefix(arg, "--append=") || strings.HasPrefix(arg, "--append-file=") {
		return false
	}

	switch arg {
	case "-in", "--in", "-mode", "--mode", "--append", "--append-file", "--api-origin", "--title", "--hash":
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

	specRef := specRefForPrompt(specPath, repoRoot)

	writeLine("You are Codex in execution mode. Implement exactly from the provided spec with high rigor and minimal drift.")
	writeLine("")
	writeLine("Execution context:")
	if repoRoot != "" {
		writeLine(fmt.Sprintf("- Work in: %s", repoRoot))
	}
	if branch != "" {
		writeLine(fmt.Sprintf("- Branch: %s", branch))
	}
	writeLine(fmt.Sprintf("- Task spec: %s", specRef))
	if title != "" {
		writeLine(fmt.Sprintf("- Spec title: %s", title))
	}
	writeLine("")
	writeLine("Primary objective:")
	writeLine("- Execute the spec end-to-end with full type/runtime/test integrity.")
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
	writeLine("4. Commit hashes")
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

func specRefForPrompt(specPath, repoRoot string) string {
	if repoRoot == "" {
		return specPath
	}
	rel, err := filepath.Rel(repoRoot, specPath)
	if err != nil {
		return specPath
	}
	if strings.HasPrefix(rel, "..") {
		return specPath
	}
	if rel == "." {
		return specPath
	}
	return filepath.ToSlash(rel)
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
	fmt.Printf("%s - Generate Codex prompts and publish spec reviews to unhash\n\n", commandName)
	fmt.Println("Usage:")
	fmt.Printf("  %s <spec.md>\n", commandName)
	fmt.Printf("  %s -in <spec.md> [--stdout] [--no-clipboard]\n", commandName)
	fmt.Printf("  %s exec <spec.md> [--append \"...\"] [--append-file path] [--copy] [--stdout] [--dry-run] [-- <codex args...>]\n", commandName)
	fmt.Printf("  %s review <spec.md> [--api-origin https://unhash.sh] [--title \"...\"] [--open] [--no-clipboard]\n", commandName)
	fmt.Printf("  %s review-comments <hash|url> [--json] [--api-origin https://unhash.sh]\n", commandName)
	fmt.Printf("  %s deploy\n", commandName)
	fmt.Printf("  %s version\n", commandName)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "Error:", err)
	os.Exit(1)
}
