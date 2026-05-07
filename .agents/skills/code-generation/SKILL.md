---
name: golang-code-generation
description: >
  Go code generation: go:generate directives, protoc/buf workflows, mockery for mocks,
  stringer for enums, wire for dependency injection, sqlc for type-safe SQL, template-based
  generation with text/template and html/template, AST manipulation with go/ast and
  go/types, and custom generator design patterns. Use when adding generated code to a project.
---

# Go Code Generation — Complete Patterns

## 1. go:generate Directives

```go
// Place //go:generate directives at the top of the file they affect
// Run all: go generate ./...
// Run one: go generate ./internal/domain/order/

package order

//go:generate mockery --name=Repository --output=../../mocks --outpkg=mocks --filename=order_repo_mock.go
//go:generate stringer -type=Status -output=status_string.go

type Status int
const (
    StatusPending   Status = iota
    StatusConfirmed
    StatusShipped
    StatusCancelled
)
```

---

## 2. mockery — Interface Mock Generation

```bash
# Install
go install github.com/vektra/mockery/v2@latest

# .mockery.yaml in project root
with-expecter: true          # generate EXPECT() helper methods
mockname: "Mock{{.InterfaceName}}"
outpkg: "mocks"
filename: "{{.InterfaceName | snakecase}}_mock.go"
dir: "mocks/{{.PackageName}}"
packages:
  github.com/org/project/internal/domain/order:
    interfaces:
      Repository: {}
      EventPublisher: {}
  github.com/org/project/internal/app/ports:
    interfaces:
      Notifier: {}
```

```go
// Generated mock usage in tests
func TestCreateOrderHandler(t *testing.T) {
    repo := mocks.NewMockRepository(t)  // auto-cleanup via t.Cleanup
    publisher := mocks.NewMockEventPublisher(t)

    // EXPECT() fluent API (with-expecter: true)
    repo.EXPECT().
        Save(mock.Anything, mock.MatchedBy(func(o *order.Order) bool {
            return o.CustomerID() == "cust-123"
        })).
        Return(nil).
        Once()

    publisher.EXPECT().
        Publish(mock.Anything, mock.AnythingOfType("order.OrderCreated")).
        Return(nil).
        Maybe()  // optional — may or may not be called

    h := NewCreateOrderHandler(repo, publisher, validator.New())
    result, err := h.Handle(ctx, CreateOrderCommand{CustomerID: "cust-123", Items: testItems})
    require.NoError(t, err)
    assert.NotNil(t, result)
}
```

---

## 3. stringer — Enum String Methods

```go
//go:generate stringer -type=Status,Role -linecomment

type Status int
const (
    StatusPending   Status = iota // pending
    StatusConfirmed               // confirmed
    StatusShipped                 // shipped
    StatusCancelled               // cancelled
)
// Generates: func (s Status) String() string { ... }
// StatusPending.String() → "pending"
```

---

## 4. buf + protoc — Protocol Buffers

```yaml
# buf.yaml — proto linting and breaking change detection
version: v2
deps:
  - buf.build/googleapis/googleapis
  - buf.build/grpc/grpc
lint:
  use: [DEFAULT]
  except: [PACKAGE_VERSION_SUFFIX]
breaking:
  use: [FILE]  # detect breaking changes at file level

# buf.gen.yaml — code generation
version: v2
plugins:
  - remote: buf.build/protocolbuffers/go
    out: gen/go
    opt: [paths=source_relative]
  - remote: buf.build/grpc/go
    out: gen/go
    opt: [paths=source_relative, require_unimplemented_servers=true]
  - remote: buf.build/grpc-ecosystem/gateway/v2
    out: gen/go
    opt: [paths=source_relative]
  - remote: buf.build/envoyproxy/protoc-gen-validate
    out: gen/go
    opt: [paths=source_relative, lang=go]
```

```bash
# Makefile targets
proto-lint:
    buf lint

proto-breaking:
    buf breaking --against '.git#branch=main'

proto-generate:
    buf generate

proto-push:
    buf push  # push to BSR (Buf Schema Registry)
```

---

## 5. sqlc — Type-Safe SQL

```yaml
# sqlc.yaml
version: "2"
sql:
  - engine: "postgresql"
    queries: "internal/adapters/postgres/queries/"
    schema:  "migrations/"
    gen:
      go:
        package: "pgstore"
        out: "internal/adapters/postgres/pgstore"
        emit_json_tags: true
        emit_db_tags: true
        emit_interface: true      # generates Querier interface
        emit_exact_table_names: false
        emit_empty_slices: true   # return [] not nil for empty
        emit_pointers_for_null_types: true
```

```sql
-- internal/adapters/postgres/queries/orders.sql
-- name: GetOrder :one
SELECT id, customer_id, status, version, created_at
FROM orders
WHERE id = $1 AND deleted_at IS NULL;

-- name: ListOrdersByCustomer :many
SELECT id, customer_id, status, version, created_at
FROM orders
WHERE customer_id = $1 AND deleted_at IS NULL
ORDER BY created_at DESC
LIMIT $2;

-- name: CreateOrder :one
INSERT INTO orders (id, customer_id, status, version)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: UpdateOrderStatus :execrows
UPDATE orders
SET status = $2, version = $3, updated_at = NOW()
WHERE id = $1 AND version = $3 - 1;  -- optimistic lock
```

---

## 6. wire — Dependency Injection Generation

```go
// wire/wire.go — provider declarations (see packages/wire/SKILL.md)
//go:build wireinject

package wire

import "github.com/google/wire"

func InitializeApp(ctx context.Context, cfg *config.Config) (*App, func(), error) {
    wire.Build(
        // Infrastructure
        postgres.NewPool,
        redis.NewClient,
        kafka.NewProducer,
        // Repositories
        postgres.NewOrderRepository,
        // Services
        order.NewCreateOrderHandler,
        order.NewGetOrderHandler,
        // Handlers
        http.NewOrderHandler,
        // App
        NewApp,
    )
    return nil, nil, nil
}
```

---

## 7. AST-Based Custom Generator

```go
// When you need custom code generation beyond off-the-shelf tools
package main

import (
    "go/ast"
    "go/parser"
    "go/token"
    "text/template"
)

func generateFromAST(sourceFile string, tmpl *template.Template) error {
    fset := token.NewFileSet()
    f, err := parser.ParseFile(fset, sourceFile, nil, parser.ParseComments)
    if err != nil { return fmt.Errorf("parse: %w", err) }

    // Walk the AST to find interfaces
    var interfaces []InterfaceInfo
    ast.Inspect(f, func(n ast.Node) bool {
        typeSpec, ok := n.(*ast.TypeSpec)
        if !ok { return true }
        iface, ok := typeSpec.Type.(*ast.InterfaceType)
        if !ok { return true }

        info := InterfaceInfo{Name: typeSpec.Name.Name}
        for _, method := range iface.Methods.List {
            info.Methods = append(info.Methods, method.Names[0].Name)
        }
        interfaces = append(interfaces, info)
        return true
    })

    // Generate code from template
    return tmpl.Execute(os.Stdout, interfaces)
}

type InterfaceInfo struct {
    Name    string
    Methods []string
}
```

---

## Code Generation Checklist

- [ ] `//go:generate` directives at top of file, not scattered
- [ ] Generated files have `// Code generated ... DO NOT EDIT.` header
- [ ] Generated files committed to repo (not gitignored) for reproducibility
- [ ] `go generate ./...` is idempotent — safe to run multiple times
- [ ] `buf lint` and `buf breaking` run in CI for proto changes
- [ ] mockery config in `.mockery.yaml` — not inline flags
- [ ] sqlc generates `emit_empty_slices: true` — never nil for collections
- [ ] wire providers are pure functions — no side effects in provider body
