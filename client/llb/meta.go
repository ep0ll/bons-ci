package llb

import (
	"context"
	"fmt"
	"net"
	"path"

	"github.com/moby/buildkit/solver/pb"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

// ─────────────────────────────────────────────────────────────────────────────
// Context keys
// ─────────────────────────────────────────────────────────────────────────────

type contextKeyT string

var (
	keyArgs      = contextKeyT("llb.exec.args")
	keyDir       = contextKeyT("llb.exec.dir")
	keyEnv       = contextKeyT("llb.exec.env")
	keyExtraHost = contextKeyT("llb.exec.extrahost")
	keyHostname  = contextKeyT("llb.exec.hostname")
	keyUlimit    = contextKeyT("llb.exec.ulimit")
	keyUser      = contextKeyT("llb.exec.user")
	keyCgroup    = contextKeyT("llb.exec.cgroup")

	keyPlatform = contextKeyT("llb.platform")
	keyNetwork  = contextKeyT("llb.network")
	keySecurity = contextKeyT("llb.security")
)

// ─────────────────────────────────────────────────────────────────────────────
// AddEnv
// ─────────────────────────────────────────────────────────────────────────────

// AddEnvOption returns a StateOption that adds an environment variable.
func AddEnvOption(key, value string) StateOption {
	return addEnv(key, value)
}

// AddEnvfOption returns a StateOption that adds a formatted env var.
func AddEnvfOption(key, value string, v ...any) StateOption {
	return addEnvf(key, fmt.Sprintf(value, v...))
}

func addEnv(key, value string) StateOption {
	return func(s State) State {
		return s.withValue(keyEnv, func(ctx context.Context, c *Constraints) (any, error) {
			env, err := getEnvVal(s)(ctx, c)
			if err != nil {
				return nil, err
			}
			return env.Set(key, value), nil
		})
	}
}

func addEnvf(key, value string, v ...any) StateOption {
	if len(v) > 0 {
		value = fmt.Sprintf(value, v...)
	}
	return addEnv(key, value)
}

func getEnvVal(s State) func(context.Context, *Constraints) (*EnvList, error) {
	return func(ctx context.Context, c *Constraints) (*EnvList, error) {
		v, err := s.GetValue(ctx, keyEnv)
		if err != nil {
			return nil, err
		}
		if v == nil {
			return NewEnvList(), nil
		}
		return v.(*EnvList), nil
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Dir
// ─────────────────────────────────────────────────────────────────────────────

// DirOption returns a StateOption that sets the working directory.
func DirOption(str string) StateOption {
	return dir(str)
}

// DirsOption returns a StateOption that sets a formatted working directory.
func DirfOption(str string, v ...any) StateOption {
	return dirf(str, v...)
}

func dir(value string) StateOption {
	return func(s State) State {
		return s.withValue(keyDir, func(ctx context.Context, c *Constraints) (any, error) {
			if !path.IsAbs(value) {
				prev, err := getDirVal(s)(ctx, c)
				if err != nil {
					return nil, err
				}
				if prev != "" {
					value = path.Join(prev, value)
				}
			}
			return value, nil
		})
	}
}

func dirf(value string, v ...any) StateOption {
	if len(v) > 0 {
		value = fmt.Sprintf(value, v...)
	}
	return dir(value)
}

func getDirVal(s State) func(context.Context, *Constraints) (string, error) {
	return func(ctx context.Context, c *Constraints) (string, error) {
		v, err := s.GetValue(ctx, keyDir)
		if err != nil {
			return "", err
		}
		if v == nil {
			return "", nil
		}
		return v.(string), nil
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// User
// ─────────────────────────────────────────────────────────────────────────────

// UserOption returns a StateOption that sets the user.
func UserOption(user string) StateOption {
	return setUser(user)
}

func setUser(user string) StateOption {
	return func(s State) State {
		return s.WithValue(keyUser, user)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Hostname
// ─────────────────────────────────────────────────────────────────────────────

// HostnameOption returns a StateOption that sets the hostname.
func HostnameOption(h string) StateOption {
	return hostname(h)
}

func hostname(h string) StateOption {
	return func(s State) State {
		return s.WithValue(keyHostname, h)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Platform
// ─────────────────────────────────────────────────────────────────────────────

// PlatformOption returns a StateOption that sets the target platform.
func PlatformOption(p ocispecs.Platform) StateOption {
	return setPlatform(p)
}

func setPlatform(p ocispecs.Platform) StateOption {
	return func(s State) State {
		return s.WithValue(keyPlatform, &p)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Network
// ─────────────────────────────────────────────────────────────────────────────

// NetworkOption returns a StateOption that sets the network mode.
func NetworkOption(mode pb.NetMode) StateOption {
	return func(s State) State {
		return s.WithValue(keyNetwork, mode)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Security
// ─────────────────────────────────────────────────────────────────────────────

// SecurityOption returns a StateOption that sets the security mode.
func SecurityOption(mode pb.SecurityMode) StateOption {
	return func(s State) State {
		return s.WithValue(keySecurity, mode)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Args
// ─────────────────────────────────────────────────────────────────────────────

// ArgsOption returns a StateOption that sets the exec args.
func ArgsOption(args ...string) StateOption {
	return func(s State) State {
		return s.WithValue(keyArgs, args)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ExtraHost
// ─────────────────────────────────────────────────────────────────────────────

// ExtraHostOption returns a StateOption that adds an extra /etc/hosts entry.
func ExtraHostOption(host string, ip net.IP) StateOption {
	return func(s State) State {
		return s.withValue(keyExtraHost, func(ctx context.Context, c *Constraints) (any, error) {
			v, err := s.GetValue(ctx, keyExtraHost)
			if err != nil {
				return nil, err
			}
			var hosts []HostIP
			if v != nil {
				hosts = v.([]HostIP)
			}
			return append(hosts, HostIP{Host: host, IP: ip}), nil
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Ulimit
// ─────────────────────────────────────────────────────────────────────────────

// UlimitName represents a ulimit resource name.
type UlimitName string

// UlimitOption returns a StateOption that adds a ulimit.
func UlimitOption(name UlimitName, soft, hard int64) StateOption {
	return func(s State) State {
		return s.withValue(keyUlimit, func(ctx context.Context, c *Constraints) (any, error) {
			v, err := s.GetValue(ctx, keyUlimit)
			if err != nil {
				return nil, err
			}
			var ulimits []*pb.Ulimit
			if v != nil {
				ulimits = v.([]*pb.Ulimit)
			}
			return append(ulimits, &pb.Ulimit{
				Name: string(name),
				Soft: soft,
				Hard: hard,
			}), nil
		})
	}
}

// CgroupParentOption returns a StateOption that sets the cgroup parent.
func CgroupParentOption(cp string) StateOption {
	return func(s State) State {
		return s.WithValue(keyCgroup, cp)
	}
}

// Reset returns a StateOption that resets the metadata to the given state
// while keeping the output of the current state.
func Reset(other State) StateOption {
	return func(s State) State {
		s = State{out: s.out, prev: &other}
		return s
	}
}
