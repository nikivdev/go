# Docker Layer Explorer

This tiny helper turns Dockerfiles into annotated layer reports so you can see which instructions create filesystem layers, metadata tweaks, or reset multi-stage builds.

## Run it

```bash
# inspect the Dockerfile in the current directory
go run ./try/dockerlayers/cmd/dockerlayers -file Dockerfile

# point at any other Dockerfile
GOFILE=/path/to/any/Dockerfile
go run ./try/dockerlayers/cmd/dockerlayers -file "$GOFILE"
```

Each layer is printed with the instruction, why it matters, cache hints, and any special notes (like `COPY --from` relationships or ARG scope reminders).

Prefer a super-fast loop? Use the helper at the repo root:

```bash
# run once
go run ./run.go -file path/to/Dockerfile

# rerun automatically when the file changes (great for throwaway experiments)
go run ./run.go -file path/to/Dockerfile -watch
```

## Learn with fixtures

The `testdata/` folder contains two teaching Dockerfiles:

- `testdata/simple/Dockerfile` – shows global `ARG`, metadata, filesystem layers, and the default command flow.
- `testdata/multistage/Dockerfile` – exercises stage aliases, `COPY --from`, build args inside stages, and `ENTRYPOINT` metadata.

Run the tool against them to experiment:

```bash
go run ./try/dockerlayers/cmd/dockerlayers -file try/dockerlayers/testdata/simple/Dockerfile

# or piggy-back on run.go shortcuts
go run ./run.go -sample simple -watch
```

Change an instruction (for example switch `RUN` commands or edit `COPY` sources) and rerun the tool to see how the cache hints update.

## Tests

Use Go's standard tooling to make sure the analyzer keeps behaving as expected:

```bash
go test ./try/dockerlayers              # run everything
go test ./try/dockerlayers -run Simple   # focus on the simple Dockerfile test
go test ./try/dockerlayers -v           # verbose output
```

The tests assert layer counts, metadata vs filesystem categorization, and the helpful notes emitted for multi-stage copies. They double as executable documentation.
