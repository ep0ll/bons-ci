---
name: golang-testing
description: >
  Go testing mastery: unit tests with table-driven patterns, testify, mocks with mockery,
  integration tests, fuzz testing, benchmarks, property-based testing, golden files, test
  helpers, test containers, contract testing, and CI testing pipeline setup. Use whenever
  writing or reviewing any test code in Go, or when implementing testable Go code.
---

# Go Testing — Comprehensive Patterns

## 1. Table-Driven Tests (canonical Go pattern)

```go
func TestParseAmount(t *testing.T) {
    t.Parallel() // always parallelize where possible
    
    tests := []struct {
        name    string
        input   string
        want    Amount
        wantErr bool
    }{
        {
            name:  "valid integer",
            input: "100",
            want:  Amount{Value: 100, Currency: "USD"},
        },
        {
            name:  "valid decimal",
            input: "19.99",
            want:  Amount{Value: 1999, Currency: "USD"},
        },
        {
            name:    "negative amount",
            input:   "-5",
            wantErr: true,
        },
        {
            name:    "empty string",
            input:   "",
            wantErr: true,
        },
    }
    
    for _, tt := range tests {
        tt := tt // capture range variable (Go < 1.22)
        t.Run(tt.name, func(t *testing.T) {
            t.Parallel()
            
            got, err := ParseAmount(tt.input)
            
            if tt.wantErr {
                require.Error(t, err)
                return
            }
            require.NoError(t, err)
            assert.Equal(t, tt.want, got)
        })
    }
}
```

---

## 2. Test Structure (AAA + Clean Code)

```go
// Arrange-Act-Assert with clear separation
func TestUserService_Register(t *testing.T) {
    t.Parallel()
    
    // --- Arrange ---
    repo := &mockUserRepository{}
    emailer := &mockEmailer{}
    svc := NewUserService(repo, emailer)
    
    req := RegisterRequest{
        Email:    "alice@example.com",
        Password: "securePassword123!",
    }
    
    repo.On("ExistsByEmail", mock.Anything, req.Email).Return(false, nil)
    repo.On("Save", mock.Anything, mock.MatchedBy(func(u *User) bool {
        return u.Email == req.Email
    })).Return(nil)
    emailer.On("SendWelcome", mock.Anything, req.Email).Return(nil)
    
    // --- Act ---
    user, err := svc.Register(context.Background(), req)
    
    // --- Assert ---
    require.NoError(t, err)
    assert.Equal(t, req.Email, user.Email)
    assert.NotEmpty(t, user.ID)
    assert.Empty(t, user.PasswordHash, "hash must not be leaked")
    
    repo.AssertExpectations(t)
    emailer.AssertExpectations(t)
}
```

---

## 3. Mocking (with mockery)

```go
// Generate mocks: go generate ./...
// //go:generate mockery --name=UserRepository --output=./mocks --outpkg=mocks

// Hand-written mock for small interfaces
type mockUserRepository struct {
    mock.Mock
}

func (m *mockUserRepository) Save(ctx context.Context, user *User) error {
    args := m.Called(ctx, user)
    return args.Error(0)
}

func (m *mockUserRepository) FindByEmail(ctx context.Context, email string) (*User, error) {
    args := m.Called(ctx, email)
    if args.Get(0) == nil { return nil, args.Error(1) }
    return args.Get(0).(*User), args.Error(1)
}

// Test double: stub (always returns fixed value) — simpler than mock for read-only
type stubUserRepository struct {
    users map[UserID]*User
}
func (r *stubUserRepository) FindByID(_ context.Context, id UserID) (*User, error) {
    u, ok := r.users[id]
    if !ok { return nil, ErrNotFound }
    return u, nil
}
```

---

## 4. Fuzz Testing (Go 1.18+)

```go
// Fuzz targets catch panics and incorrect behavior with random inputs
func FuzzParseAmount(f *testing.F) {
    // Seed corpus — known interesting values
    f.Add("0")
    f.Add("100")
    f.Add("99.99")
    f.Add("-1")
    f.Add("")
    f.Add("not-a-number")
    f.Add("999999999999999999999") // overflow
    
    f.Fuzz(func(t *testing.T, s string) {
        // Must not panic — ever
        amount, err := ParseAmount(s)
        if err != nil { return } // error is fine
        
        // Round-trip property: parse → format → parse must be identical
        formatted := amount.String()
        reparsed, err := ParseAmount(formatted)
        require.NoError(t, err, "round-trip failed for %q → %q", s, formatted)
        assert.Equal(t, amount, reparsed, "round-trip mismatch")
    })
}
// Run: go test -fuzz=FuzzParseAmount -fuzztime=30s ./...
```

---

## 5. Benchmarks

```go
func BenchmarkJSON(b *testing.B) {
    payload := largePayload()
    
    b.Run("encode", func(b *testing.B) {
        b.ReportAllocs()
        b.SetBytes(int64(len(payload)))
        b.ResetTimer()
        for i := 0; i < b.N; i++ {
            _, _ = json.Marshal(payload)
        }
    })
    
    b.Run("decode", func(b *testing.B) {
        data, _ := json.Marshal(payload)
        b.ReportAllocs()
        b.SetBytes(int64(len(data)))
        b.ResetTimer()
        for i := 0; i < b.N; i++ {
            var out Payload
            _ = json.Unmarshal(data, &out)
        }
    })
}

// Run with: go test -bench=. -benchmem -count=5 ./...
// Compare: benchstat old.txt new.txt
```

---

## 6. Integration Tests with Testcontainers

```go
import "github.com/testcontainers/testcontainers-go/modules/postgres"

func TestUserRepository_Integration(t *testing.T) {
    if testing.Short() { t.Skip("integration test: requires Docker") }
    t.Parallel()
    
    ctx := context.Background()
    
    // Spin up real Postgres container
    pgContainer, err := postgres.RunContainer(ctx,
        testcontainers.WithImage("postgres:16-alpine"),
        postgres.WithDatabase("testdb"),
        postgres.WithUsername("test"),
        postgres.WithPassword("test"),
        testcontainers.WithWaitStrategy(
            wait.ForLog("database system is ready to accept connections").
                WithOccurrence(2).WithStartupTimeout(30*time.Second)),
    )
    require.NoError(t, err)
    t.Cleanup(func() { pgContainer.Terminate(ctx) })
    
    dsn, _ := pgContainer.ConnectionString(ctx, "sslmode=disable")
    db, err := sql.Open("postgres", dsn)
    require.NoError(t, err)
    
    // Run migrations
    require.NoError(t, runMigrations(db))
    
    repo := NewUserRepository(db)
    
    // Run tests against real DB
    t.Run("save and find", func(t *testing.T) {
        user := &User{Email: "test@example.com"}
        require.NoError(t, repo.Save(ctx, user))
        assert.NotZero(t, user.ID)
        
        found, err := repo.FindByEmail(ctx, "test@example.com")
        require.NoError(t, err)
        assert.Equal(t, user.ID, found.ID)
    })
}
```

---

## 7. Golden Files

```go
// For testing complex outputs (HTML, JSON, text reports)
var updateGolden = flag.Bool("update", false, "update golden files")

func TestRenderReport(t *testing.T) {
    data := loadTestData(t, "input.json")
    got := RenderReport(data)
    
    goldenPath := filepath.Join("testdata", t.Name()+".golden")
    
    if *updateGolden {
        require.NoError(t, os.WriteFile(goldenPath, got, 0644))
        return
    }
    
    want, err := os.ReadFile(goldenPath)
    require.NoError(t, err, "golden file missing — run with -update to create")
    assert.Equal(t, string(want), string(got))
}
// Run: go test -run TestRenderReport -update
```

---

## 8. Test Helpers & Fixtures

```go
// Test helpers: t as first arg, no error returns — use t.Fatal
func mustCreateUser(t *testing.T, repo UserRepository, email string) *User {
    t.Helper() // makes failure report correct line number
    user := &User{Email: email}
    if err := repo.Save(context.Background(), user); err != nil {
        t.Fatalf("mustCreateUser: %v", err)
    }
    return user
}

// Cleanup with t.Cleanup (preferred over defer in tests)
func withTempDir(t *testing.T) string {
    t.Helper()
    dir, err := os.MkdirTemp("", "test-*")
    require.NoError(t, err)
    t.Cleanup(func() { os.RemoveAll(dir) })
    return dir
}

// Seed deterministic random for reproducible tests
func deterministicRand(t *testing.T) *rand.Rand {
    seed := int64(42) // or t.Name() hash for per-test seed
    return rand.New(rand.NewSource(seed))
}
```

---

## 9. Test Coverage Standards

```makefile
test-coverage:
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out | tail -n1
	# Fail if coverage below threshold
	@coverage=$$(go tool cover -func=coverage.out | tail -n1 | awk '{print $$3}' | tr -d '%'); \
	if [ "$${coverage%.*}" -lt "80" ]; then echo "Coverage $${coverage}% below 80%"; exit 1; fi
```

---

## Testing Checklist

- [ ] All tests call `t.Parallel()` unless they share global state
- [ ] Table-driven tests for all functions with multiple input cases
- [ ] Mocks generated with mockery, not hand-written for large interfaces
- [ ] Fuzz targets for all public parsing/decoding functions
- [ ] Benchmarks with `b.ReportAllocs()` for performance-critical code
- [ ] Integration tests behind `testing.Short()` guard
- [ ] All test helpers call `t.Helper()`
- [ ] Test names use `TestFunction_Scenario_ExpectedBehavior` format
- [ ] No `time.Sleep` in tests — use channels, polling helpers, or testify Eventually
- [ ] Race detector enabled in CI: `go test -race ./...`
- [ ] 80%+ coverage on business logic (not generated code)
- [ ] Testcontainers used for real DB/cache/queue integration tests
