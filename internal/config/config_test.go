package config

import (
	"os"
	"path/filepath"
	"testing"
)

func env(m map[string]string) Env { return func(k string) string { return m[k] } }

func TestDefaultPaths(t *testing.T) {
	home := filepath.Join("/", "home", "ada")
	root := t.TempDir() // absolute on every OS, for the XDG cases
	xdgDir := func(p string) string { return filepath.Join(root, p) }
	cases := []struct {
		name       string
		sys        System
		config, db string
		answers    string
	}{
		{
			name:    "linux defaults",
			sys:     System{GOOS: "linux", Home: home, Env: env(nil)},
			config:  filepath.Join(home, ".config", "condoo", "config.json"),
			db:      filepath.Join(home, ".local", "share", "condoo", "condoo.sqlite"),
			answers: filepath.Join(home, ".cache", "condoo", "answers"),
		},
		{
			name: "linux XDG",
			sys: System{GOOS: "linux", Home: home, Env: env(map[string]string{
				"XDG_CONFIG_HOME": xdgDir("config"), "XDG_DATA_HOME": xdgDir("data"), "XDG_CACHE_HOME": xdgDir("cache")})},
			config:  filepath.Join(xdgDir("config"), "condoo", "config.json"),
			db:      filepath.Join(xdgDir("data"), "condoo", "condoo.sqlite"),
			answers: filepath.Join(xdgDir("cache"), "condoo", "answers"),
		},
		{
			name: "linux ignores relative XDG values",
			sys: System{GOOS: "linux", Home: home, Env: env(map[string]string{
				"XDG_CONFIG_HOME": "relative", "XDG_DATA_HOME": "rel/data"})},
			config:  filepath.Join(home, ".config", "condoo", "config.json"),
			db:      filepath.Join(home, ".local", "share", "condoo", "condoo.sqlite"),
			answers: filepath.Join(home, ".cache", "condoo", "answers"),
		},
		{
			name:    "freebsd follows XDG like linux",
			sys:     System{GOOS: "freebsd", Home: home, Env: env(nil)},
			config:  filepath.Join(home, ".config", "condoo", "config.json"),
			db:      filepath.Join(home, ".local", "share", "condoo", "condoo.sqlite"),
			answers: filepath.Join(home, ".cache", "condoo", "answers"),
		},
		{
			name:    "macOS",
			sys:     System{GOOS: "darwin", Home: home, Env: env(map[string]string{"XDG_DATA_HOME": "/ignored"})},
			config:  filepath.Join(home, "Library", "Application Support", "Condoo", "config.json"),
			db:      filepath.Join(home, "Library", "Application Support", "Condoo", "condoo.sqlite"),
			answers: filepath.Join(home, "Library", "Caches", "Condoo", "answers"),
		},
		{
			name: "windows",
			sys: System{GOOS: "windows", Home: home, Env: env(map[string]string{
				"APPDATA": "/Users/ada/AppData/Roaming", "LOCALAPPDATA": "/Users/ada/AppData/Local"})},
			config:  filepath.Join("/Users/ada/AppData/Roaming", "Condoo", "config.json"),
			db:      filepath.Join("/Users/ada/AppData/Local", "Condoo", "condoo.sqlite"),
			answers: filepath.Join("/Users/ada/AppData/Local", "Condoo", "answers"),
		},
		{
			name:    "windows without the variables",
			sys:     System{GOOS: "windows", Home: home, Env: env(nil)},
			config:  filepath.Join(home, "AppData", "Roaming", "Condoo", "config.json"),
			db:      filepath.Join(home, "AppData", "Local", "Condoo", "condoo.sqlite"),
			answers: filepath.Join(home, "AppData", "Local", "Condoo", "answers"),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DefaultConfigFile(c.sys); got != c.config {
				t.Errorf("config: got %s, want %s", got, c.config)
			}
			if got := DefaultDB(c.sys); got != c.db {
				t.Errorf("db: got %s, want %s", got, c.db)
			}
			if got := AnswerDir(c.sys); got != c.answers {
				t.Errorf("answers: got %s, want %s", got, c.answers)
			}
		})
	}
}

func TestStripDomain(t *testing.T) {
	for in, want := range map[string]string{
		`CORP\nigel`:         "nigel",
		`DESKTOP-1A2B\Ada`:   "Ada",
		`a\b\c`:              "c",
		"nigel":              "nigel",
		"":                   "",
		"first.last@example": "first.last@example",
	} {
		if got := StripDomain(in); got != want {
			t.Errorf("StripDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoginNameIsNeverEmpty(t *testing.T) {
	if LoginName(env(nil)) == "" {
		t.Fatal("empty login name")
	}
}

func TestResolvePrecedence(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	login := func() string { return "loginname" }
	vars := map[string]string{"CONDOO_CONFIG": cfgPath}
	sys := System{GOOS: "linux", Home: dir, Env: env(vars)}

	r, err := Resolve(sys, login)
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "loginname" || r.NameSource != "login" || r.DBSource != "default" || r.DB != DefaultDB(sys) {
		t.Fatalf("no config: %+v", r)
	}

	if err := Write(cfgPath, File{Name: "Ada", DB: "~/notes/c.sqlite"}); err != nil {
		t.Fatal(err)
	}
	r, _ = Resolve(sys, login)
	if r.Name != "Ada" || r.NameSource != "config" {
		t.Errorf("config name: %+v", r)
	}
	if want := filepath.Join(dir, "notes", "c.sqlite"); r.DB != want || r.DBSource != "config" {
		t.Errorf("config db: got %s (%s), want %s", r.DB, r.DBSource, want)
	}

	vars["CONDOO_NAME"], vars["CONDOO_DB"] = "Grace", "/env/db.sqlite"
	r, _ = Resolve(sys, login)
	if r.Name != "Grace" || r.NameSource != "env" || r.DB != "/env/db.sqlite" || r.DBSource != "env" {
		t.Errorf("env wins: %+v", r)
	}
}

func TestResolveLoginOnlyWhenNeeded(t *testing.T) {
	sys := System{GOOS: "linux", Home: t.TempDir(), Env: env(map[string]string{"CONDOO_NAME": "Ada"})}
	_, err := Resolve(sys, func() string { t.Fatal("login looked up"); return "" })
	if err != nil {
		t.Fatal(err)
	}
}

func TestReadInvalidConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(p, []byte("{nope"), 0o644)
	if _, err := Read(p); err == nil {
		t.Fatal("want an error for invalid JSON")
	}
}

func TestWriteCreatesFolderAndRoundTrips(t *testing.T) {
	p := filepath.Join(t.TempDir(), "deep", "er", "config.json")
	if err := Write(p, File{Name: "Ada"}); err != nil {
		t.Fatal(err)
	}
	f, err := Read(p)
	if err != nil || f.Name != "Ada" || f.DB != "" {
		t.Fatalf("got %+v, %v", f, err)
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(p), ".config-*"))
	if len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}
