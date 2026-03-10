# spec

Generate a Codex-optimized execution prompt from a Markdown spec, copy it to clipboard, launch Codex with that prompt prefilled, or publish the spec for inline review on unhash.

## Usage

```bash
cd /Users/nikiv/code/lang/go/cli/spec

go run . /Users/nikiv/code/org/linsa/linsa/spec/ai-request-response-mobile.md
```

Default behavior:
- reads the `.md` file
- generates a high-rigor Codex execution prompt
- copies the prompt to clipboard

Optional flags:

```bash
go run . -in /path/to/spec.md --stdout
go run . -in /path/to/spec.md --no-clipboard --stdout
go run . version
go run . deploy
```

### Launch Codex directly

```bash
go run . exec /Users/nikiv/code/org/linsa/linsa/spec/ai-request-response-mobile.md
```

By default, `spec exec` runs Codex with:

```bash
--dangerously-bypass-approvals-and-sandbox
```

so the session starts with full permissions.

Append extra instructions inline:

```bash
go run . exec /path/to/spec.md --append "Focus on edge-case tests too"
```

Append extra instructions from file:

```bash
go run . exec /path/to/spec.md --append-file /tmp/extra.md
```

Pass flags through to `codex` (everything after `--`):

```bash
go run . exec /path/to/spec.md -- --model gpt-5.1-codex-max --no-alt-screen
```

If you pass your own permission flags (`-a/--ask-for-approval`, `-s/--sandbox`, or `--dangerously-bypass-approvals-and-sandbox`), `spec` will not inject its default permission flag.

Dry run (show generated command, do not launch):

```bash
go run . exec /path/to/spec.md --dry-run
```

### Publish spec review to unhash

```bash
go run . review /Users/nikiv/code/org/linsa/linsa/spec/ai-request-response-mobile.md
```

Default review behavior:
- builds a publish payload from markdown (`title`, `summary`, `markdown`, source metadata)
- publishes to `https://unhash.sh/api/publish` (or `UNHASH_API_ORIGIN`)
- prints:
  - hash
  - canonical share URL (`https://unhash.sh/<hash>`)
  - feedback API endpoint
- copies the share URL to clipboard

Useful flags:

```bash
go run . review /path/to/spec.md --title "Custom title"
go run . review /path/to/spec.md --open
go run . review /path/to/spec.md --dry-run --stdout
go run . review /path/to/spec.md --api-origin http://localhost:8787 --no-clipboard
```

### Read inline feedback

```bash
go run . review-comments <hash-or-url>
go run . review-comments https://unhash.sh/<hash> --json
```

`deploy` builds and installs to `~/bin/spec`.
