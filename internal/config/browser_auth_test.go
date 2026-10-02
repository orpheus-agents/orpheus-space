package config

import (
	"testing"
	"time"
)

func TestBrowserSettings(t *testing.T) {
	base := BrowserAuth{Mode: "api_only", SessionTTL: 12 * time.Hour}
	if base.Mode != "api_only" {
		t.Fatal(base)
	}
	for _, edit := range []func(*BrowserAuth){
		func(c *BrowserAuth) { c.Mode = "none" }, func(c *BrowserAuth) { c.Mode = "saml" },
		func(c *BrowserAuth) { c.PublicURL = "https://example.test/" }, func(c *BrowserAuth) { c.PublicURL = "https://user:pass@example.test" },
		func(c *BrowserAuth) { c.PublicURL = "https://example.test?" }, func(c *BrowserAuth) { c.PublicURL = "https://example.test#" },
		func(c *BrowserAuth) { c.ttlSeconds = "1" }, func(c *BrowserAuth) { c.ttlSeconds = "99999999999999999999" }, func(c *BrowserAuth) { c.ttlSeconds = "invalid" },
	} {
		c := base
		edit(&c)
		if _, err := c.Validated(); err == nil {
			t.Fatal("accepted", c)
		}
	}
	c := base
	c.Mode = "anonymous"
	c.ttlSeconds = "600"
	c, err := c.Validated()
	if err != nil || c.SessionTTL != 10*time.Minute {
		t.Fatal(c, err)
	}

}

func TestBrowserPublicOriginCanonicalization(t *testing.T) {
	for input, want := range map[string]string{
		"https://HOST:443": "https://host", "http://LOCALHOST:80": "http://localhost",
		"https://HOST:8443": "https://host:8443", "http://[::1]:80": "http://[::1]",
		"https://[::1]:8443": "https://[::1]:8443",
	} {
		c := BrowserAuth{Mode: "api_only", SessionTTL: 12 * time.Hour}
		c.PublicURL = input
		c, err := c.Validated()
		if err != nil || c.PublicURL != want {
			t.Fatal(input, c.PublicURL, err)
		}
	}
}
