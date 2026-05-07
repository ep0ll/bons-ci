# Project: [PROJECT NAME]

## Engineering Standards

Before writing any Go code, read:
```
.claude/skills/golang-skills/SKILL.md
```

## Hard Constraints (never violate)

1. `ctx context.Context` always first parameter on all I/O functions
2. All errors wrapped: `fmt.Errorf("Type.Method(param): %w", err)`
3. `go test -race ./...` must always pass
4. No global mutable state — dependency injection only
5. No `init()` functions
6. No `panic()` in library code
7. All exported symbols have godoc comments

## Quality Gates

```bash
go vet ./...
golangci-lint run
go test -race -count=1 ./...
govulncheck ./...
```

## Tech Stack

| Concern | Package |
|---|---|
| [Fill in your stack] | |

## Package Skills

| Package | SKILL.md location |
|---|---|
| [Fill in as packages are created] | |
