package config

import (
	"errors"
	"os"
	"path/filepath"
)

// BaseDir is where factory keeps factory.json, its adapters, the credentials
// file and the access token: one directory, on every platform.
//
// It is deliberately not os.UserConfigDir. That returns
// ~/Library/Application Support on macOS, which meant factory.json sat in
// ~/.config/factory — where the README's quick start puts you, and where
// `factory init` writes it — while the credentials and the token landed
// somewhere else entirely. The credentials were therefore not "beside
// factory.json" as documented, and every command other than `init` reported
// "no config found" unless you happened to be standing in that directory.
//
// $XDG_CONFIG_HOME still wins when set. That is exactly what
// os.UserConfigDir already does on Linux, so Linux behaviour is unchanged;
// this only makes macOS agree with Linux and with the documentation.
func BaseDir() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		if !filepath.IsAbs(dir) {
			return "", errors.New("path in $XDG_CONFIG_HOME is relative")
		}
		return filepath.Join(dir, "factory"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "factory"), nil
}

// TokenPath is where the generated web access token is stored, beside
// factory.json. The caller creates it with mode 0600.
func TokenPath() (string, error) {
	dir, err := BaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "token"), nil
}
