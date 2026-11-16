// Package config loads and validates the LANventory YAML configuration.
//
// Only process-level settings live here. Users, categories and assets are
// stored in the database and managed through the web UI or the command line.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultPath is used when neither --config nor LANVENTORY_CONFIG is set.
const DefaultPath = "/etc/lanventory/config.yaml"

// DefaultNumberFormat is the default inventory number format.
const DefaultNumberFormat = "{prefix}-{seq:05}"

// Config is the root of config.yaml.
type Config struct {
	// Listen is the HTTP listen address, e.g. "127.0.0.1:9060".
	Listen string `yaml:"listen"`
	// PublicURL is the externally visible base URL (scheme, host and port, no
	// path). If it starts with https://, session cookies are marked Secure.
	// Requests whose Origin header differs from it are rejected.
	PublicURL string `yaml:"public_url"`
	// TrustedProxies are the proxy IPs or networks whose X-Real-IP /
	// X-Forwarded-For headers are trusted for client IPs (lockout, audit log).
	TrustedProxies []string `yaml:"trusted_proxies"`
	// SecretKeyFile is the file with the AES-256 key that encrypts secrets in
	// the database (64 hex characters). It is created if missing.
	SecretKeyFile string `yaml:"secret_key_file"`

	// Storage configures the SQLite database.
	Storage Storage `yaml:"storage"`
	// Log configures logging.
	Log Log `yaml:"log"`
	// TLS enables the built-in TLS listener; usually a reverse proxy does this.
	TLS TLS `yaml:"tls"`
	// Inventory configures inventory behavior (number format of generated inventory numbers).
	Inventory Inventory `yaml:"inventory"`
	// Organization describes who runs this instance; it appears on printed
	// inspection protocols.
	Organization Organization `yaml:"organization"`
	// API configures the REST API under /api/v1.
	API API `yaml:"api"`

	// dir is the directory of the loaded file; relative paths resolve against it.
	dir string
}

// Storage configures the SQLite database.
type Storage struct {
	// Path is the SQLite database file. Its directory is created if missing.
	Path string `yaml:"path"`
}

// Log configures log/slog output.
type Log struct {
	// Level is the minimum log level: debug, info, warn or error.
	Level string `yaml:"level" enum:"debug,info,warn,error"`
	// Format is the log output format: text or json.
	Format string `yaml:"format" enum:"text,json"`
}

// TLS enables the built-in TLS listener. Usually a reverse proxy does this.
type TLS struct {
	// CertFile is the PEM certificate (chain). Set together with tls.key_file.
	CertFile string `yaml:"cert_file"`
	// KeyFile is the PEM private key.
	KeyFile string `yaml:"key_file"`
}

// Inventory configures inventory behavior.
type Inventory struct {
	// NumberFormat is the template of automatically assigned inventory
	// numbers: {prefix} is the category prefix, {seq:05} the running number
	// with the given minimum width. Without {prefix} (for example "{seq:04}"
	// for 0001, 0002, …) all categories share one running number; a category
	// without prefix drops {prefix} and the separator after it.
	NumberFormat string `yaml:"number_format"`
}

// Organization describes the operator of this instance.
type Organization struct {
	// Name is printed in the header of inspection protocols, for example the
	// name of your association or company. Optional; empty prints no name.
	Name string `yaml:"name"`
}

// API configures the REST API.
type API struct {
	// RateLimit is the sustained number of API requests per second for each
	// API token (browser sessions are limited per user). Excess requests get
	// HTTP 429.
	RateLimit float64 `yaml:"rate_limit"`
	// RateBurst is the number of API requests a token may send at once
	// before RateLimit applies.
	RateBurst int `yaml:"rate_burst"`
}

// Default returns a configuration with all defaults applied.
func Default() *Config {
	c := &Config{}
	c.applyDefaults()
	return c
}

func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:9060"
	}
	if c.PublicURL == "" {
		c.PublicURL = "http://" + c.Listen
	}
	c.PublicURL = strings.TrimRight(c.PublicURL, "/")
	if c.TrustedProxies == nil {
		c.TrustedProxies = []string{"127.0.0.1", "::1"}
	}
	if c.SecretKeyFile == "" {
		c.SecretKeyFile = "secret.key"
	}
	if c.Storage.Path == "" {
		c.Storage.Path = "data/lanventory.db"
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Log.Format == "" {
		c.Log.Format = "text"
	}
	if c.Inventory.NumberFormat == "" {
		c.Inventory.NumberFormat = DefaultNumberFormat
	}
	if c.API.RateLimit == 0 {
		c.API.RateLimit = 10
	}
	if c.API.RateBurst == 0 {
		c.API.RateBurst = 30
	}
}

// Load reads and validates the file at path. Environment variables override
// a few values (see ApplyEnv).
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	c := &Config{}
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	c.dir = filepath.Dir(abs)
	c.ApplyEnv(os.Getenv)
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return c, nil
}

// ApplyEnv overrides values from LANVENTORY_* environment variables. It is
// useful for containers: LANVENTORY_LISTEN, LANVENTORY_PUBLIC_URL,
// LANVENTORY_LOG_LEVEL.
func (c *Config) ApplyEnv(getenv func(string) string) {
	if v := getenv("LANVENTORY_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := getenv("LANVENTORY_PUBLIC_URL"); v != "" {
		c.PublicURL = v
	}
	if v := getenv("LANVENTORY_LOG_LEVEL"); v != "" {
		c.Log.Level = v
	}
}

// Validate checks value ranges and combinations.
func (c *Config) Validate() error {
	var errs []error
	u, err := url.Parse(c.PublicURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, fmt.Errorf("public_url %q: want http(s)://host[:port]", c.PublicURL))
	} else if u.Path != "" || u.RawQuery != "" {
		errs = append(errs, fmt.Errorf("public_url %q: LANventory must be served at the root of a host, without a path", c.PublicURL))
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("log.level %q: want debug, info, warn or error", c.Log.Level))
	}
	if c.Log.Format != "text" && c.Log.Format != "json" {
		errs = append(errs, fmt.Errorf("log.format %q: want text or json", c.Log.Format))
	}
	if (c.TLS.CertFile == "") != (c.TLS.KeyFile == "") {
		errs = append(errs, errors.New("tls.cert_file and tls.key_file must be set together"))
	}
	if err := ValidateNumberFormat(c.Inventory.NumberFormat); err != nil {
		errs = append(errs, fmt.Errorf("inventory.number_format: %w", err))
	}
	if n := strings.TrimSpace(c.Organization.Name); len([]rune(n)) > 200 || strings.ContainsAny(n, "\r\n\t") {
		errs = append(errs, errors.New("organization.name: at most 200 characters, a single line"))
	}
	if c.API.RateLimit < 0 || c.API.RateBurst < 0 {
		errs = append(errs, errors.New("api.rate_limit and api.rate_burst must not be negative"))
	}
	return errors.Join(errs...)
}

// ValidateNumberFormat checks an inventory number template: it must contain
// {seq} or {seq:0N} (so that numbers are unique) and may contain {prefix}.
func ValidateNumberFormat(f string) error {
	rest := f
	seq := false
	for {
		i := strings.IndexByte(rest, '{')
		if i < 0 {
			break
		}
		j := strings.IndexByte(rest[i:], '}')
		if j < 0 {
			return errors.New("unclosed {")
		}
		tok := rest[i+1 : i+j]
		switch {
		case tok == "prefix":
		case tok == "seq":
			seq = true
		case strings.HasPrefix(tok, "seq:0") && len(tok) > 5 && len(tok) <= 7 && isDigits(tok[5:]):
			seq = true
		default:
			return fmt.Errorf("unknown placeholder {%s}", tok)
		}
		rest = rest[i+j+1:]
	}
	if !seq {
		return errors.New("must contain {seq} or {seq:0N}")
	}
	return nil
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// SecureCookies reports whether session cookies get the Secure flag.
func (c *Config) SecureCookies() bool {
	return strings.HasPrefix(c.PublicURL, "https://")
}

// Origin returns the origin of the public URL ("https://host:port"), which
// state-changing requests must send in their Origin header.
func (c *Config) Origin() string {
	u, err := url.Parse(c.PublicURL)
	if err != nil {
		return c.PublicURL
	}
	return u.Scheme + "://" + u.Host
}

// ResolvePath makes p absolute relative to the config file's directory.
func (c *Config) ResolvePath(p string) string {
	if p == "" || filepath.IsAbs(p) || c.dir == "" {
		return p
	}
	return filepath.Join(c.dir, p)
}

// Example is written by "lanventory config init".
const Example = `# LANventory configuration
# Users, categories and assets live in the database and are managed
# in the web UI or with the command line.

# HTTP listen address. Behind a reverse proxy, bind to localhost only.
listen: 127.0.0.1:9060

# Public URL (scheme, host, port – no path). With https://, session
# cookies are marked Secure. Must match what browsers see.
public_url: http://127.0.0.1:9060

# Proxies whose X-Real-IP/X-Forwarded-For headers are trusted.
trusted_proxies: [127.0.0.1, "::1"]

# AES-256 key for secrets in the database (created if missing).
# Relative paths are relative to this file.
secret_key_file: secret.key

storage:
  path: data/lanventory.db    # SQLite database file

log:
  level: info                 # debug, info, warn, error
  format: text                # text or json

# Number format of generated inventory numbers.
inventory:
  number_format: "{prefix}-{seq:05}"   # {prefix} = category prefix (optional), {seq:05} = running number; "{seq:04}" gives 0001, 0002, …

# Printed in the header of inspection protocols (optional).
organization:
  name: ""

# REST API (/api/v1): rate limit per API token.
api:
  rate_limit: 10              # requests per second
  rate_burst: 30              # requests allowed at once

# Built-in TLS (otherwise use a reverse proxy):
# tls:
#   cert_file: /etc/lanventory/tls.crt
#   key_file: /etc/lanventory/tls.key
`
