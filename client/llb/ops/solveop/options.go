package solveop

// SolveOption configures a nested solve scope.
type SolveOption func(*SolveInfo)

// WithSecret binds a secret for the inner solve.
func WithSecret(id, value string) SolveOption {
	return func(si *SolveInfo) {
		si.Secrets = append(si.Secrets, SecretBinding{ID: id, Value: value})
	}
}

// WithSecretEnv binds a secret as an environment variable for the inner solve.
func WithSecretEnv(id, value string) SolveOption {
	return func(si *SolveInfo) {
		si.Secrets = append(si.Secrets, SecretBinding{ID: id, Value: value, IsEnv: true})
	}
}

// WithSSH binds an SSH agent for the inner solve.
func WithSSH(id string, socket ...string) SolveOption {
	return func(si *SolveInfo) {
		sock := ""
		if len(socket) > 0 {
			sock = socket[0]
		}
		si.SSHBindings = append(si.SSHBindings, SSHBinding{ID: id, Socket: sock})
	}
}

// WithEnv sets an environment variable for the inner solve.
func WithEnv(key, value string) SolveOption {
	return func(si *SolveInfo) {
		si.Env[key] = value
	}
}

// WithFrontend selects the frontend for parsing the inner definition.
func WithFrontend(name string) SolveOption {
	return func(si *SolveInfo) {
		si.Frontend = name
	}
}

// WithFrontendAttr sets a frontend-specific attribute.
func WithFrontendAttr(key, value string) SolveOption {
	return func(si *SolveInfo) {
		si.FrontendAttrs[key] = value
	}
}

// WithCacheImport configures a cache import source for the inner solve.
func WithCacheImport(cacheType string, attrs map[string]string) SolveOption {
	return func(si *SolveInfo) {
		si.CacheImports = append(si.CacheImports, CacheConfig{Type: cacheType, Attrs: attrs})
	}
}

// WithCacheExport configures a cache export destination for the inner solve.
func WithCacheExport(cacheType string, attrs map[string]string) SolveOption {
	return func(si *SolveInfo) {
		si.CacheExports = append(si.CacheExports, CacheConfig{Type: cacheType, Attrs: attrs})
	}
}

// WithLocalSource makes a named local directory available to the inner solve.
func WithLocalSource(name, dir string) SolveOption {
	return func(si *SolveInfo) {
		si.LocalSources[name] = dir
	}
}

// WithOCISource makes a named OCI layout directory available to the inner solve.
func WithOCISource(name, dir string) SolveOption {
	return func(si *SolveInfo) {
		si.OCISources[name] = dir
	}
}

// WithConstraints applies build constraints to the solve operation.
func WithConstraints(co llb.ConstraintsOpt) SolveOption {
	return func(si *SolveInfo) {
		// The SolveInfo doesn't directly embed Constraints.
		// This is handled at the SolveOp level.
	}
}
