---
name: golang-cli
description: >
  Go CLI tool design: command hierarchy, flag handling, stdin/stdout/stderr separation,
  exit codes, shell completion, progress reporting, interactive prompts, color/formatting,
  config file integration, table output, JSON output mode, and distribution/install patterns.
  Always combine with packages/cobra-viper/SKILL.md and configuration/SKILL.md.
---

# Go CLI — Production Tool Design

## 1. Command Structure

```go
// cmd/myapp/main.go — thin entrypoint
func main() {
    if err := cli.New().Execute(); err != nil {
        // cobra prints usage on flag errors; we only exit
        os.Exit(exitCodeFor(err))
    }
}

func exitCodeFor(err error) int {
    var exitErr *ExitError
    if errors.As(err, &exitErr) { return exitErr.Code }
    return 1
}

// internal/cli/root.go
func New() *cobra.Command {
    root := &cobra.Command{
        Use:          "myapp",
        Short:        "My application CLI",
        Long:         `myapp manages orders, users, and payments.`,
        SilenceUsage: true,  // don't show usage on runtime errors (only on flag errors)
        SilenceErrors: true, // we handle error printing ourselves
        PersistentPreRunE: globalSetup,
    }

    // Global flags (all subcommands)
    root.PersistentFlags().StringP("config", "c", "", "config file (default: ./config.yaml)")
    root.PersistentFlags().StringP("output", "o", "table", "output format: table|json|yaml")
    root.PersistentFlags().BoolP("verbose", "v", false, "verbose output")
    root.PersistentFlags().Bool("no-color", false, "disable color output")

    // Subcommands
    root.AddCommand(
        newOrdersCmd(),
        newUsersCmd(),
        newVersionCmd(),
        newCompletionCmd(root),
    )
    return root
}

// Subcommand group: myapp orders [create|list|cancel]
func newOrdersCmd() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "orders",
        Short: "Manage orders",
    }
    cmd.AddCommand(
        newOrdersCreateCmd(),
        newOrdersListCmd(),
        newOrdersCancelCmd(),
    )
    return cmd
}
```

---

## 2. Flag Patterns

```go
func newOrdersCreateCmd() *cobra.Command {
    var opts struct {
        customerID string
        items      []string // "product-uuid:quantity"
        dryRun     bool
        wait       bool
        timeout    time.Duration
    }

    cmd := &cobra.Command{
        Use:   "create",
        Short: "Create a new order",
        Example: `
  # Create order with single item
  myapp orders create --customer-id abc123 --item prod-uuid:2

  # Dry run
  myapp orders create --customer-id abc123 --item prod-uuid:2 --dry-run`,
        Args: cobra.NoArgs,
        RunE: func(cmd *cobra.Command, args []string) error {
            return runCreateOrder(cmd.Context(), cmd, opts)
        },
    }

    f := cmd.Flags()
    f.StringVar(&opts.customerID, "customer-id", "", "customer UUID (required)")
    f.StringArrayVar(&opts.items, "item", nil, "item as product-id:quantity (repeatable)")
    f.BoolVar(&opts.dryRun, "dry-run", false, "preview without creating")
    f.BoolVar(&opts.wait, "wait", false, "wait for order confirmation")
    f.DurationVar(&opts.timeout, "timeout", 30*time.Second, "operation timeout")

    // Mark required flags explicitly
    _ = cmd.MarkFlagRequired("customer-id")
    _ = cmd.MarkFlagRequired("item")

    return cmd
}
```

---

## 3. Output Formatter

```go
// Support multiple output formats: table (human), json (pipe-friendly), yaml
type Formatter interface {
    Print(w io.Writer, data any) error
}

type OutputFormat string
const (
    OutputTable OutputFormat = "table"
    OutputJSON  OutputFormat = "json"
    OutputYAML  OutputFormat = "yaml"
)

func NewFormatter(format string, noColor bool) (Formatter, error) {
    switch OutputFormat(format) {
    case OutputTable: return &TableFormatter{NoColor: noColor}, nil
    case OutputJSON:  return &JSONFormatter{Indent: true}, nil
    case OutputYAML:  return &YAMLFormatter{}, nil
    default:          return nil, fmt.Errorf("unknown output format %q (table|json|yaml)", format)
    }
}

type TableFormatter struct{ NoColor bool }

func (f *TableFormatter) Print(w io.Writer, data any) error {
    tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
    switch v := data.(type) {
    case []OrderRow:
        if !f.NoColor { fmt.Fprintln(tw, bold("ID\tSTATUS\tCUSTOMER\tTOTAL\tCREATED")) }
        for _, row := range v {
            fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
                row.ID, colorStatus(row.Status, f.NoColor),
                row.Customer, row.Total, row.CreatedAt.Format("2006-01-02 15:04"))
        }
    }
    return tw.Flush()
}

type JSONFormatter struct{ Indent bool }
func (f *JSONFormatter) Print(w io.Writer, data any) error {
    enc := json.NewEncoder(w)
    if f.Indent { enc.SetIndent("", "  ") }
    return enc.Encode(data)
}
```

---

## 4. Exit Codes

```go
// Standard exit codes (follow sysexits.h convention)
const (
    ExitOK      = 0  // success
    ExitError   = 1  // general error
    ExitUsage   = 2  // bad usage / invalid flags
    ExitTimeout = 3  // operation timed out
    ExitNotFound = 4 // resource not found
    ExitConflict = 5 // optimistic lock / conflict
    ExitAuth    = 6  // authentication/authorization error
)

type ExitError struct {
    Code    int
    Message string
    Err     error
}
func (e *ExitError) Error() string { return e.Message }
func (e *ExitError) Unwrap() error { return e.Err }

func exitFor(err error) *ExitError {
    switch {
    case errors.Is(err, context.DeadlineExceeded): return &ExitError{Code: ExitTimeout, Message: "operation timed out", Err: err}
    case errors.Is(err, domain.ErrNotFound):       return &ExitError{Code: ExitNotFound, Message: err.Error(), Err: err}
    case errors.Is(err, domain.ErrConflict):       return &ExitError{Code: ExitConflict, Message: err.Error(), Err: err}
    case errors.Is(err, domain.ErrUnauthorized):   return &ExitError{Code: ExitAuth, Message: "unauthorized", Err: err}
    default:                                        return &ExitError{Code: ExitError, Message: err.Error(), Err: err}
    }
}
```

---

## 5. Progress & Spinner

```go
// For long-running operations: show progress, not just silence
import "github.com/briandowns/spinner"

func withSpinner(msg string, fn func() error) error {
    if !isTerminal(os.Stdout) {
        // In pipes/scripts: no spinner — just print message
        fmt.Println(msg + "...")
        return fn()
    }
    s := spinner.New(spinner.CharSets[14], 100*time.Millisecond)
    s.Suffix = " " + msg
    s.Start()
    err := fn()
    s.Stop()
    if err != nil {
        fmt.Fprintf(os.Stderr, "✗ %s: %v\n", msg, err)
    } else {
        fmt.Printf("✓ %s\n", msg)
    }
    return err
}

func isTerminal(f *os.File) bool {
    fi, err := f.Stat()
    if err != nil { return false }
    return fi.Mode()&os.ModeCharDevice != 0
}
```

---

## 6. Shell Completion

```go
func newCompletionCmd(root *cobra.Command) *cobra.Command {
    return &cobra.Command{
        Use:   "completion [bash|zsh|fish|powershell]",
        Short: "Generate shell completion scripts",
        Long: `To load completions:
  Bash:  source <(myapp completion bash)
  Zsh:   echo ". <(myapp completion zsh)" >> ~/.zshrc
  Fish:  myapp completion fish | source`,
        ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
        Args: cobra.ExactArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            switch args[0] {
            case "bash":        return root.GenBashCompletion(os.Stdout)
            case "zsh":         return root.GenZshCompletion(os.Stdout)
            case "fish":        return root.GenFishCompletion(os.Stdout, true)
            case "powershell":  return root.GenPowerShellCompletion(os.Stdout)
            }
            return fmt.Errorf("unsupported shell: %s", args[0])
        },
    }
}
```

---

## CLI Checklist

- [ ] `SilenceUsage: true` — don't show usage on runtime errors
- [ ] `SilenceErrors: true` — print errors ourselves for consistent formatting
- [ ] Errors written to `stderr`, data to `stdout` — safe for piping
- [ ] JSON output mode (`--output json`) for scriptability
- [ ] Exit codes are meaningful and documented
- [ ] Required flags use `MarkFlagRequired` — not runtime checks
- [ ] `--dry-run` flag on any destructive command
- [ ] Shell completion generated via `completion` subcommand
- [ ] Spinner only when stdout is a terminal
- [ ] Context with timeout on all network operations
- [ ] `version` subcommand with build info (version, commit, date)
