package devenv

import (
	"strings"
	"testing"
)

func TestConfigValidation(t *testing.T) {
	valid := Config{
		Commands: []string{"./cmd/hello"},
		Packages: []Package{{Name: "hello", RefName: "hello.example.com", Config: "config/hello", Images: []string{"hello"}}},
	}
	if err := valid.validate(); err != nil {
		t.Errorf("valid config: %v", err)
	}
	for _, tc := range []struct {
		want   string
		change func(*Config)
	}{
		{"no packages", func(c *Config) { c.Packages = nil }},
		{"a package has no name", func(c *Config) { c.Packages[0].Name = "" }},
		{`package hello has no RefName`, func(c *Config) { c.Packages[0].RefName = "" }},
		{`package hello has no Config`, func(c *Config) { c.Packages[0].Config = "" }},
		{`two packages are called hello`, func(c *Config) { c.Packages = append(c.Packages, c.Packages[0]) }},
	} {
		c := valid
		c.Packages = append([]Package(nil), valid.Packages...)
		tc.change(&c)
		if err := c.validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v", tc.want, err)
		}
	}
}
