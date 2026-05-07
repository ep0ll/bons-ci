---
name: pkg-decimal
description: >
  shopspring/decimal precise decimal arithmetic for money. See packages/uuid/SKILL.md Part 2
  for the complete decimal reference. Load packages/uuid/SKILL.md when working with money,
  prices, or any decimal arithmetic. NEVER use float64 for money values.
---

# Package: shopspring/decimal

See **`packages/uuid/SKILL.md` — Part 2: shopspring/decimal** for the full reference.

That file covers:
- Construction from float, string, int
- All arithmetic operations (Add, Sub, Mul, Div)
- Rounding modes (Round, RoundUp, RoundDown, RoundCeil, RoundFloor)
- Comparison methods
- Database and JSON marshaling
- Money value object pattern

Quick import:
```go
import "github.com/shopspring/decimal"

price := decimal.NewFromFloat(19.99)
total := price.Mul(decimal.NewFromInt(3)).Round(2)
```

**Core rule**: Always `Round(2)` before persisting. Never `float64` for money.
