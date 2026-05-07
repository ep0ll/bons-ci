---
name: pkg-buf
description: >
  Exhaustive reference for buf.build (Buf CLI): buf.yaml schema, buf.gen.yaml code generation,
  linting rules, breaking change detection, BSR (Buf Schema Registry), buf push, and
  integration with CI. Primary proto toolchain replacing raw protoc. Cross-references:
  packages/protobuf/SKILL.md, packages/grpc/SKILL.md, code-generation/SKILL.md.
---

# Package: buf CLI — Complete Reference

## 1. Project Layout

```
api/
├── buf.yaml              ← module definition + lint/breaking rules
├── buf.gen.yaml          ← code generation config
├── buf.lock              ← dependency lock file (commit this)
└── order/
    └── v1/
        ├── order.proto
        └── order_service.proto
```

## 2. buf.yaml

```yaml
# api/buf.yaml
version: v2

# External proto dependencies
deps:
  - buf.build/googleapis/googleapis          # google.api.*
  - buf.build/grpc/grpc                      # grpc status, health
  - buf.build/bufbuild/protovalidate         # field validation
  - buf.build/grpc-ecosystem/grpc-gateway    # HTTP/JSON gateway

# Lint rules
lint:
  use:
    - DEFAULT          # enable all default rules
  except:
    - PACKAGE_VERSION_SUFFIX  # if not using v1/v2 package versioning
  ignore:
    - google/           # don't lint vendored google protos

# Breaking change detection
breaking:
  use:
    - FILE             # detect breaking at file level (recommended)
  ignore_unstable_packages: false
```

## 3. buf.gen.yaml (Code Generation)

```yaml
# api/buf.gen.yaml
version: v2

# Managed mode: handles go_package, java_package etc. automatically
managed:
  enabled: true
  override:
    - file_option: go_package_prefix
      value: github.com/org/project/gen/go

plugins:
  # Go structs
  - remote: buf.build/protocolbuffers/go
    out: gen/go
    opt:
      - paths=source_relative

  # gRPC service stubs
  - remote: buf.build/grpc/go
    out: gen/go
    opt:
      - paths=source_relative
      - require_unimplemented_servers=true  # enforces forward compat

  # HTTP/JSON gateway (optional)
  - remote: buf.build/grpc-ecosystem/gateway/v2
    out: gen/go
    opt:
      - paths=source_relative
      - generate_unbound_methods=true

  # Validation (buf.validate)
  - remote: buf.build/bufbuild/validate-go
    out: gen/go
    opt:
      - paths=source_relative
```

## 4. Essential Commands

```bash
# Install
brew install bufbuild/buf/buf  # macOS
# or: go install github.com/bufbuild/buf/cmd/buf@latest

# Initialize workspace
buf config init

# Install dependencies from buf.yaml
buf dep update           # resolve and update buf.lock

# Generate code
buf generate             # runs buf.gen.yaml
buf generate api/        # specify input directory

# Lint
buf lint                 # lint all protos
buf lint api/order/v1/   # lint specific directory

# Breaking change detection
buf breaking --against '.git#branch=main'  # compare to main branch
buf breaking --against 'buf.build/org/project'  # compare to BSR

# Format protos
buf format --write

# Push to Buf Schema Registry (BSR)
buf push --tag v1.0.0

# Build (validate proto compiles)
buf build

# Local protoc fallback (when using local plugins)
buf generate --template buf.gen.local.yaml
```

## 5. Breaking Change Rules

```
BREAKING CHANGES (buf breaking catches these):
  ✗ Remove a field
  ✗ Change a field's type
  ✗ Change a field's number
  ✗ Remove a message
  ✗ Remove an enum value
  ✗ Remove an RPC method
  ✗ Change RPC request/response types
  ✗ Change field from optional to required

SAFE CHANGES (not breaking):
  ✓ Add new fields (proto3: always optional)
  ✓ Add new messages
  ✓ Add new enum values
  ✓ Add new RPC methods
  ✓ Add new services
  ✓ Change field name (number stays same — wire format unchanged)
```

## 6. CI Integration

```yaml
# .github/workflows/proto.yaml
name: Proto
on: [push, pull_request]

jobs:
  lint-and-breaking:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }  # full history for breaking detection

      - uses: bufbuild/buf-setup-action@v1
        with: { version: "1.30.0" }

      - name: Lint
        run: buf lint

      - name: Breaking change detection
        run: buf breaking --against '.git#branch=main'

      - name: Generate and check for drift
        run: |
          buf generate
          git diff --exit-code gen/  # fail if generated code not committed
```

## 7. Local Plugins (When BSR Not Available)

```yaml
# buf.gen.local.yaml — use locally installed protoc plugins
version: v2
plugins:
  - local: protoc-gen-go
    out: gen/go
    opt: [paths=source_relative]

  - local: protoc-gen-go-grpc
    out: gen/go
    opt: [paths=source_relative]
```

## buf Checklist
- [ ] `buf.lock` committed — reproducible builds
- [ ] `buf lint` passes with zero warnings
- [ ] `buf breaking --against '.git#branch=main'` in CI on every PR
- [ ] `buf format --write` applied to all protos (enforced in CI)
- [ ] Generated code committed — no regeneration drift
- [ ] `require_unimplemented_servers=true` in go-grpc plugin
- [ ] `managed.enabled: true` — no manual go_package options in .proto files
- [ ] Breaking change detection catches field removals before merge
- [ ] BSR used for shared protos across services (single source of truth)
