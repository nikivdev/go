package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: flow <command>")
		fmt.Fprintln(os.Stderr, "commands: zed-focus-from-warp, version")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "zed-focus-from-warp":
		zedFocusFromWarp()
	case "version":
		fmt.Println(version)
	case "-h", "--help", "help":
		printHelp()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Println(`flow - workflow automation commands

Commands:
  zed-focus-from-warp   Activate Zed window matching clipboard folder name
                        (switch, focus, get, list windows)
  version               Show version
  help                  Show this help

Usage:
  flow zed-focus-from-warp   Read clipboard for folder path, find and raise matching Zed window`)
}

// zedFocusFromWarp reads clipboard (e.g. "~/flow - fish"), extracts folder name, and activates matching Zed window
func zedFocusFromWarp() {
	// Read clipboard
	out, err := exec.Command("pbpaste").Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to read clipboard:", err)
		os.Exit(1)
	}

	clip := strings.TrimSpace(string(out))
	if clip == "" {
		fmt.Fprintln(os.Stderr, "clipboard is empty")
		os.Exit(1)
	}

	// Parse: "~/flow - fish" -> extract "flow"
	// Take everything before " - " and get the last path component
	parts := strings.SplitN(clip, " - ", 2)
	path := strings.TrimSpace(parts[0])
	folder := filepath.Base(path)

	if folder == "" || folder == "." || folder == "/" {
		fmt.Fprintln(os.Stderr, "could not extract folder name from:", clip)
		os.Exit(1)
	}

	// Use AppleScript to find and activate Zed window with matching title
	script := fmt.Sprintf(`
tell application "System Events"
	tell process "Zed"
		set frontmost to true
		repeat with w in windows
			if name of w contains "%s" then
				perform action "AXRaise" of w
				return "activated"
			end if
		end repeat
	end tell
end tell
return "not found"
`, folder)

	result, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to activate window:", err)
		os.Exit(1)
	}

	if strings.TrimSpace(string(result)) == "not found" {
		fmt.Fprintf(os.Stderr, "no Zed window found with title containing: %s\n", folder)
		os.Exit(1)
	}
}
