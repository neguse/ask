package ask

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config is the per-user configuration. It lives outside any repository so
// that every working repo can use ask without setup (PRD §3).
//
// The file is the flat TOML subset ask itself writes: `key = value` lines
// with string or integer values, and # comments. No external TOML parser;
// the binary stays dependency-free.
type Config struct {
	Version             int
	Driver              string
	Inbox               string // "owner/repo"
	Responder           string
	PollIntervalSeconds int
}

// ConfigPath returns os.UserConfigDir()/ask/config.toml. UserConfigDir is
// used instead of a literal ~/.config so the same binary works on Windows.
func ConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user config directory: %w", err)
	}
	return filepath.Join(dir, "ask", "config.toml"), nil
}

// LoadConfig reads ConfigPath. A missing file is an error telling the user
// to run `ask init`. PollIntervalSeconds defaults to 10 when omitted.
func LoadConfig() (Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return Config{}, err
	}
	cfg, err := loadConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("ask config not found at %q; run `ask init`", path)
	}
	if err != nil {
		return Config{}, fmt.Errorf("load ask config %q: %w", path, err)
	}
	return cfg, nil
}

// Save writes the config, creating parent directories as needed.
func (c Config) Save() error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	if err := saveConfig(path, c); err != nil {
		return fmt.Errorf("save ask config %q: %w", path, err)
	}
	return nil
}

func loadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()

	cfg, err := parseConfig(f)
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func parseConfig(r io.Reader) (Config, error) {
	cfg := Config{PollIntervalSeconds: 10}
	seen := make(map[string]struct{})

	scanner := bufio.NewScanner(r)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(stripConfigComment(scanner.Text()))
		if line == "" {
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return Config{}, fmt.Errorf("line %d: expected key = value", lineNumber)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			return Config{}, fmt.Errorf("line %d: expected key = value", lineNumber)
		}
		if _, ok := seen[key]; ok {
			return Config{}, fmt.Errorf("line %d: duplicate key %q", lineNumber, key)
		}
		seen[key] = struct{}{}

		var err error
		switch key {
		case "version":
			cfg.Version, err = parseConfigInt(value)
		case "driver":
			cfg.Driver, err = parseConfigString(value)
		case "inbox":
			cfg.Inbox, err = parseConfigString(value)
		case "responder":
			cfg.Responder, err = parseConfigString(value)
		case "poll_interval_seconds":
			cfg.PollIntervalSeconds, err = parseConfigInt(value)
		default:
			return Config{}, fmt.Errorf("line %d: unknown key %q", lineNumber, key)
		}
		if err != nil {
			return Config{}, fmt.Errorf("line %d: invalid value for %q: %w", lineNumber, key, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	return cfg, nil
}

func parseConfigInt(value string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("expected bare integer: %w", err)
	}
	return n, nil
}

func parseConfigString(value string) (string, error) {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return "", errors.New("expected double-quoted string")
	}
	s, err := strconv.Unquote(value)
	if err != nil {
		return "", fmt.Errorf("invalid double-quoted string: %w", err)
	}
	return s, nil
}

func stripConfigComment(line string) string {
	inString := false
	escaped := false
	for i, r := range line {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch r {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch r {
		case '"':
			inString = true
		case '#':
			return line[:i]
		}
	}
	return line
}

func saveConfig(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	contents := fmt.Sprintf(
		"version = %d\ndriver = %s\ninbox = %s\nresponder = %s\npoll_interval_seconds = %d\n",
		cfg.Version,
		strconv.Quote(cfg.Driver),
		strconv.Quote(cfg.Inbox),
		strconv.Quote(cfg.Responder),
		cfg.PollIntervalSeconds,
	)
	return os.WriteFile(path, []byte(contents), 0o600)
}
