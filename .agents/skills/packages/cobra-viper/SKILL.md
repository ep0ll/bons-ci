---
name: pkg-cobra-viper
description: >
  Exhaustive reference for spf13/cobra + spf13/viper: command hierarchy, flag binding to viper,
  persistent flags, required flags, subcommand patterns, config file loading, env var binding,
  shell completion, and testing cobra commands. Primary CLI framework. Cross-references:
  cli/SKILL.md, configuration/SKILL.md.
---

# Package: spf13/cobra + spf13/viper — Complete Reference

## Import
```go
import (
    "github.com/spf13/cobra"
    "github.com/spf13/viper"
)
```

## 1. Root Command Setup

```go
// cmd/root.go
var rootCmd = &cobra.Command{
    Use:           "myapp",
    Short:         "My application",
    SilenceUsage:  true,   // suppress usage on runtime errors (only show on flag errors)
    SilenceErrors: true,   // control error printing ourselves
    PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
        return initConfig(cmd)
    },
}

func Execute() error {
    return rootCmd.Execute()
}

func init() {
    rootCmd.PersistentFlags().StringP("config", "c", "", "config file path")
    rootCmd.PersistentFlags().StringP("output", "o", "table", "output format: table|json")
    rootCmd.PersistentFlags().BoolP("verbose", "v", false, "verbose output")
    rootCmd.PersistentFlags().Bool("no-color", false, "disable color output")

    // Bind persistent flags to viper
    _ = viper.BindPFlag("output", rootCmd.PersistentFlags().Lookup("output"))
    _ = viper.BindPFlag("verbose", rootCmd.PersistentFlags().Lookup("verbose"))
}

func initConfig(cmd *cobra.Command) error {
    cfgFile, _ := cmd.Flags().GetString("config")
    if cfgFile != "" {
        viper.SetConfigFile(cfgFile)
    } else {
        viper.AddConfigPath(".")
        viper.AddConfigPath("$HOME/.myapp")
        viper.SetConfigName("config")
        viper.SetConfigType("yaml")
    }

    viper.SetEnvPrefix("MYAPP")
    viper.AutomaticEnv()
    viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))

    if err := viper.ReadInConfig(); err != nil {
        if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
            return fmt.Errorf("config: %w", err)
        }
    }
    return nil
}
```

## 2. Subcommands

```go
// cmd/orders.go
func newOrdersCmd() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "orders",
        Short: "Manage orders",
    }
    cmd.AddCommand(newOrdersListCmd(), newOrdersCreateCmd(), newOrdersGetCmd())
    return cmd
}

func newOrdersListCmd() *cobra.Command {
    var flags struct {
        status string
        limit  int
        output string
    }

    cmd := &cobra.Command{
        Use:   "list",
        Short: "List orders",
        Example: `  myapp orders list --status=pending
  myapp orders list --limit=50 --output=json`,
        RunE: func(cmd *cobra.Command, args []string) error {
            // Get values: prefer viper (env/config override flags)
            output := viper.GetString("output")
            return runOrdersList(cmd.Context(), flags.status, flags.limit, output)
        },
    }

    f := cmd.Flags()
    f.StringVar(&flags.status, "status", "", "filter by status (pending|confirmed|shipped)")
    f.IntVar(&flags.limit, "limit", 20, "max results (1-100)")

    // Local flag bound to viper (env can override)
    _ = viper.BindPFlag("orders.default_limit", f.Lookup("limit"))

    return cmd
}

func newOrdersCreateCmd() *cobra.Command {
    var flags struct {
        customerID string
        items      []string
        dryRun     bool
    }

    cmd := &cobra.Command{
        Use:   "create",
        Short: "Create a new order",
        Args:  cobra.NoArgs,
        RunE: func(cmd *cobra.Command, args []string) error {
            return runOrdersCreate(cmd.Context(), flags)
        },
    }

    f := cmd.Flags()
    f.StringVar(&flags.customerID, "customer-id", "", "customer UUID")
    f.StringArrayVar(&flags.items, "item", nil, "product-id:quantity (repeatable)")
    f.BoolVar(&flags.dryRun, "dry-run", false, "preview without creating")

    _ = cmd.MarkFlagRequired("customer-id")
    _ = cmd.MarkFlagRequired("item")

    // Custom validation on flag value
    _ = cmd.RegisterFlagCompletionFunc("status", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
        return []string{"pending", "confirmed", "shipped", "cancelled"}, cobra.ShellCompDirectiveDefault
    })

    return cmd
}
```

## 3. Flag ↔ Viper Binding

```go
// Precedence (highest to lowest):
// 1. explicit flag set by user
// 2. MYAPP_KEY env var
// 3. config file value
// 4. viper.SetDefault()
// 5. flag default

// Bind ALL persistent flags at root init
func bindFlags(cmd *cobra.Command, v *viper.Viper) {
    cmd.Flags().VisitAll(func(f *pflag.Flag) {
        if !f.Changed {
            if v.IsSet(f.Name) {
                val := v.Get(f.Name)
                _ = cmd.Flags().Set(f.Name, fmt.Sprintf("%v", val))
            }
        }
        _ = v.BindPFlag(f.Name, cmd.Flags().Lookup(f.Name))
    })
}
```

## 4. Reading Viper Config in Commands

```go
// After initConfig runs in PersistentPreRunE
func runOrdersList(ctx context.Context, status string, limit int, output string) error {
    // Read from viper (env/config file override flags)
    apiURL := viper.GetString("api.url")          // MYAPP_API_URL env
    apiKey := viper.GetString("api.key")          // MYAPP_API_KEY env (from secret)
    timeout := viper.GetDuration("api.timeout")   // MYAPP_API_TIMEOUT or config file

    if apiURL == "" {
        return fmt.Errorf("api.url not configured (set MYAPP_API_URL or api.url in config)")
    }
    // ...
}
```

## 5. Testing Cobra Commands

```go
func TestOrdersListCmd(t *testing.T) {
    t.Parallel()

    // Reset viper for each test
    v := viper.New()
    v.Set("api.url", "http://mock-api:8080")

    // Create fresh command tree for each test
    root := newRootCmd(v)

    // Capture output
    var buf bytes.Buffer
    root.SetOut(&buf)
    root.SetErr(&buf)

    // Execute
    root.SetArgs([]string{"orders", "list", "--status=pending", "--output=json"})
    err := root.Execute()

    require.NoError(t, err)
    assert.Contains(t, buf.String(), `"status":"pending"`)
}
```

## 6. Shell Completion

```go
// Register completion in root command init
rootCmd.AddCommand(&cobra.Command{
    Use:   "completion [bash|zsh|fish|powershell]",
    Short: "Generate shell completion script",
    Args:  cobra.ExactArgs(1),
    ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
    RunE: func(cmd *cobra.Command, args []string) error {
        switch args[0] {
        case "bash":       return rootCmd.GenBashCompletion(os.Stdout)
        case "zsh":        return rootCmd.GenZshCompletion(os.Stdout)
        case "fish":       return rootCmd.GenFishCompletion(os.Stdout, true)
        case "powershell": return rootCmd.GenPowerShellCompletionWithDesc(os.Stdout)
        }
        return fmt.Errorf("unsupported shell: %q", args[0])
    },
})
```

## cobra/viper Checklist
- [ ] `SilenceUsage: true` — usage only shown for flag errors
- [ ] `SilenceErrors: true` — errors printed in main, not cobra internals
- [ ] `PersistentPreRunE` used for config initialization — runs for all subcommands
- [ ] Required flags use `MarkFlagRequired` — not runtime string checks
- [ ] Viper prefix set (`SetEnvPrefix`) — prevents collision with other tools
- [ ] Flag values read from viper (not directly from flag) — respects env/config override
- [ ] Tests reset viper state between test cases
- [ ] Shell completion registered via `completion` subcommand
- [ ] `--dry-run` flag on all destructive commands
- [ ] Version command with build info (version, commit, build date)
