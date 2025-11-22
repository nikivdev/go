package dockerlayers

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	effectStageStart = "stage start"
	effectFilesystem = "filesystem layer"
	effectMetadata   = "metadata"
	effectBuildArg   = "build arg"
)

type descriptor struct {
	Effect      string
	Explanation string
	CacheHint   string
}

var instructionDescriptors = map[string]descriptor{
	"FROM": {
		Effect:      effectStageStart,
		Explanation: "Starts a stage and pulls the referenced base image. Any cache from previous stages is discarded.",
		CacheHint:   "Invalidates when the base image digest or flags like --platform change.",
	},
	"RUN": {
		Effect:      effectFilesystem,
		Explanation: "Executes a shell command inside an intermediate container and commits the result as a new, immutable layer.",
		CacheHint:   "Cache key is the command text plus every file the command reads. Changing any of them busts the cache.",
	},
	"COPY": {
		Effect:      effectFilesystem,
		Explanation: "Copies files into the image and creates a new layer with their contents.",
		CacheHint:   "Any change in the source files or flags invalidates this layer's cache entry.",
	},
	"ADD": {
		Effect:      effectFilesystem,
		Explanation: "Behaves like COPY but also accepts remote URLs and auto-extracts tar archives, all of which produce new layers.",
		CacheHint:   "Cache depends on the archive/URL content as well as the instruction text.",
	},
	"CMD": {
		Effect:      effectMetadata,
		Explanation: "Sets the default command for containers created from the image. No filesystem changes occur.",
		CacheHint:   "Cache is tied to the instruction text only.",
	},
	"ENTRYPOINT": {
		Effect:      effectMetadata,
		Explanation: "Defines the executable that always runs when a container starts.",
		CacheHint:   "Cache is tied to the instruction text only.",
	},
	"ENV": {
		Effect:      effectMetadata,
		Explanation: "Persists environment variables into image metadata for future instructions and containers.",
		CacheHint:   "Any variable value change invalidates the cache for this step and later steps.",
	},
	"ARG": {
		Effect:      effectBuildArg,
		Explanation: "Defines build-time arguments. The value can influence cache keys but does not end up in the final image runtime environment.",
		CacheHint:   "Changing build args invalidates the layer that consumes them.",
	},
	"WORKDIR": {
		Effect:      effectMetadata,
		Explanation: "Sets the working directory for subsequent instructions, recorded as metadata.",
		CacheHint:   "Cache busts only when the path changes.",
	},
	"USER": {
		Effect:      effectMetadata,
		Explanation: "Configures the user/group used for following instructions and containers.",
		CacheHint:   "Cache busts when the user specification changes.",
	},
	"LABEL": {
		Effect:      effectMetadata,
		Explanation: "Adds metadata key/value pairs to the image manifest without touching the filesystem.",
		CacheHint:   "Cache invalidates when a label changes.",
	},
	"EXPOSE": {
		Effect:      effectMetadata,
		Explanation: "Documents which ports containers are expected to listen on. Pure metadata.",
		CacheHint:   "Cache invalidates when the exposed ports change.",
	},
	"VOLUME": {
		Effect:      effectMetadata,
		Explanation: "Declares mount points that become anonymous volumes at runtime.",
		CacheHint:   "Cache depends only on the instruction text.",
	},
	"HEALTHCHECK": {
		Effect:      effectMetadata,
		Explanation: "Stores a command for Docker to probe container health. No filesystem changes.",
		CacheHint:   "Cache invalidates when the command or interval flags change.",
	},
	"STOPSIGNAL": {
		Effect:      effectMetadata,
		Explanation: "Configures which signal Docker sends to stop the container.",
		CacheHint:   "Cache depends on the instruction text.",
	},
	"SHELL": {
		Effect:      effectMetadata,
		Explanation: "Overrides the default shell that RUN and similar instructions use.",
		CacheHint:   "Cache invalidates when the shell definition changes.",
	},
	"ONBUILD": {
		Effect:      effectMetadata,
		Explanation: "Registers a trigger that fires when the current image is used as a base in another Dockerfile.",
		CacheHint:   "Cache ties to the trigger content.",
	},
	"MAINTAINER": {
		Effect:      effectMetadata,
		Explanation: "Deprecated metadata about the author. Included here for completeness.",
		CacheHint:   "Cache invalidates when the value changes.",
	},
}

type rawInstruction struct {
	line int
	text string
}

type parsedInstruction struct {
	Line    int
	Keyword string
	Args    string
	Raw     string
}

type layerReport struct {
	Number      int
	Instruction parsedInstruction
	Effect      string
	Explanation string
	CacheHint   string
	Notes       []string
}

type stageInfo struct {
	Index int
	Name  string
	Base  string
}

type stageReport struct {
	Stage          stageInfo
	Layers         []layerReport
	FsLayers       int
	MetadataLayers int
	BuildArgs      int
}

type report struct {
	FilePath string
	Global   []layerReport
	Stages   []*stageReport
}

func RunCLI(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("dockerlayers", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dockerfilePath := fs.String("file", "Dockerfile", "path to the Dockerfile to inspect")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	rep, err := analyzeDockerfile(*dockerfilePath)
	if err != nil {
		return err
	}

	printReport(stdout, rep)
	return nil
}

func analyzeDockerfile(path string) (*report, error) {
	fullPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}

	rawInstructions, err := readInstructions(fullPath)
	if err != nil {
		return nil, err
	}
	if len(rawInstructions) == 0 {
		return nil, fmt.Errorf("no Dockerfile instructions found in %s", fullPath)
	}

	var instructions []parsedInstruction
	for _, raw := range rawInstructions {
		parsed, err := parseInstruction(raw)
		if err != nil {
			return nil, err
		}
		instructions = append(instructions, parsed)
	}

	rep := &report{
		FilePath: fullPath,
	}

	var stageIndex = -1
	stageAliases := map[string]int{}

	for _, inst := range instructions {
		if inst.Keyword == "" {
			continue
		}

		if inst.Keyword == "FROM" {
			stageIndex++
			stage := ensureStage(rep, stageIndex)
			base, alias := parseFrom(inst.Args)
			if base == "" {
				return nil, fmt.Errorf("line %d: FROM instruction missing base image", inst.Line)
			}
			stage.Stage.Base = base
			stage.Stage.Name = alias
			if alias != "" {
				stageAliases[strings.ToLower(alias)] = stageIndex
			}
			stageAliases[fmt.Sprintf("%d", stageIndex)] = stageIndex
			layer := buildLayer(inst, descriptorFor(inst.Keyword), []string{
				fmt.Sprintf("Stage resets here and pulls %q.", base),
			})
			if alias != "" {
				layer.Notes = append(layer.Notes, fmt.Sprintf("Alias %q lets you reference this stage via COPY --from=%s.", alias, alias))
			}
			layer.Number = len(stage.Layers) + 1
			stage.Layers = append(stage.Layers, layer)
			continue
		}

		if stageIndex == -1 {
			// Only ARG is valid before the first FROM.
			if inst.Keyword == "ARG" {
				layer := buildLayer(inst, descriptorFor(inst.Keyword), nil)
				layer.Notes = append(layer.Notes, "This ARG applies globally and can be referenced in the first FROM.")
				layer.Number = len(rep.Global) + 1
				rep.Global = append(rep.Global, layer)
				continue
			}
			return nil, fmt.Errorf("line %d: Dockerfile must start with FROM (found %s)", inst.Line, inst.Keyword)
		}

		stage := ensureStage(rep, stageIndex)
		layer := buildLayer(inst, descriptorFor(inst.Keyword), nil)

		switch inst.Keyword {
		case "COPY":
			layer.Notes = append(layer.Notes, copyNotes(inst.Args, stageAliases)...)
		case "ADD":
			if strings.Contains(inst.Args, "http://") || strings.Contains(inst.Args, "https://") {
				layer.Notes = append(layer.Notes, "Remote URLs are downloaded at build time; network changes can invalidate cache.")
			}
			if strings.Contains(inst.Args, ".tar") {
				layer.Notes = append(layer.Notes, "Tar archives are auto-extracted, which can surprise caching when archive contents change.")
			}
		case "RUN":
			layer.Notes = append(layer.Notes, "Cleanup temp files within the same RUN to prevent them from sticking in the layer.")
		case "ARG":
			layer.Notes = append(layer.Notes, "Only available during build; use ENV if the value is needed at runtime.")
		}

		switch layer.Effect {
		case effectFilesystem:
			stage.FsLayers++
		case effectMetadata:
			stage.MetadataLayers++
		case effectBuildArg:
			stage.BuildArgs++
		}

		layer.Number = len(stage.Layers) + 1
		stage.Layers = append(stage.Layers, layer)
	}

	return rep, nil
}

func buildLayer(inst parsedInstruction, desc descriptor, extraNotes []string) layerReport {
	layer := layerReport{
		Instruction: inst,
		Effect:      desc.Effect,
		Explanation: desc.Explanation,
		CacheHint:   desc.CacheHint,
	}
	layer.Notes = append(layer.Notes, extraNotes...)
	return layer
}

func descriptorFor(keyword string) descriptor {
	if desc, ok := instructionDescriptors[keyword]; ok {
		return desc
	}
	return descriptor{
		Effect:      effectMetadata,
		Explanation: "Recorded as metadata. It influences how containers start but does not add filesystem content.",
		CacheHint:   "Cache key ties to the literal instruction, so changing text invalidates the layer.",
	}
}

func ensureStage(rep *report, index int) *stageReport {
	for len(rep.Stages) <= index {
		rep.Stages = append(rep.Stages, &stageReport{
			Stage: stageInfo{
				Index: len(rep.Stages),
			},
		})
	}
	stage := rep.Stages[index]
	if stage == nil {
		stage = &stageReport{
			Stage: stageInfo{Index: index},
		}
		rep.Stages[index] = stage
	}
	return stage
}

func parseInstruction(raw rawInstruction) (parsedInstruction, error) {
	text := raw.text
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return parsedInstruction{}, nil
	}
	idx := strings.IndexFunc(trimmed, unicode.IsSpace)
	var keyword, args string
	if idx == -1 {
		keyword = trimmed
	} else {
		keyword = trimmed[:idx]
		args = strings.TrimSpace(trimmed[idx:])
	}
	return parsedInstruction{
		Line:    raw.line,
		Keyword: strings.ToUpper(keyword),
		Args:    args,
		Raw:     trimmed,
	}, nil
}

func readInstructions(path string) ([]rawInstruction, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var instructions []rawInstruction
	var current strings.Builder
	var currentLine int

	for line := 1; scanner.Scan(); line++ {
		text := scanner.Text()
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		lineWithoutInlineComment := removeInlineComment(trimmed)
		if lineWithoutInlineComment == "" {
			continue
		}

		if current.Len() == 0 {
			currentLine = line
		} else {
			current.WriteString(" ")
		}
		linePart, carries := stripContinuation(lineWithoutInlineComment)
		current.WriteString(linePart)

		if !carries {
			instructions = append(instructions, rawInstruction{
				line: currentLine,
				text: current.String(),
			})
			current.Reset()
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if current.Len() != 0 {
		return nil, errors.New("unterminated line continuation at end of file")
	}

	return instructions, nil
}

func stripContinuation(line string) (string, bool) {
	if strings.HasSuffix(line, "\\") {
		return strings.TrimSpace(strings.TrimSuffix(line, "\\")), true
	}
	return line, false
}

func removeInlineComment(line string) string {
	// Docker ignores inline comments preceded by whitespace. We implement a light-weight check.
	for i := 0; i < len(line); i++ {
		if line[i] == '#' && (i == 0 || unicode.IsSpace(rune(line[i-1]))) {
			return strings.TrimSpace(line[:i])
		}
	}
	return line
}

func parseFrom(args string) (base string, alias string) {
	tokens := strings.Fields(args)
	if len(tokens) == 0 {
		return "", ""
	}

	i := 0
	for i < len(tokens) {
		token := tokens[i]
		if strings.HasPrefix(token, "--") {
			i++
			continue
		}
		break
	}

	if i >= len(tokens) {
		return "", ""
	}

	base = tokens[i]
	i++
	if i+1 < len(tokens) && strings.EqualFold(tokens[i], "AS") {
		alias = tokens[i+1]
	}
	return base, alias
}

func copyNotes(args string, aliases map[string]int) []string {
	var notes []string
	source := detectCopySourceStage(args)
	if source == "" {
		notes = append(notes, "Takes files from the build context, so editing those files will invalidate this layer.")
		return notes
	}

	lowered := strings.ToLower(source)
	if idx, ok := aliases[lowered]; ok {
		notes = append(notes, fmt.Sprintf("Copies from stage %d (%s). Cache depends on that stage's output instead of local files.", idx, source))
	} else {
		notes = append(notes, fmt.Sprintf("Copies from %q. Make sure the stage or image exists.", source))
	}
	return notes
}

func detectCopySourceStage(args string) string {
	tokens := strings.Fields(args)
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		if strings.HasPrefix(token, "--from=") {
			return strings.TrimPrefix(token, "--from=")
		}
		if token == "--from" && i+1 < len(tokens) {
			return tokens[i+1]
		}
	}
	return ""
}

func printReport(w io.Writer, rep *report) {
	fmt.Fprintf(w, "Dockerfile insight for %s\n\n", rep.FilePath)

	if len(rep.Global) > 0 {
		fmt.Fprintln(w, "Global build args (before first FROM):")
		for i, layer := range rep.Global {
			printLayer(w, i+1, layer)
		}
		fmt.Fprintln(w)
	}

	for _, stage := range rep.Stages {
		if stage == nil {
			continue
		}
		displayName := fmt.Sprintf("Stage %d", stage.Stage.Index)
		if stage.Stage.Name != "" {
			displayName = fmt.Sprintf("Stage %d (%s)", stage.Stage.Index, stage.Stage.Name)
		}
		fmt.Fprintln(w, displayName)
		fmt.Fprintf(w, "  Base image: %s\n", stage.Stage.Base)
		fmt.Fprintf(w, "  Layer breakdown:\n")
		for _, layer := range stage.Layers {
			printLayer(w, layer.Number, layer)
		}
		fmt.Fprintf(w, "  Summary: %d filesystem layers | %d metadata steps | %d build args\n\n", stage.FsLayers, stage.MetadataLayers, stage.BuildArgs)
	}

	fmt.Fprintln(w, "Legend:")
	fmt.Fprintf(w, "  %s: Pulls or resets a stage.\n", effectStageStart)
	fmt.Fprintf(w, "  %s: Adds or mutates files, affecting image size and cache.\n", effectFilesystem)
	fmt.Fprintf(w, "  %s: Adjusts container config without changing files.\n", effectMetadata)
	fmt.Fprintf(w, "  %s: Build-only inputs that do not persist in the image.\n", effectBuildArg)
}

func printLayer(w io.Writer, number int, layer layerReport) {
	fmt.Fprintf(w, "  %2d. %-12s %s\n", number, layer.Effect, layer.Instruction.Raw)
	fmt.Fprintf(w, "      Why : %s\n", layer.Explanation)
	if layer.CacheHint != "" {
		fmt.Fprintf(w, "      Cache: %s\n", layer.CacheHint)
	}
	for _, note := range layer.Notes {
		fmt.Fprintf(w, "      Note : %s\n", note)
	}
}
