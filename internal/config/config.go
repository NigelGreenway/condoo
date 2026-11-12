// Package config decides where Condoo keeps its files and whose list it is.
//
// Precedence for each value: environment variable, then config.json, then the default.
//
//	name  CONDOO_NAME  > config "name" > the login name (DOMAIN\ stripped on Windows)
//	db    CONDOO_DB    > config "db"   > the OS data folder (see DefaultDB)
//	file  CONDOO_CONFIG > the OS config folder (see DefaultConfigFile)
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// Env reads an environment variable; tests pass their own.
type Env func(string) string

// System describes the machine the paths are for, so they can be tested for any OS.
type System struct {
	GOOS string // "linux", "darwin", "windows", ...
	Home string
	Env  Env
}

// File is config.json. Empty fields mean "use the default".
type File struct {
	Name string `json:"name,omitempty"`
	DB   string `json:"db,omitempty"`
}

// Resolved is what the CLI runs with, and what `condoo config` prints.
type Resolved struct {
	Name       string `json:"name"`
	NameSource string `json:"name_source"` // env | config | login
	DB         string `json:"db"`
	DBSource   string `json:"db_source"` // env | config | default
	ConfigFile string `json:"config"`
}

// DefaultConfigFile: where config.json lives on each OS.
//
//	linux    $XDG_CONFIG_HOME/condoo/config.json (default ~/.config/condoo)
//	darwin   ~/Library/Application Support/Condoo/config.json
//	windows  %APPDATA%\Condoo\config.json
func DefaultConfigFile(s System) string {
	switch s.GOOS {
	case "darwin":
		return filepath.Join(s.Home, "Library", "Application Support", "Condoo", "config.json")
	case "windows":
		return filepath.Join(orDefault(s.Env("APPDATA"), filepath.Join(s.Home, "AppData", "Roaming")), "Condoo", "config.json")
	default:
		return filepath.Join(xdg(s, "XDG_CONFIG_HOME", ".config"), "condoo", "config.json")
	}
}

// DefaultDB: where the database lives on each OS. On Windows it's LocalAppData, not the
// roaming folder: an SQLite file shouldn't be synced between machines.
//
//	linux    $XDG_DATA_HOME/condoo/condoo.sqlite (default ~/.local/share/condoo)
//	darwin   ~/Library/Application Support/Condoo/condoo.sqlite
//	windows  %LOCALAPPDATA%\Condoo\condoo.sqlite
func DefaultDB(s System) string {
	switch s.GOOS {
	case "darwin":
		return filepath.Join(s.Home, "Library", "Application Support", "Condoo", "condoo.sqlite")
	case "windows":
		return filepath.Join(orDefault(s.Env("LOCALAPPDATA"), filepath.Join(s.Home, "AppData", "Local")), "Condoo", "condoo.sqlite")
	default:
		return filepath.Join(xdg(s, "XDG_DATA_HOME", filepath.Join(".local", "share")), "condoo", "condoo.sqlite")
	}
}

// AnswerDir: a private folder for answer drafts (the user's cache folder, not a shared /tmp).
func AnswerDir(s System) string {
	switch s.GOOS {
	case "darwin":
		return filepath.Join(s.Home, "Library", "Caches", "Condoo", "answers")
	case "windows":
		return filepath.Join(orDefault(s.Env("LOCALAPPDATA"), filepath.Join(s.Home, "AppData", "Local")), "Condoo", "answers")
	default:
		return filepath.Join(xdg(s, "XDG_CACHE_HOME", ".cache"), "condoo", "answers")
	}
}

// StripDomain turns a Windows account name like `CORP\nigel` into `nigel`.
func StripDomain(name string) string {
	if i := strings.LastIndex(name, `\`); i >= 0 {
		return name[i+1:]
	}
	return name
}

// LoginName is the account's login name, from the OS, else the usual variables.
func LoginName(env Env) string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return StripDomain(u.Username)
	}
	for _, k := range []string{"USER", "USERNAME", "LOGNAME"} {
		if v := env(k); v != "" {
			return StripDomain(v)
		}
	}
	return "me"
}

// ConfigFile: CONDOO_CONFIG, else the OS default.
func ConfigFile(s System) string {
	return orDefault(s.Env("CONDOO_CONFIG"), DefaultConfigFile(s))
}

// Read loads config.json; a missing file is an empty config.
func Read(path string) (File, error) {
	var f File
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// Write saves config.json atomically (temp file + rename).
func Write(path string, f File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Resolve works out the name and database from env, config.json and defaults.
// login is called only when nothing else names the user.
func Resolve(s System, login func() string) (Resolved, error) {
	r := Resolved{ConfigFile: ConfigFile(s)}
	f, err := Read(r.ConfigFile)
	if err != nil {
		return r, err
	}
	switch {
	case s.Env("CONDOO_NAME") != "":
		r.Name, r.NameSource = s.Env("CONDOO_NAME"), "env"
	case f.Name != "":
		r.Name, r.NameSource = f.Name, "config"
	default:
		r.Name, r.NameSource = login(), "login"
	}
	switch {
	case s.Env("CONDOO_DB") != "":
		r.DB, r.DBSource = expand(s, s.Env("CONDOO_DB")), "env"
	case f.DB != "":
		r.DB, r.DBSource = expand(s, f.DB), "config"
	default:
		r.DB, r.DBSource = DefaultDB(s), "default"
	}
	return r, nil
}

// expand turns a leading ~ into the home folder, so config.json can say "~/notes/condoo.sqlite".
func expand(s System, p string) string {
	if p == "~" {
		return s.Home
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.Join(s.Home, p[2:])
	}
	return p
}

func xdg(s System, key, fallback string) string {
	// The XDG spec ignores relative values.
	if v := s.Env(key); v != "" && filepath.IsAbs(v) {
		return v
	}
	return filepath.Join(s.Home, fallback)
}

func orDefault(v, d string) string {
	if v != "" {
		return v
	}
	return d
}
