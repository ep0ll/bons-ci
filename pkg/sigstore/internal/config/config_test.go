package config_test

import (
	"testing"

	"github.com/bons/bons-ci/pkg/sigstore/internal/config"
)

func TestValidate_KeylessBackend(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid keyless config",
			cfg: config.Config{
				Server: config.ServerConfig{Addr: ":8080"},
				Signing: config.SigningConfig{
					Backend: "keyless",
					Keyless: config.KeylessConfig{
						FulcioURL:  "https://fulcio.sigstore.dev",
						RekorURL:   "https://rekor.sigstore.dev",
						OIDCIssuer: "https://accounts.google.com",
					},
				},
				Observability: config.ObservabilityConfig{ServiceName: "svc"},
			},
			wantErr: false,
		},
		{
			name: "missing fulcio_url",
			cfg: config.Config{
				Server: config.ServerConfig{Addr: ":8080"},
				Signing: config.SigningConfig{
					Backend: "keyless",
					Keyless: config.KeylessConfig{
						RekorURL:   "https://rekor.sigstore.dev",
						OIDCIssuer: "https://accounts.google.com",
					},
				},
				Observability: config.ObservabilityConfig{ServiceName: "svc"},
			},
			wantErr: true,
			errMsg:  "fulcio_url",
		},
		{
			name: "missing rekor_url",
			cfg: config.Config{
				Server: config.ServerConfig{Addr: ":8080"},
				Signing: config.SigningConfig{
					Backend: "keyless",
					Keyless: config.KeylessConfig{
						FulcioURL:  "https://fulcio.sigstore.dev",
						OIDCIssuer: "https://accounts.google.com",
					},
				},
				Observability: config.ObservabilityConfig{ServiceName: "svc"},
			},
			wantErr: true,
			errMsg:  "rekor_url",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := config.Validate(&tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && tt.errMsg != "" {
				if !containsStr(err.Error(), tt.errMsg) {
					t.Errorf("error %q does not mention %q", err.Error(), tt.errMsg)
				}
			}
		})
	}
}

func TestValidate_StaticKeyBackend(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.Config
		wantErr bool
	}{
		{
			name: "valid static_key",
			cfg: config.Config{
				Server: config.ServerConfig{Addr: ":8080"},
				Signing: config.SigningConfig{
					Backend:   "static_key",
					StaticKey: config.StaticKeyConfig{KeyPath: "/secrets/key.pem"},
				},
				Observability: config.ObservabilityConfig{ServiceName: "svc"},
			},
			wantErr: false,
		},
		{
			name: "static_key missing key_path",
			cfg: config.Config{
				Server:        config.ServerConfig{Addr: ":8080"},
				Signing:       config.SigningConfig{Backend: "static_key"},
				Observability: config.ObservabilityConfig{ServiceName: "svc"},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := config.Validate(&tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidate_UnknownBackend(t *testing.T) {
	cfg := config.Config{
		Server:        config.ServerConfig{Addr: ":8080"},
		Signing:       config.SigningConfig{Backend: "magic"},
		Observability: config.ObservabilityConfig{ServiceName: "svc"},
	}
	err := config.Validate(&cfg)
	if err == nil {
		t.Fatal("expected error for unknown backend, got nil")
	}
	if !containsStr(err.Error(), "unknown") {
		t.Errorf("error %q does not mention 'unknown'", err.Error())
	}
}

func TestValidate_MissingServerAddr(t *testing.T) {
	cfg := config.Config{
		Signing:       config.SigningConfig{Backend: "static_key", StaticKey: config.StaticKeyConfig{KeyPath: "/k"}},
		Observability: config.ObservabilityConfig{ServiceName: "svc"},
	}
	err := config.Validate(&cfg)
	if err == nil {
		t.Fatal("expected error for missing server.addr, got nil")
	}
}

func TestObservabilityConfig_SlogLevel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"debug", "DEBUG"},
		{"info", "INFO"},
		{"warn", "WARN"},
		{"error", "ERROR"},
		{"INFO", "INFO"},
		{"", "INFO"},        // default
		{"invalid", "INFO"}, // default
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			cfg := config.ObservabilityConfig{LogLevel: tt.input}
			got := cfg.SlogLevel().String()
			if got != tt.want {
				t.Errorf("SlogLevel(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func containsStr(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
