// Package config reads the server's runtime settings from environment variables. Rule values (fees,
// windows, limits) are NOT here: they live in rulebook.json. This is only how the process is run.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr           string
	DataDir        string
	JWTSecret      string
	TokenTTL       time.Duration
	AdminEmail     string
	AdminPassword  string
	AdminName      string
	AllowSignup    bool
	AllowedOrigins []string
	RulebookPath   string
	UniversePath   string
	Autostart      bool
	LogLevel       string
	// WebDir, if set, serves the built web app from disk instead of the copy embedded in the binary.
	WebDir string
}

// LoadEnvFile sets variables from a KEY=VALUE file without overriding ones already in the environment.
// A missing file is fine.
func LoadEnvFile(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("config: %s: line %q is not KEY=VALUE", path, line)
		}
		k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`)
		if _, set := os.LookupEnv(k); !set {
			_ = os.Setenv(k, v)
		}
	}
	return sc.Err()
}

func get(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func getBool(key string, def bool) (bool, error) {
	v := get(key, "")
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("config: %s must be true or false, got %q", key, v)
	}
	return b, nil
}

// FromEnv builds and validates the configuration.
func FromEnv() (Config, error) {
	c := Config{
		Addr:          get("ADDR", "127.0.0.1:8080"),
		DataDir:       get("DATA_DIR", "./data"),
		JWTSecret:     get("JWT_SECRET", ""),
		AdminEmail:    get("ADMIN_EMAIL", ""),
		AdminPassword: get("ADMIN_PASSWORD", ""),
		AdminName:     get("ADMIN_NAME", "Organiser"),
		RulebookPath:  get("RULEBOOK_PATH", ""),
		UniversePath:  get("UNIVERSE_PATH", ""),
		LogLevel:      get("LOG_LEVEL", "info"),
		WebDir:        get("WEB_DIR", ""),
	}
	for _, o := range strings.Split(get("ALLOWED_ORIGINS", "http://localhost:3000,http://127.0.0.1:3000"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			c.AllowedOrigins = append(c.AllowedOrigins, o)
		}
	}
	var err error
	if c.AllowSignup, err = getBool("ALLOW_SIGNUP", true); err != nil {
		return c, err
	}
	if c.Autostart, err = getBool("AUTOSTART", false); err != nil {
		return c, err
	}
	hours, err := strconv.Atoi(get("TOKEN_TTL_HOURS", "12"))
	if err != nil || hours < 1 || hours > 72 {
		return c, errors.New("config: TOKEN_TTL_HOURS must be a whole number from 1 to 72")
	}
	c.TokenTTL = time.Duration(hours) * time.Hour
	if len(c.JWTSecret) < 32 {
		return c, errors.New("config: JWT_SECRET is required and must be at least 32 characters")
	}
	if (c.AdminEmail == "") != (c.AdminPassword == "") {
		return c, errors.New("config: set both ADMIN_EMAIL and ADMIN_PASSWORD, or neither")
	}
	if c.AdminPassword != "" && len(c.AdminPassword) < 12 {
		return c, errors.New("config: ADMIN_PASSWORD must be at least 12 characters")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return c, fmt.Errorf("config: LOG_LEVEL must be debug, info, warn or error, got %q", c.LogLevel)
	}
	return c, nil
}
