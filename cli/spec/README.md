# spec

Generate a Codex-optimized execution prompt from a Markdown spec.

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

`deploy` builds and installs to `~/bin/spec`.
