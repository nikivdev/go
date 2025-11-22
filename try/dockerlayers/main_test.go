package dockerlayers

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyzeDockerfileSimple(t *testing.T) {
	rep, err := analyzeDockerfile(testDockerfile("simple"))
	if err != nil {
		t.Fatalf("analyzeDockerfile(simple) error: %v", err)
	}

	if len(rep.Global) != 1 {
		t.Fatalf("expected 1 global ARG, got %d", len(rep.Global))
	}
	if rep.Global[0].Effect != effectBuildArg {
		t.Fatalf("expected global ARG to be marked as build arg, got %s", rep.Global[0].Effect)
	}
	if len(rep.Global[0].Notes) == 0 || !strings.Contains(rep.Global[0].Notes[0], "applies globally") {
		t.Fatalf("global ARG note missing explanation: %+v", rep.Global[0].Notes)
	}

	if len(rep.Stages) == 0 || rep.Stages[0] == nil {
		t.Fatalf("expected at least one stage")
	}
	stage := rep.Stages[0]

	if want, got := "alpine:${GLOBAL_VERSION}", stage.Stage.Base; want != got {
		t.Fatalf("stage base mismatch: want %q got %q", want, got)
	}
	if want, got := 2, stage.FsLayers; want != got {
		t.Errorf("filesystem layer count: want %d got %d", want, got)
	}
	if want, got := 3, stage.MetadataLayers; want != got {
		t.Errorf("metadata layer count: want %d got %d", want, got)
	}
	if stage.BuildArgs != 0 {
		t.Errorf("unexpected build args count inside stage: %d", stage.BuildArgs)
	}

	if want, got := 6, len(stage.Layers); want != got {
		t.Fatalf("expected %d layers in the stage, got %d", want, got)
	}
	first := stage.Layers[0]
	if first.Effect != effectStageStart {
		t.Fatalf("first layer effect: want %s got %s", effectStageStart, first.Effect)
	}

	runLayer := findLayer(stage, "RUN")
	if runLayer == nil {
		t.Fatalf("RUN instruction not found")
	}
	if runLayer.Effect != effectFilesystem {
		t.Fatalf("RUN effect: want filesystem got %s", runLayer.Effect)
	}
}

func TestAnalyzeDockerfileMultiStage(t *testing.T) {
	rep, err := analyzeDockerfile(testDockerfile("multistage"))
	if err != nil {
		t.Fatalf("analyzeDockerfile(multistage) error: %v", err)
	}

	if want, got := 3, len(rep.Stages); want != got {
		t.Fatalf("expected %d stages, got %d", want, got)
	}

	base := rep.Stages[0]
	if base.Stage.Name != "base" {
		t.Fatalf("base stage alias missing: %+v", base.Stage)
	}

	builder := rep.Stages[1]
	if want, got := 1, builder.BuildArgs; want != got {
		t.Errorf("builder build args count: want %d got %d", want, got)
	}
	copyLayer := findLayer(builder, "COPY")
	if copyLayer == nil {
		t.Fatalf("COPY --from base layer missing")
	}
	if !noteContains(copyLayer.Notes, "stage 0 (base)") {
		t.Fatalf("expected COPY note to mention base stage, notes=%v", copyLayer.Notes)
	}

	final := rep.Stages[2]
	if final.Stage.Base != "scratch" {
		t.Fatalf("final stage base mismatch: %s", final.Stage.Base)
	}
	finalCopy := findLayer(final, "COPY")
	if finalCopy == nil {
		t.Fatalf("final COPY layer missing")
	}
	if !noteContains(finalCopy.Notes, "stage 1 (builder)") {
		t.Fatalf("expected final COPY to mention builder stage, notes=%v", finalCopy.Notes)
	}
	entry := findLayer(final, "ENTRYPOINT")
	if entry == nil || entry.Effect != effectMetadata {
		t.Fatalf("ENTRYPOINT effect mismatch: %+v", entry)
	}
}

func findLayer(stage *stageReport, keyword string) *layerReport {
	for i := range stage.Layers {
		layer := stage.Layers[i]
		if layer.Instruction.Keyword == keyword {
			return &layer
		}
	}
	return nil
}

func noteContains(notes []string, needle string) bool {
	for _, note := range notes {
		if strings.Contains(note, needle) {
			return true
		}
	}
	return false
}

func testDockerfile(name string) string {
	return filepath.Join("testdata", name, "Dockerfile")
}
