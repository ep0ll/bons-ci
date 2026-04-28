package overlay

type config struct {
	whiteoutPrefix string
	opaqueMarker   string
	hooks          InterpreterHooks
}

func defaultConfig() config {
	return config{
		whiteoutPrefix: defaultWhiteoutPrefix,
		opaqueMarker:   defaultOpaqueMarker,
		hooks:          InterpreterHooks{},
	}
}

// Option configures overlay interpretation behavior.
type Option func(*config)

// WithWhiteoutPrefix configures a custom whiteout prefix.
func WithWhiteoutPrefix(prefix string) Option {
	return func(c *config) {
		c.whiteoutPrefix = prefix
	}
}

// WithOpaqueMarker configures a custom opaque directory marker.
func WithOpaqueMarker(marker string) Option {
	return func(c *config) {
		c.opaqueMarker = marker
	}
}

// WithInterpreterHooks registers lifecycle hooks for the interpreter.
func WithInterpreterHooks(hooks InterpreterHooks) Option {
	return func(c *config) {
		c.hooks = hooks
	}
}
