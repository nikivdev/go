# fgo

```
fgo --help
fgo is CLI to do things fast

Usage:
  fgo [command]

Run `fgo` without arguments to open the interactive command palette.

Available Commands:
  help             Help about any command
  deploy           Install fgo into ~/bin and optionally add it to PATH
  commit           Generate a commit message with GPT-5 nano and create the commit
  commitPush       Generate a commit message, commit, and push to the default remote
  commitReviewAndPush Generate a commit message, review it interactively, commit, and push
  branchFromClipboard Create a git branch from the clipboard name
  clone            Clone a GitHub repository into ~/gh/<owner>/<repo>
  cloneAndOpen     Clone a GitHub repository and open it in Cursor (Safari tab optional)
  gitCheckout      Check out a branch from the remote, creating a local tracking branch if needed
  killPort         Kill a process by the port it listens on, optionally with fuzzy finder
  privateForkRepo  Clone a repo and create a private fork with upstream remotes
  privateForkRepoAndOpen Clone a repo, create a private fork, and open it in Cursor
  gitFetchUpstream Fetch from upstream (or all remotes) with pruning
  gitSyncFork      Update a local branch from upstream using rebase or merge
  updateGoVersion  Upgrade Go using the workspace script
  youtubeToSound   Download audio from a YouTube URL into ~/.flow/youtube-sound using yt-dlp
  spotifyPlay      Start playing a Spotify track from a URL or ID
  openChanges      Open the current monthly changes doc in Cursor
  openLookingBack  Open the current looking-back doc in Cursor
  openSqlite       Select a .sqlite file in the current tree and open it in TablePlus
  version          Reports the current version of fgo

Flags:
  -h, --help   help for fgo

Use "fgo [command] --help" for more information about a command.
```

## Notes

Running `fgo` without any arguments opens an embedded fzf palette so you can fuzzy-search commands and read their descriptions before executing them.

For `fgo commit`, export `OPENAI_API_KEY` in your shell profile (e.g. fish config) so the CLI can talk to OpenAI. This environment variable is the only requirement, so the command works in local shells and CI alike.

For `fgo youtubeToSound`, the CLI automatically passes `--cookies-from-browser` using Safari cookies. Override this by setting `FLOW_YOUTUBE_COOKIES_BROWSER` (e.g. `firefox`), set it to `none` to skip cookies entirely, or pass your own `--cookies*` flags after the URL—they are forwarded directly to `yt-dlp`.

If you run `fgo youtubeToSound` without arguments, the command grabs the frontmost Safari tab URL automatically.

A shorthand `fe` alias is installed alongside `fgo`; update or remove the symlink at ~/bin/fe if you prefer a different name.
