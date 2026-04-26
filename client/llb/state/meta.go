// Package state provides meta-key definitions and accessor helpers for the
// metadata value chain carried through an immutable State. This mirrors
// BuildKit's client/llb/meta.go.
package state

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/moby/buildkit/solver/pb"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

// ─── Context keys ────────────────────────────────────────────────────────────

type contextKeyT string

var (
	keyArgs           = contextKeyT("llb.exec.args")
	keyDir            = contextKeyT("llb.exec.dir")
	keyEnv            = contextKeyT("llb.exec.env")
	keyExtraHost      = contextKeyT("llb.exec.extrahost")
	keyHostname       = contextKeyT("llb.exec.hostname")
	keyCgroupParent   = contextKeyT("llb.exec.cgroupparent")
	keyUlimit         = contextKeyT("llb.exec.ulimit")
	keyUser           = contextKeyT("llb.exec.user")
	keyValidExitCodes = contextKeyT("llb.exec.validexitcodes")
	keyPlatform       = contextKeyT("llb.platform")
	keyNetwork        = contextKeyT("llb.network")
	keySecurity       = contextKeyT("llb.security")
)

// ─── StateOption ─────────────────────────────────────────────────────────────

// StateOption is a function that produces a new State from an existing one.
// StateOptions are used to compose metadata on the immutable State chain.
type StateOption func(State) State

// ─── EnvList ─────────────────────────────────────────────────────────────────

// EnvList represents an immutable, ordered list of environment variables.
type EnvList struct {
	mu   sync.RWMutex
	list []KeyValue
}

// KeyValue is a single key=value pair.
type KeyValue struct {
	Key   string
	Value string
}

// AddOrReplace returns a new EnvList with the key set (or replaced).
func (e *EnvList) AddOrReplace(key, value string) *EnvList {
	e.mu.RLock()
	oldList := slices.Clone(e.list)
	e.mu.RUnlock()

	newList := make([]KeyValue, 0, len(oldList)+1)
	replaced := false
	for _, kv := range oldList {
		if kv.Key == key {
			newList = append(newList, KeyValue{Key: key, Value: value})
			replaced = true
		} else {
			newList = append(newList, kv)
		}
	}
	if !replaced {
		newList = append(newList, KeyValue{Key: key, Value: value})
	}
	return &EnvList{list: newList}
}

// Get returns the value and true if found, or "" and false.
func (e *EnvList) Get(key string) (string, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for i := len(e.list) - 1; i >= 0; i-- {
		if e.list[i].Key == key {
			return e.list[i].Value, true
		}
	}
	return "", false
}

// Delete returns a new EnvList without the given key.
func (e *EnvList) Delete(key string) *EnvList {
	e.mu.RLock()
	oldList := slices.Clone(e.list)
	e.mu.RUnlock()
	newList := make([]KeyValue, 0, len(oldList))
	for _, kv := range oldList {
		if kv.Key != key {
			newList = append(newList, kv)
		}
	}
	return &EnvList{list: newList}
}

// ToSlice returns "KEY=VALUE" strings.
func (e *EnvList) ToSlice() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]string, len(e.list))
	for i, kv := range e.list {
		out[i] = kv.Key + "=" + kv.Value
	}
	return out
}

// Len returns the number of environment variables.
func (e *EnvList) Len() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.list)
}

// ─── HostIP ──────────────────────────────────────────────────────────────────

// HostIP represents a hostname-to-IP mapping for extra /etc/hosts entries.
type HostIP struct {
	Host string
	IP   net.IP
}

// ─── UlimitName ──────────────────────────────────────────────────────────────

// UlimitName defines the name of a ulimit resource.
type UlimitName string

// ─── Package-level StateOption constructors ──────────────────────────────────

// AddEnv returns a StateOption that adds or replaces an environment variable.
func AddEnv(key, value string) StateOption {
	return addEnvf(key, value, false)
}

// AddEnvf is the same as AddEnv but allows for format string expansion.
func AddEnvf(key, value string, v ...any) StateOption {
	return addEnvf(key, value, true, v...)
}

func addEnvf(key, value string, replace bool, v ...any) StateOption {
	if replace {
		value = fmt.Sprintf(value, v...)
	}
	return func(s State) State {
		return s.withValue(keyEnv, func(ctx context.Context, c *core.Constraints) (any, error) {
			env, err := getEnv(s)(ctx, c)
			if err != nil {
				return nil, err
			}
			return env.AddOrReplace(key, value), nil
		})
	}
}

// DelEnv returns a StateOption that removes an environment variable.
func DelEnv(key string) StateOption {
	return func(s State) State {
		return s.withValue(keyEnv, func(ctx context.Context, c *core.Constraints) (any, error) {
			env, err := getEnv(s)(ctx, c)
			if err != nil {
				return nil, err
			}
			return env.Delete(key), nil
		})
	}
}

// Dir returns a StateOption that sets the working directory.
func Dir(str string) StateOption {
	return dirf(str, false)
}

// Dirf is the same as Dir but allows for format string expansion.
func Dirf(str string, v ...any) StateOption {
	return dirf(str, true, v...)
}

func dirf(value string, replace bool, v ...any) StateOption {
	if replace {
		value = fmt.Sprintf(value, v...)
	}
	return func(s State) State {
		return s.withValue(keyDir, func(ctx context.Context, c *core.Constraints) (any, error) {
			if !path.IsAbs(value) {
				prev, err := getDir(s)(ctx, c)
				if err != nil {
					return nil, err
				}
				if prev == "" {
					prev = "/"
				}
				value = path.Join(prev, value)
			}
			return value, nil
		})
	}
}

// User returns a StateOption that sets the user.
func User(str string) StateOption {
	return func(s State) State {
		return s.WithValue(keyUser, str)
	}
}

// Hostname returns a StateOption that sets the hostname.
func Hostname(str string) StateOption {
	return func(s State) State {
		return s.withValue(keyHostname, func(ctx context.Context, c *core.Constraints) (any, error) {
			return str, nil
		})
	}
}

// Reset returns a StateOption that reparents the current state.
func Reset(other State) StateOption {
	return func(s State) State {
		ns := other
		ns.output = s.output
		return ns
	}
}

// Network returns a StateOption that sets the network mode.
func Network(v pb.NetMode) StateOption {
	return func(s State) State {
		return s.WithValue(keyNetwork, v)
	}
}

// Security returns a StateOption that sets the security mode.
func Security(v pb.SecurityMode) StateOption {
	return func(s State) State {
		return s.WithValue(keySecurity, v)
	}
}

// Args sets the command arguments on the state.
func Args(a []string) StateOption {
	return func(s State) State {
		return s.WithValue(keyArgs, a)
	}
}

// Shlexf splits a shell-command string and sets it as args.
func Shlexf(str string, v ...any) StateOption {
	return shlexfInternal(str, true, v...)
}

func shlexfInternal(str string, replace bool, v ...any) StateOption {
	if replace {
		str = fmt.Sprintf(str, v...)
	}
	return func(s State) State {
		return s.withValue(keyArgs, func(ctx context.Context, c *core.Constraints) (any, error) {
			parts := strings.Fields(str)
			if len(parts) == 0 {
				return nil, fmt.Errorf("shlexf: empty command")
			}
			return parts, nil
		})
	}
}

// ─── Internal getters ────────────────────────────────────────────────────────

func getEnv(s State) func(context.Context, *core.Constraints) (*EnvList, error) {
	return func(ctx context.Context, c *core.Constraints) (*EnvList, error) {
		v, err := s.Value(ctx, keyEnv, c)
		if err != nil {
			return nil, err
		}
		if v != nil {
			return v.(*EnvList), nil
		}
		return &EnvList{}, nil
	}
}

func getDir(s State) func(context.Context, *core.Constraints) (string, error) {
	return func(ctx context.Context, c *core.Constraints) (string, error) {
		v, err := s.Value(ctx, keyDir, c)
		if err != nil {
			return "", err
		}
		if v != nil {
			return v.(string), nil
		}
		return "", nil
	}
}

func getArgs(s State) func(context.Context, *core.Constraints) ([]string, error) {
	return func(ctx context.Context, c *core.Constraints) ([]string, error) {
		v, err := s.Value(ctx, keyArgs, c)
		if err != nil {
			return nil, err
		}
		if v != nil {
			return v.([]string), nil
		}
		return nil, nil
	}
}

func getUser(s State) func(context.Context, *core.Constraints) (string, error) {
	return func(ctx context.Context, c *core.Constraints) (string, error) {
		v, err := s.Value(ctx, keyUser, c)
		if err != nil {
			return "", err
		}
		if v != nil {
			return v.(string), nil
		}
		return "", nil
	}
}

func getHostname(s State) func(context.Context, *core.Constraints) (string, error) {
	return func(ctx context.Context, c *core.Constraints) (string, error) {
		v, err := s.Value(ctx, keyHostname, c)
		if err != nil {
			return "", err
		}
		if v != nil {
			return v.(string), nil
		}
		return "", nil
	}
}

func getNetwork(s State) func(context.Context, *core.Constraints) (pb.NetMode, error) {
	return func(ctx context.Context, c *core.Constraints) (pb.NetMode, error) {
		v, err := s.Value(ctx, keyNetwork, c)
		if err != nil {
			return 0, err
		}
		if v != nil {
			return v.(pb.NetMode), nil
		}
		return pb.NetMode_UNSET, nil
	}
}

func getSecurity(s State) func(context.Context, *core.Constraints) (pb.SecurityMode, error) {
	return func(ctx context.Context, c *core.Constraints) (pb.SecurityMode, error) {
		v, err := s.Value(ctx, keySecurity, c)
		if err != nil {
			return 0, err
		}
		if v != nil {
			return v.(pb.SecurityMode), nil
		}
		return pb.SecurityMode_SANDBOX, nil
	}
}

func getPlatform(s State) func(context.Context, *core.Constraints) (*ocispecs.Platform, error) {
	return func(ctx context.Context, c *core.Constraints) (*ocispecs.Platform, error) {
		v, err := s.Value(ctx, keyPlatform, c)
		if err != nil {
			return nil, err
		}
		if v != nil {
			p := v.(ocispecs.Platform)
			return &p, nil
		}
		return nil, nil
	}
}

func getExtraHosts(s State) func(context.Context, *core.Constraints) ([]HostIP, error) {
	return func(ctx context.Context, c *core.Constraints) ([]HostIP, error) {
		v, err := s.Value(ctx, keyExtraHost, c)
		if err != nil {
			return nil, err
		}
		if v != nil {
			return v.([]HostIP), nil
		}
		return nil, nil
	}
}

func getUlimit(s State) func(context.Context, *core.Constraints) ([]*pb.Ulimit, error) {
	return func(ctx context.Context, c *core.Constraints) ([]*pb.Ulimit, error) {
		v, err := s.Value(ctx, keyUlimit, c)
		if err != nil {
			return nil, err
		}
		if v != nil {
			return v.([]*pb.Ulimit), nil
		}
		return nil, nil
	}
}

func getCgroupParent(s State) func(context.Context, *core.Constraints) (string, error) {
	return func(ctx context.Context, c *core.Constraints) (string, error) {
		v, err := s.Value(ctx, keyCgroupParent, c)
		if err != nil {
			return "", err
		}
		if v != nil {
			return v.(string), nil
		}
		return "", nil
	}
}

func getValidExitCodes(s State) func(context.Context, *core.Constraints) ([]int, error) {
	return func(ctx context.Context, c *core.Constraints) ([]int, error) {
		v, err := s.Value(ctx, keyValidExitCodes, c)
		if err != nil {
			return nil, err
		}
		if v != nil {
			return v.([]int), nil
		}
		return nil, nil
	}
}

// ─── Extra host / ulimit / cgroup helpers ────────────────────────────────────

func extraHost(host string, ip net.IP) StateOption {
	return func(s State) State {
		return s.withValue(keyExtraHost, func(ctx context.Context, c *core.Constraints) (any, error) {
			prev, err := getExtraHosts(s)(ctx, c)
			if err != nil {
				return nil, err
			}
			return append(prev, HostIP{Host: host, IP: ip}), nil
		})
	}
}

func ulimit(name UlimitName, soft, hard int64) StateOption {
	return func(s State) State {
		return s.withValue(keyUlimit, func(ctx context.Context, c *core.Constraints) (any, error) {
			prev, err := getUlimit(s)(ctx, c)
			if err != nil {
				return nil, err
			}
			return append(prev, &pb.Ulimit{
				Name: string(name),
				Soft: soft,
				Hard: hard,
			}), nil
		})
	}
}

func cgroupParent(cp string) StateOption {
	return func(s State) State {
		return s.WithValue(keyCgroupParent, cp)
	}
}

func validExitCodes(codes ...int) StateOption {
	return func(s State) State {
		return s.WithValue(keyValidExitCodes, codes)
	}
}

// platform sets the internal platform value.
func setPlatform(p ocispecs.Platform) StateOption {
	return func(s State) State {
		return s.WithValue(keyPlatform, p)
	}
}

// ─── Parsing helpers ─────────────────────────────────────────────────────────

// ParseImageConfig parses an OCI image config JSON and returns env, user, dir,
// and platform metadata. This is used by State.WithImageConfig.
func ParseImageConfig(dt []byte) (env []string, user string, dir string, platform *ocispecs.Platform, err error) {
	type imgConfig struct {
		Env        []string `json:"Env"`
		WorkingDir string   `json:"WorkingDir"`
		User       string   `json:"User"`
	}
	type imgManifest struct {
		Config   imgConfig          `json:"config"`
		Platform *ocispecs.Platform `json:"platform,omitempty"`
	}
	var m imgManifest
	if err := json.Unmarshal(dt, &m); err != nil {
		return nil, "", "", nil, fmt.Errorf("parse image config: %w", err)
	}
	return m.Config.Env, m.Config.User, m.Config.WorkingDir, m.Platform, nil
}

