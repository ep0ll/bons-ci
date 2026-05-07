---
name: pkg-protobuf
description: >
  Exhaustive reference for google.golang.org/protobuf: proto3 message design, marshaling,
  field masks, well-known types (Timestamp, Duration, Any, Struct), proto options,
  proto-validate rules, JSON marshaling, and backward-compatibility rules. Cross-references:
  packages/grpc/SKILL.md, api-design/SKILL.md, code-generation/SKILL.md.
---

# Package: google.golang.org/protobuf — Complete Reference

## Import
```go
import (
    "google.golang.org/protobuf/proto"
    "google.golang.org/protobuf/encoding/protojson"
    "google.golang.org/protobuf/types/known/timestamppb"
    "google.golang.org/protobuf/types/known/durationpb"
    "google.golang.org/protobuf/types/known/fieldmaskpb"
    "google.golang.org/protobuf/types/known/emptypb"
    "google.golang.org/protobuf/types/known/wrapperspb"
)
```

## 1. Proto3 Message Design

```protobuf
// api/order/v1/order.proto
syntax = "proto3";
package order.v1;
option go_package = "github.com/org/project/gen/go/order/v1;orderv1";

import "google/protobuf/timestamp.proto";
import "google/protobuf/field_mask.proto";
import "buf/validate/validate.proto";

message Order {
  string id          = 1;  // immutable — never reuse field numbers
  string customer_id = 2;
  OrderStatus status = 3;
  repeated LineItem items = 4;
  Money total        = 5;
  google.protobuf.Timestamp created_at = 6;
  google.protobuf.Timestamp updated_at = 7;
}

enum OrderStatus {
  ORDER_STATUS_UNSPECIFIED = 0;  // MUST be zero value — unset/unknown
  ORDER_STATUS_PENDING     = 1;
  ORDER_STATUS_CONFIRMED   = 2;
  ORDER_STATUS_SHIPPED     = 3;
  ORDER_STATUS_CANCELLED   = 4;
}

message Money {
  int64  amount_cents = 1;   // int64 for money — never float
  string currency     = 2;   // ISO 4217
}

// proto-validate rules (buf.build/bufbuild/protovalidate)
message CreateOrderRequest {
  string customer_id = 1 [(buf.validate.field).string.uuid = true];
  repeated LineItem items = 2 [(buf.validate.field).repeated = {min_items: 1, max_items: 100}];
  string idempotency_key = 3 [(buf.validate.field).string.min_len = 1];
}
```

## 2. Backward Compatibility Rules

```
SAFE (non-breaking):
  ✓ Add new fields (always optional in proto3)
  ✓ Add new enum values
  ✓ Add new messages
  ✓ Add new RPC methods
  ✓ Add new services

UNSAFE (breaking):
  ✗ Change field number
  ✗ Change field type
  ✗ Remove a field (reserve the number instead)
  ✗ Rename enum values (the NUMBER must not change)
  ✗ Change a repeated field to singular
  ✗ Move field between oneof and not-oneof

REMOVING FIELDS: use reserved keyword
  reserved 5;         // never reuse field 5
  reserved "old_name"; // never reuse this name
```

## 3. Marshaling

```go
// Binary marshaling (wire format)
data, err := proto.Marshal(order)
if err != nil { return nil, fmt.Errorf("proto.Marshal: %w", err) }

restored := &orderv1.Order{}
if err := proto.Unmarshal(data, restored); err != nil {
    return nil, fmt.Errorf("proto.Unmarshal: %w", err)
}

// proto.Equal for comparison (never == on proto messages)
if proto.Equal(a, b) { /* same content */ }

// proto.Clone for deep copy
cloned := proto.Clone(original).(*orderv1.Order)
```

## 4. JSON Marshaling (protojson)

```go
// protojson — NOT encoding/json — for proto messages
// Reason: protojson handles well-known types, enums, oneof correctly

m := protojson.MarshalOptions{
    EmitUnpopulated:   false,  // don't emit zero values
    UseProtoNames:     true,   // use snake_case field names (not camelCase)
    EmitDefaultValues: false,
}
jsonBytes, err := m.Marshal(order)
if err != nil { return nil, fmt.Errorf("protojson.Marshal: %w", err) }

um := protojson.UnmarshalOptions{
    DiscardUnknown: true,  // ignore unknown fields (forward compat)
}
msg := &orderv1.Order{}
if err := um.Unmarshal(jsonBytes, msg); err != nil {
    return nil, fmt.Errorf("protojson.Unmarshal: %w", err)
}
```

## 5. Well-Known Types

```go
// Timestamp
import "google.golang.org/protobuf/types/known/timestamppb"
ts := timestamppb.New(time.Now())     // time.Time → Timestamp
t := ts.AsTime()                       // Timestamp → time.Time
if err := ts.CheckValid(); err != nil { /* invalid */ }

// Duration
import "google.golang.org/protobuf/types/known/durationpb"
d := durationpb.New(30 * time.Second)
dur := d.AsDuration()

// FieldMask (for partial updates — AIP-134)
import "google.golang.org/protobuf/types/known/fieldmaskpb"
mask, err := fieldmaskpb.New(&orderv1.Order{}, "status", "updated_at")
// Validate mask paths against the message type
if err := mask.IsValid(&orderv1.Order{}); err != nil { /* invalid paths */ }
// Normalize (sort + deduplicate)
mask.Normalize()

// Apply field mask
proto.Merge(target, &orderv1.Order{Status: newStatus}) // apply only masked fields
// Use fieldmask-utils library for selective merge

// Wrappers (nullable primitives)
import "google.golang.org/protobuf/types/known/wrapperspb"
optionalName := wrapperspb.String("Alice")  // *StringValue — nil = not set
if optionalName != nil { name := optionalName.Value }
```

## 6. Type Conversion Pattern (Proto ↔ Domain)

```go
// Converter: keep proto/domain conversion in one place (adapters layer)
// Domain types never import proto packages

func toProtoOrder(o *domain.Order) *orderv1.Order {
    items := make([]*orderv1.LineItem, len(o.Items()))
    for i, item := range o.Items() {
        items[i] = toProtoLineItem(item)
    }
    return &orderv1.Order{
        Id:         o.ID().String(),
        CustomerId: o.CustomerID().String(),
        Status:     toProtoStatus(o.Status()),
        Items:      items,
        Total:      &orderv1.Money{
            AmountCents: o.Total().AmountCents(),
            Currency:    o.Total().Currency(),
        },
        CreatedAt: timestamppb.New(o.CreatedAt()),
    }
}

func fromProtoCreateRequest(req *orderv1.CreateOrderRequest) (app.CreateOrderCommand, error) {
    items := make([]app.ItemInput, len(req.Items))
    for i, item := range req.Items {
        qty := int(item.Quantity)
        if qty <= 0 { return app.CreateOrderCommand{}, fmt.Errorf("item[%d]: quantity must be positive", i) }
        items[i] = app.ItemInput{ProductID: item.ProductId, Quantity: qty}
    }
    return app.CreateOrderCommand{
        CustomerID:      req.CustomerId,
        Items:           items,
        IdempotencyKey:  req.IdempotencyKey,
    }, nil
}
```

## protobuf Checklist
- [ ] `option go_package` set with full import path + package alias
- [ ] All enums have `UNSPECIFIED = 0` as first value
- [ ] Field numbers never reused — use `reserved` when removing fields
- [ ] Money as `int64` cents — never `float` or `double`
- [ ] Timestamps as `google.protobuf.Timestamp` — never string
- [ ] `proto-validate` annotations on all request messages
- [ ] `protojson` used for JSON marshaling — never `encoding/json`
- [ ] `proto.Equal` for comparison — never `==`
- [ ] `proto.Clone` for deep copy — never struct copy
- [ ] Domain types never import proto packages (conversion in adapters layer)
- [ ] `buf breaking` in CI to catch backward-incompatible changes
