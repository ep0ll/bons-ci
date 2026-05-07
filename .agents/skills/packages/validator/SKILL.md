---
name: pkg-validator
description: >
  Exhaustive reference for github.com/go-playground/validator/v10: struct validation tags,
  custom validators, cross-field validation, error extraction with localization, and
  integration with HTTP handlers. Primary input validation library. Cross-references:
  api-design/SKILL.md, error-handling/SKILL.md, packages/chi/SKILL.md.
---

# Package: go-playground/validator/v10 — Complete Reference

## Import
```go
import "github.com/go-playground/validator/v10"
```

## 1. Validator Setup (Singleton)

```go
// Singleton validator — expensive to create, safe for concurrent use
var validate *validator.Validate

func init() {
    validate = validator.New(validator.WithRequiredStructEnabled())

    // Use JSON tag names in error messages (not struct field names)
    validate.RegisterTagNameFunc(func(fld reflect.StructField) string {
        name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
        if name == "-" { return "" }
        return name
    })

    // Register custom validators
    _ = validate.RegisterValidation("uuid4", validateUUID4)
    _ = validate.RegisterValidation("slug", validateSlug)
    _ = validate.RegisterValidation("currency", validateCurrency)
}
```

## 2. Struct Validation Tags

```go
type CreateOrderRequest struct {
    CustomerID string      `json:"customer_id" validate:"required,uuid4"`
    Items      []ItemInput `json:"items"        validate:"required,min=1,max=100,dive"`
    CouponCode string      `json:"coupon_code"  validate:"omitempty,alphanum,min=4,max=20"`
    Note       string      `json:"note"         validate:"omitempty,max=500"`
}

type ItemInput struct {
    ProductID string `json:"product_id" validate:"required,uuid4"`
    Quantity  int    `json:"quantity"   validate:"required,min=1,max=1000"`
}

type UpdateUserRequest struct {
    Name      string `json:"name"      validate:"omitempty,min=1,max=255"`
    Email     string `json:"email"     validate:"omitempty,email"`
    Phone     string `json:"phone"     validate:"omitempty,e164"`          // E.164 format
    BirthYear int    `json:"birth_year" validate:"omitempty,min=1900,max=2010"`
    Role      string `json:"role"       validate:"omitempty,oneof=admin user guest"`
    Website   string `json:"website"    validate:"omitempty,url"`
}

// Cross-field validation
type DateRange struct {
    StartDate time.Time `json:"start_date" validate:"required"`
    EndDate   time.Time `json:"end_date"   validate:"required,gtfield=StartDate"`
}

// Conditional validation
type PaymentRequest struct {
    Method      string `json:"method"       validate:"required,oneof=card bank_transfer"`
    CardNumber  string `json:"card_number"  validate:"required_if=Method card,omitempty,credit_card"`
    BankAccount string `json:"bank_account" validate:"required_if=Method bank_transfer,omitempty,min=6"`
}
```

## 3. Common Tag Reference

```go
// Required
required            // field must be non-zero value
required_if=Field value   // required when Field == value
required_unless=Field value
omitempty           // skip validation if zero value

// String
min=3,max=255       // length bounds
len=10              // exact length
alpha               // a-z A-Z only
alphanum            // a-z A-Z 0-9
numeric             // numeric string
email               // valid email format
url                 // valid URL
uri                 // valid URI
uuid                // valid UUID (any version)
uuid4               // valid UUIDv4 specifically
e164                // E.164 phone format
isbn13              // valid ISBN-13
ascii               // ASCII only
printascii          // printable ASCII
startswith=foo      // must start with "foo"
endswith=bar        // must end with "bar"
contains=@          // must contain "@"
excludes=admin      // must NOT contain "admin"
oneof=red green blue // must be one of listed values

// Numeric
min=0,max=100       // value bounds
gt=0                // greater than
gte=18              // greater than or equal
lt=150              // less than
lte=100             // less than or equal
positive            // > 0
negative            // < 0

// Collections
min=1,max=100       // collection size bounds
dive               // validate each element
keys               // validate map keys (use with dive)
endkeys            // end map key validation

// Cross-field
eqfield=OtherField          // equal to other field
nefield=OtherField          // not equal
gtfield=StartDate           // greater than other field
ltefield=EndDate            // less than or equal to other field
```

## 4. Error Extraction and Formatting

```go
// Extract structured validation errors for API response
func extractValidationErrors(err error) map[string]string {
    var ve validator.ValidationErrors
    if !errors.As(err, &ve) { return nil }

    fields := make(map[string]string, len(ve))
    for _, fe := range ve {
        fields[fe.Field()] = fieldErrorMessage(fe)
    }
    return fields
}

func fieldErrorMessage(fe validator.FieldError) string {
    switch fe.Tag() {
    case "required":         return "this field is required"
    case "min":              return fmt.Sprintf("minimum %s characters/items", fe.Param())
    case "max":              return fmt.Sprintf("maximum %s characters/items", fe.Param())
    case "email":            return "must be a valid email address"
    case "uuid4":            return "must be a valid UUID"
    case "oneof":            return fmt.Sprintf("must be one of: %s", fe.Param())
    case "e164":             return "must be a valid E.164 phone number"
    case "url":              return "must be a valid URL"
    case "required_if":      return "this field is required"
    case "gtfield":          return fmt.Sprintf("must be greater than %s", fe.Param())
    default:                 return fmt.Sprintf("failed validation: %s", fe.Tag())
    }
}

// Usage in HTTP handler
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
    var req CreateOrderRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        respondError(w, http.StatusUnprocessableEntity, "invalid JSON", nil)
        return
    }

    if err := validate.StructCtx(r.Context(), req); err != nil {
        respondError(w, http.StatusUnprocessableEntity, "validation failed",
            extractValidationErrors(err))
        return
    }
    // proceed
}

// Response: {"error":{"code":"VALIDATION_ERROR","message":"validation failed",
//            "fields":{"customer_id":"must be a valid UUID","items":"minimum 1 items"}}}
```

## 5. Custom Validators

```go
// Register once in init()
func validateUUID4(fl validator.FieldLevel) bool {
    id, err := uuid.Parse(fl.Field().String())
    return err == nil && id.Version() == 4
}

func validateSlug(fl validator.FieldLevel) bool {
    return regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`).
        MatchString(fl.Field().String())
}

func validateCurrency(fl validator.FieldLevel) bool {
    code := fl.Field().String()
    _, ok := validCurrencyCodes[code]
    return ok
}

// Struct-level cross-field validation
validate.RegisterStructValidation(func(sl validator.StructLevel) {
    req := sl.Current().Interface().(DateRange)
    if req.EndDate.Before(req.StartDate) {
        sl.ReportError(req.EndDate, "end_date", "EndDate", "gtfield", "start_date")
    }
}, DateRange{})
```

## 6. Var Validation (Non-struct)

```go
// Validate individual values
err := validate.Var(email, "required,email")
err = validate.Var(count, "min=1,max=100")
err = validate.VarCtx(ctx, userID, "required,uuid4")

// Validate slices
err = validate.Var([]string{"a", "b", "c"}, "min=1,dive,min=1,max=50")
```

## validator Checklist
- [ ] Single `validator.New()` instance — not recreated per request
- [ ] `RegisterTagNameFunc` sets JSON field names for error messages
- [ ] `StructCtx` used (not `Struct`) — respects context cancellation
- [ ] `extractValidationErrors` returns `map[string]string` for structured API errors
- [ ] Custom validators registered in `init()` with descriptive names
- [ ] `omitempty` used for optional fields — not `required` with zero-check
- [ ] `dive` used for slice/map element validation
- [ ] Struct-level validators for cross-field constraints (date ranges, conditional fields)
- [ ] Error messages localized to user-friendly language (not raw tag names)
