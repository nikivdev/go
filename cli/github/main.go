package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/dzonerzy/go-snap/snap"
)

const (
	version     = "1.0.0"
	commandName = "ghx"
)

func main() {
	// Handle default case: if first arg looks like a PR ref, run diff
	if len(os.Args) > 1 && looksLikePRRef(os.Args[1]) {
		if err := runDiffDirect(os.Args[1], os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	app := snap.New(commandName, "GitHub CLI for PR operations").
		Version(version)

	app.Command("diff", "Get full diff of a GitHub PR").
		Action(runDiff)

	app.Command("version", "Show version").
		Action(func(ctx *snap.Context) error {
			fmt.Fprintln(ctx.Stdout(), version)
			return nil
		})

	app.Command("deploy", "Build and install ghx to ~/bin").
		Action(runDeploy)

	if len(os.Args) == 1 {
		showHelpDirect()
		return
	}

	app.RunAndExit()
}

func showHelpDirect() {
	fmt.Printf("%s - GitHub CLI for PR operations\n\n", commandName)
	fmt.Println("Usage:")
	fmt.Printf("  %s <pr-url>                    Get full diff of a PR\n", commandName)
	fmt.Printf("  %s <pr-url> --no-comments      Get diff without comments/reviews\n", commandName)
	fmt.Printf("  %s diff <pr-url>               Get full diff of a PR\n", commandName)
	fmt.Printf("  %s deploy                      Build and install to ~/bin\n", commandName)
	fmt.Printf("  %s version                     Show version\n", commandName)
	fmt.Println()
	fmt.Println("PR reference formats:")
	fmt.Println("  https://github.com/owner/repo/pull/123")
	fmt.Println("  owner/repo#123")
}

func runDiffDirect(ref string, extraArgs []string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fmt.Errorf("PR reference cannot be empty")
	}

	includeComments := true
	for _, arg := range extraArgs {
		if strings.TrimSpace(arg) == "--no-comments" {
			includeComments = false
		}
	}

	owner, repo, prNumber, err := parsePRRef(ref)
	if err != nil {
		return err
	}

	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("gh CLI not found in PATH: %w", err)
	}

	repoFull := fmt.Sprintf("%s/%s", owner, repo)
	prRef := fmt.Sprintf("%d", prNumber)

	var out bytes.Buffer

	out.WriteString(fmt.Sprintf("# Pull Request: %s#%d\n\n", repoFull, prNumber))

	prInfo, err := getPRInfo(repoFull, prRef)
	if err != nil {
		return err
	}

	out.WriteString(fmt.Sprintf("## %s\n\n", prInfo.Title))
	out.WriteString(fmt.Sprintf("**Author:** %s\n", prInfo.Author.Login))
	out.WriteString(fmt.Sprintf("**State:** %s\n", prInfo.State))
	out.WriteString(fmt.Sprintf("**Base:** %s ← **Head:** %s\n", prInfo.BaseRefName, prInfo.HeadRefName))
	out.WriteString(fmt.Sprintf("**Stats:** +%d -%d across %d files\n\n", prInfo.Additions, prInfo.Deletions, prInfo.ChangedFiles))

	if prInfo.Body != "" {
		out.WriteString("## Description\n\n")
		out.WriteString(prInfo.Body)
		out.WriteString("\n\n")
	}

	if includeComments {
		if comments, err := getPRComments(repoFull, prRef); err == nil && len(comments) > 0 {
			out.WriteString("## Comments\n\n")
			for i, c := range comments {
				out.WriteString(fmt.Sprintf("### Comment %d by %s\n\n", i+1, c.Author.Login))
				out.WriteString(c.Body)
				out.WriteString("\n\n")
			}
		}

		if reviews, err := getPRReviews(repoFull, prRef); err == nil && len(reviews) > 0 {
			out.WriteString("## Reviews\n\n")
			for i, r := range reviews {
				if r.Body == "" {
					continue
				}
				out.WriteString(fmt.Sprintf("### Review %d by %s (%s)\n\n", i+1, r.Author.Login, r.State))
				out.WriteString(r.Body)
				out.WriteString("\n\n")
			}
		}
	}

	out.WriteString("## Diff\n\n")
	out.WriteString("```diff\n")

	diffOutput, err := getPRDiff(repoFull, prRef)
	if err != nil {
		return err
	}

	out.Write(diffOutput)
	out.WriteString("```\n")

	fmt.Print(out.String())
	return nil
}

func looksLikePRRef(s string) bool {
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return strings.Contains(s, "/pull/")
	}
	return strings.Contains(s, "#")
}

func runDiff(ctx *snap.Context) error {
	if ctx.NArgs() < 1 {
		fmt.Fprintf(ctx.Stderr(), "Usage: %s diff <pr-url> [--no-comments]\n", commandName)
		return fmt.Errorf("expected at least 1 argument")
	}
	return runDiffDirect(ctx.Arg(0), ctx.Args()[1:])
}

func runDeploy(ctx *snap.Context) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home dir: %w", err)
	}
	dest := home + "/bin/" + commandName

	cmd := exec.Command("go", "build", "-o", dest, ".")
	cmd.Stdout = ctx.Stdout()
	cmd.Stderr = ctx.Stderr()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build: %w", err)
	}

	fmt.Fprintf(ctx.Stdout(), "Installed: %s\n", dest)
	return nil
}

type prInfoResponse struct {
	Title        string         `json:"title"`
	Body         string         `json:"body"`
	Author       authorResponse `json:"author"`
	State        string         `json:"state"`
	BaseRefName  string         `json:"baseRefName"`
	HeadRefName  string         `json:"headRefName"`
	Additions    int            `json:"additions"`
	Deletions    int            `json:"deletions"`
	ChangedFiles int            `json:"changedFiles"`
}

type authorResponse struct {
	Login string `json:"login"`
}

type commentResponse struct {
	Author authorResponse `json:"author"`
	Body   string         `json:"body"`
}

type reviewResponse struct {
	Author authorResponse `json:"author"`
	Body   string         `json:"body"`
	State  string         `json:"state"`
}

func getPRInfo(repo, prRef string) (*prInfoResponse, error) {
	cmd := exec.Command("gh", "pr", "view", prRef, "--repo", repo, "--json",
		"title,body,author,state,baseRefName,headRefName,additions,deletions,changedFiles")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh pr view: %w", err)
	}

	var info prInfoResponse
	if err := json.Unmarshal(output, &info); err != nil {
		return nil, fmt.Errorf("parse PR info: %w", err)
	}
	return &info, nil
}

func getPRComments(repo, prRef string) ([]commentResponse, error) {
	cmd := exec.Command("gh", "pr", "view", prRef, "--repo", repo, "--json", "comments")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var resp struct {
		Comments []commentResponse `json:"comments"`
	}
	if err := json.Unmarshal(output, &resp); err != nil {
		return nil, err
	}
	return resp.Comments, nil
}

func getPRReviews(repo, prRef string) ([]reviewResponse, error) {
	cmd := exec.Command("gh", "pr", "view", prRef, "--repo", repo, "--json", "reviews")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var resp struct {
		Reviews []reviewResponse `json:"reviews"`
	}
	if err := json.Unmarshal(output, &resp); err != nil {
		return nil, err
	}
	return resp.Reviews, nil
}

func getPRDiff(repo, prRef string) ([]byte, error) {
	cmd := exec.Command("gh", "pr", "diff", prRef, "--repo", repo)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh pr diff: %w", err)
	}
	return output, nil
}

func parsePRRef(input string) (string, string, int, error) {
	candidate := strings.TrimSpace(strings.TrimSuffix(input, "/"))
	if candidate == "" {
		return "", "", 0, fmt.Errorf("PR reference cannot be empty")
	}

	if strings.HasPrefix(candidate, "http://") || strings.HasPrefix(candidate, "https://") {
		u, err := url.Parse(candidate)
		if err != nil {
			return "", "", 0, fmt.Errorf("parse url %q: %w", input, err)
		}
		if !strings.EqualFold(u.Host, "github.com") {
			return "", "", 0, fmt.Errorf("expected github.com host, got %s", u.Host)
		}
		segments := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(segments) < 4 {
			return "", "", 0, fmt.Errorf("expected GitHub PR URL, got %q", input)
		}
		owner := segments[0]
		repo := strings.TrimSuffix(segments[1], ".git")
		number := 0
		for i := 2; i < len(segments); i++ {
			if segments[i] == "pull" || segments[i] == "pulls" {
				if i+1 < len(segments) {
					if n, err := strconv.Atoi(strings.TrimSpace(segments[i+1])); err == nil && n > 0 {
						number = n
						break
					}
				}
			}
		}
		if owner == "" || repo == "" || number == 0 {
			return "", "", 0, fmt.Errorf("unable to parse PR from %q", input)
		}
		return owner, repo, number, nil
	}

	if hash := strings.Index(candidate, "#"); hash > 0 {
		repoPart := strings.TrimSpace(candidate[:hash])
		numberPart := strings.TrimSpace(candidate[hash+1:])
		parts := strings.Split(repoPart, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", "", 0, fmt.Errorf("invalid repo format %q, expected owner/repo", repoPart)
		}
		number, err := strconv.Atoi(numberPart)
		if err != nil || number <= 0 {
			return "", "", 0, fmt.Errorf("invalid PR number %q", numberPart)
		}
		return parts[0], parts[1], number, nil
	}

	return "", "", 0, fmt.Errorf("unrecognized PR reference format: %q", input)
}
