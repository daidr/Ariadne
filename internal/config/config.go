package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Home         string
	DatabasePath string
	Host         string
	Port         int
	Token        string
	Open         bool
	FirstRun     bool
}

func (config Config) Address() string {
	return net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
}

// Load preserves the existing environment variables and data layout. A token
// passed through the environment is never written to disk.
func Load(lookup func(string) (string, bool)) (Config, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	home, exists := lookup("ARIADNE_HOME")
	if !exists {
		user, err := os.UserHomeDir()
		if err != nil {
			return Config{}, err
		}
		home = filepath.Join(user, ".ariadne")
	}
	if home == "" {
		return Config{}, errors.New("ARIADNE_HOME must not be empty")
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return Config{}, err
	}
	database := filepath.Join(home, "ariadne.sqlite")
	_, err := os.Stat(database)
	firstRun := errors.Is(err, fs.ErrNotExist)
	if err != nil && !firstRun {
		return Config{}, err
	}
	port := 7428
	if given, exists := lookup("ARIADNE_PORT"); exists {
		port, err = strconv.Atoi(given)
		if err != nil || port < 1 || port > 65535 {
			return Config{}, fmt.Errorf("invalid ARIADNE_PORT: %q", given)
		}
	}
	token, exists := lookup("ARIADNE_TOKEN")
	if !exists {
		token, err = loadToken(home)
		if err != nil {
			return Config{}, err
		}
	}
	if token == "" {
		return Config{}, errors.New("ARIADNE_TOKEN must not be empty")
	}
	open, _ := lookup("ARIADNE_OPEN")
	return Config{Home: home, DatabasePath: database, Host: "127.0.0.1", Port: port, Token: token, Open: open == "1", FirstRun: firstRun}, nil
}

func loadToken(home string) (string, error) {
	path := filepath.Join(home, "token")
	data, err := os.ReadFile(path)
	if err == nil && strings.TrimSpace(string(data)) != "" {
		return strings.TrimSpace(string(data)), nil
	}
	existingEmpty := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(bytes)
	// An empty token file is treated as first use, as in the original backend.
	if existingEmpty {
		if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
			return "", err
		}
		return token, os.Chmod(path, 0o600)
	}
	// Atomic creation avoids two starting servers retaining different tokens.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if token := strings.TrimSpace(string(data)); token != "" {
			return token, nil
		}
		return "", errors.New("token file is empty")
	}
	if err != nil {
		return "", err
	}
	if _, err := file.WriteString(token + "\n"); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return token, os.Chmod(path, 0o600)
}
