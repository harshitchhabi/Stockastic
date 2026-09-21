package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stockastic/api/internal/config"
)

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range []string{"ADDR", "DATA_DIR", "JWT_SECRET", "ADMIN_EMAIL", "ADMIN_PASSWORD", "ALLOW_SIGNUP", "AUTOSTART", "TOKEN_TTL_HOURS", "LOG_LEVEL", "ALLOWED_ORIGINS"} {
		t.Setenv(k, "")
	}
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func TestSecretIsRequiredAndMustBeLong(t *testing.T) {
	setEnv(t, nil)
	if _, err := config.FromEnv(); err == nil || !strings.Contains(err.Error(), "JWT_SECRET") {
		t.Fatalf("err = %v", err)
	}
	setEnv(t, map[string]string{"JWT_SECRET": "short"})
	if _, err := config.FromEnv(); err == nil {
		t.Fatal("a short secret was accepted")
	}
}

func TestDefaultsAreSafe(t *testing.T) {
	setEnv(t, map[string]string{"JWT_SECRET": strings.Repeat("s", 40)})
	c, err := config.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.Addr, "127.0.0.1") {
		t.Fatalf("the default address %q is reachable from other machines", c.Addr)
	}
	if c.Autostart {
		t.Fatal("the event clock auto-starts by default")
	}
}

func TestAdminCredentialsComeAsAPairAndAreStrong(t *testing.T) {
	base := map[string]string{"JWT_SECRET": strings.Repeat("s", 40)}
	for name, extra := range map[string]map[string]string{
		"email only":     {"ADMIN_EMAIL": "a@b.c"},
		"password only":  {"ADMIN_PASSWORD": "long-enough-password"},
		"weak password":  {"ADMIN_EMAIL": "a@b.c", "ADMIN_PASSWORD": "short"},
		"bad bool":       {"AUTOSTART": "maybe"},
		"bad log level":  {"LOG_LEVEL": "loud"},
		"bad token life": {"TOKEN_TTL_HOURS": "0"},
	} {
		kv := map[string]string{}
		for k, v := range base {
			kv[k] = v
		}
		for k, v := range extra {
			kv[k] = v
		}
		setEnv(t, kv)
		if _, err := config.FromEnv(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestEnvFileDoesNotOverrideTheRealEnvironment(t *testing.T) {
	setEnv(t, map[string]string{"JWT_SECRET": strings.Repeat("e", 40)})
	path := filepath.Join(t.TempDir(), ".env.local")
	_ = os.WriteFile(path, []byte("# comment\nJWT_SECRET=from-file-should-lose\nADMIN_NAME=\"From File\"\n\n"), 0o600)
	os.Unsetenv("ADMIN_NAME")
	t.Cleanup(func() { os.Unsetenv("ADMIN_NAME") })
	if err := config.LoadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	c, err := config.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.JWTSecret != strings.Repeat("e", 40) {
		t.Fatal("the file overrode the environment")
	}
	if c.AdminName != "From File" {
		t.Fatalf("AdminName = %q", c.AdminName)
	}
	if err := config.LoadEnvFile(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatalf("a missing env file is fine, got %v", err)
	}
}
