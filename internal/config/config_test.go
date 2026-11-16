package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExampleLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(Example), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("example config does not load: %v", err)
	}
	if c.Listen != "127.0.0.1:9060" || c.Inventory.NumberFormat != DefaultNumberFormat {
		t.Fatalf("unexpected values: %+v", c)
	}
	if got := c.ResolvePath(c.Storage.Path); got != filepath.Join(filepath.Dir(path), "data/lanventory.db") {
		t.Fatalf("storage path not resolved relative to the file: %s", got)
	}
}

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]func(*Config){
		"public_url with path":  func(c *Config) { c.PublicURL = "https://example.org/inv" },
		"public_url bad scheme": func(c *Config) { c.PublicURL = "ftp://example.org" },
		"tls cert without key":  func(c *Config) { c.TLS.CertFile = "a.crt" },
		"log level":             func(c *Config) { c.Log.Level = "verbose" },
		"log format":            func(c *Config) { c.Log.Format = "xml" },
		"number format no seq":  func(c *Config) { c.Inventory.NumberFormat = "{prefix}" },
		"number format unknown": func(c *Config) { c.Inventory.NumberFormat = "{foo}-{seq}" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := Default()
			mutate(c)
			if err := c.Validate(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestNumberFormat(t *testing.T) {
	for _, f := range []string{"{seq}", "{prefix}-{seq:05}", "LV{seq:04}"} {
		if err := ValidateNumberFormat(f); err != nil {
			t.Errorf("%q: %v", f, err)
		}
	}
}

func TestEnvOverrides(t *testing.T) {
	c := Default()
	c.ApplyEnv(func(k string) string {
		return map[string]string{"LANVENTORY_PUBLIC_URL": "https://inv.example.org", "LANVENTORY_LOG_LEVEL": "debug"}[k]
	})
	if !c.SecureCookies() || c.Origin() != "https://inv.example.org" || c.Log.Level != "debug" {
		t.Fatalf("env not applied: %+v", c)
	}
}

// docs/config.example.yaml (shipped in the packages) must match Example.
func TestDocsExampleInSync(t *testing.T) {
	data, err := os.ReadFile("../../docs/config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != Example {
		t.Fatal("docs/config.example.yaml differs from config.Example; run: lanventory -c /tmp/x.yaml config init && cp /tmp/x.yaml docs/config.example.yaml")
	}
}

func TestOrganizationName(t *testing.T) {
	c := Default()
	c.Organization.Name = "Example Club"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Organization.Name = "two\nlines"
	if c.Validate() == nil {
		t.Error("multi-line organization name accepted")
	}
	c.Organization.Name = strings.Repeat("x", 201)
	if c.Validate() == nil {
		t.Error("overlong organization name accepted")
	}
}
