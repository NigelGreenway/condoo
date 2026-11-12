package cli

import (
	"fmt"
	"slices"
	"strings"
)

// flag is one option; every option takes a value (`--x v` or `--x=v`).
type flag struct {
	key     string   // the name it's stored under
	names   []string // "--summary", "-s"
	choices []string // allowed values, if fixed
}

// parse splits args into positionals and options. Options and positionals may come in any
// order (`ask "title" -s ... --agent ...`); `--` ends options. Present-but-empty options
// (`--space=`) are kept, so callers can tell "set to empty" from "not given".
func parse(args []string, flags []flag) (pos []string, opts map[string]string, err error) {
	byName := map[string]flag{}
	for _, f := range flags {
		for _, n := range f.names {
			byName[n] = f
		}
	}
	opts = map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			pos = append(pos, a)
			continue
		}
		name, value, inline := strings.Cut(a, "=")
		f, ok := byName[name]
		if !ok {
			return nil, nil, fmt.Errorf("unknown option %s", name)
		}
		if !inline {
			if i+1 >= len(args) {
				return nil, nil, fmt.Errorf("%s needs a value", name)
			}
			i++
			value = args[i]
		}
		if f.choices != nil && !slices.Contains(f.choices, value) {
			return nil, nil, fmt.Errorf("%s must be one of %s", name, strings.Join(f.choices, ", "))
		}
		opts[f.key] = value
	}
	return pos, opts, nil
}

// splitCommand splits an $EDITOR value into words, honouring double and single quotes, so
// `code --wait` and `"C:\Program Files\Vim\vim.exe"` both work.
func splitCommand(s string) []string {
	var (
		out   []string
		cur   strings.Builder
		quote rune
		has   bool
	)
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, has = r, true
		case r == ' ' || r == '\t':
			if has || cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
		}
	}
	if has || cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}
