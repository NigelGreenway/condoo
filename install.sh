#!/bin/sh
# Condoo installer for Linux and macOS (Windows: install.ps1).
#
#   sh install.sh                 from a checkout: builds with Go if you have it, else downloads
#   curl -fsSL https://github.com/NigelGreenway/condoo/releases/latest/download/install.sh | sh
#
# It asks before each step and never edits your shell files: if the folder you pick isn't on
# your PATH, it prints the line to add. Run with --help for the options.
set -eu

REPO="${CONDOO_REPO:-NigelGreenway/condoo}"
VERSION="latest"
BIN_DIR=""
NAME=""
HERDR_HOME="${HERDR_APP_HOME:-$HOME/.config/herdr-app}"
WANT_MOD=""
FROM=""          # source | release ("" = source if possible)
YES=0
UNINSTALL=0

usage() {
	cat <<EOF
Usage: install.sh [options]

  --bin-dir DIR      where the condoo link goes (default: ~/.local/bin)
  --name NAME        your name on the list (default: your login name)
  --herdr-home DIR   Herdr's folder (default: \$HERDR_APP_HOME or ~/.config/herdr-app)
  --no-mod           don't install the Herdr panel
  --from-source      build with Go from this checkout
  --from-release     download a release binary (default when not in a checkout)
  --version vX.Y.Z   the release to download (default: latest)
  --yes              accept every default without asking
  --uninstall        remove the binary, link and panel (keeps your list and settings)
  -h, --help         this help
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--bin-dir) BIN_DIR="$2"; shift 2 ;;
	--name) NAME="$2"; shift 2 ;;
	--herdr-home) HERDR_HOME="$2"; shift 2 ;;
	--no-mod) WANT_MOD=no; shift ;;
	--from-source) FROM=source; shift ;;
	--from-release) FROM=release; shift ;;
	--version) VERSION="$2"; shift 2 ;;
	--yes | -y) YES=1; shift ;;
	--uninstall) UNINSTALL=1; shift ;;
	-h | --help) usage; exit 0 ;;
	*) echo "Unknown option: $1" >&2; usage >&2; exit 2 ;;
	esac
done

# ---------- helpers ----------

say() { printf '%s\n' "$*"; }
step() { printf '\n\033[1m%s\033[0m\n' "$*"; }
die() { printf 'condoo install: %s\n' "$*" >&2; exit 1; }

# Prompts read the terminal even when this script arrives on a pipe (curl | sh).
TTY=""
if [ "$YES" = 0 ] && [ -r /dev/tty ] && (: </dev/tty) 2>/dev/null; then TTY=/dev/tty; fi

# ask "Question" "default" -> the answer (or the default with --yes / no terminal)
ask() {
	if [ -z "$TTY" ]; then printf '%s\n' "$2"; return; fi
	printf '%s [%s]: ' "$1" "$2" >"$TTY"
	read -r reply <"$TTY" || reply=""
	printf '%s\n' "${reply:-$2}"
}

# confirm "Question" y|n -> exit status
confirm() {
	a=$(ask "$1 (y/n)" "$2")
	case "$a" in [Yy]*) return 0 ;; *) return 1 ;; esac
}

expand_home() {
	case "$1" in
	"~") printf '%s\n' "$HOME" ;;
	"~/"*) printf '%s/%s\n' "$HOME" "${1#\~/}" ;;
	*) printf '%s\n' "$1" ;;
	esac
}

json_string() { printf '"%s"' "$(printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g')"; }

fetch() { # url file
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$1" -o "$2"
	elif command -v wget >/dev/null 2>&1; then
		wget -qO "$2" "$1"
	else
		die "need curl or wget to download"
	fi
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
	else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

release_url() {
	if [ "$VERSION" = latest ]; then
		printf 'https://github.com/%s/releases/latest/download/%s\n' "$REPO" "$1"
	else
		printf 'https://github.com/%s/releases/download/%s/%s\n' "$REPO" "$VERSION" "$1"
	fi
}

# ---------- where things go ----------

case "$(uname -s)" in
Linux) OS=linux; DATA_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/condoo" ;;
Darwin) OS=darwin; DATA_DIR="$HOME/Library/Application Support/Condoo" ;;
*) die "unsupported system $(uname -s): use install.ps1 on Windows" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) ARCH=amd64 ;;
aarch64 | arm64) ARCH=arm64 ;;
*) die "unsupported CPU $(uname -m)" ;;
esac

# The real binary lives in Condoo's own folder; the folder you choose gets a link to it,
# so upgrading replaces one file and the link keeps working.
REAL_BIN="$DATA_DIR/bin/condoo"
SRC_DIR=$(cd "$(dirname "$0")" 2>/dev/null && pwd || true)
IN_CHECKOUT=0
[ -n "$SRC_DIR" ] && [ -f "$SRC_DIR/go.mod" ] && [ -d "$SRC_DIR/cmd/condoo" ] && IN_CHECKOUT=1

# ---------- uninstall ----------

if [ "$UNINSTALL" = 1 ]; then
	step "Uninstalling Condoo"
	dir=$(expand_home "${BIN_DIR:-$(ask "Folder with the condoo link" "$HOME/.local/bin")}")
	if [ -L "$dir/condoo" ] && [ "$(readlink "$dir/condoo")" = "$REAL_BIN" ]; then
		rm "$dir/condoo" && say "Removed $dir/condoo"
	elif [ -e "$dir/condoo" ]; then
		say "Left $dir/condoo alone: it isn't a link to $REAL_BIN"
	fi
	info=""
	if [ -x "$REAL_BIN" ]; then
		info=$("$REAL_BIN" config 2>/dev/null || true)
		rm "$REAL_BIN" && say "Removed $REAL_BIN"
	fi
	if [ -d "$HERDR_HOME/mods/condoo" ] && confirm "Remove the Herdr panel ($HERDR_HOME/mods/condoo)?" y; then
		rm -r "$HERDR_HOME/mods/condoo" && say "Removed the panel"
	fi
	say ""
	say "Kept your list and settings; delete them yourself if you want them gone:"
	if [ -n "$info" ]; then
		say "  database: $(printf '%s' "$info" | sed -n 's/.*"db":"\([^"]*\)".*/\1/p')"
		say "  settings: $(printf '%s' "$info" | sed -n 's/.*"config":"\([^"]*\)".*/\1/p')"
	else
		say "  see the paths in the README (Where things live)"
	fi
	exit 0
fi

say "Condoo (Context + Todo): your agents' questions and requests, in one list."

# ---------- 1. the binary ----------

if [ -z "$FROM" ]; then
	if [ "$IN_CHECKOUT" = 1 ] && command -v go >/dev/null 2>&1; then FROM=source; else FROM=release; fi
fi
[ "$FROM" = source ] && [ "$IN_CHECKOUT" = 0 ] && die "--from-source needs this script in a checkout of the repo"

step "1. The condoo program"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM
mkdir -p "$DATA_DIR/bin"
if [ "$FROM" = source ]; then
	command -v go >/dev/null 2>&1 || die "--from-source needs Go (https://go.dev/dl)"
	say "Building from $SRC_DIR with $(go version | cut -d' ' -f3)"
	ver=$(git -C "$SRC_DIR" describe --tags --always --dirty 2>/dev/null || echo dev)
	(cd "$SRC_DIR" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$ver" -o "$TMP/condoo" ./cmd/condoo)
else
	asset="condoo-$OS-$ARCH"
	say "Downloading $asset ($VERSION) from github.com/$REPO"
	fetch "$(release_url "$asset")" "$TMP/condoo" || die "download failed: $(release_url "$asset")"
	fetch "$(release_url checksums.txt)" "$TMP/checksums.txt" || die "couldn't download checksums.txt"
	want=$(awk -v f="$asset" '$2 == f || $2 == "*"f { print $1 }' "$TMP/checksums.txt")
	[ -n "$want" ] || die "checksums.txt doesn't list $asset"
	[ "$(sha256 "$TMP/condoo")" = "$want" ] || die "checksum mismatch for $asset: not installing it"
	say "Checksum verified."
fi
chmod 755 "$TMP/condoo"
mv -f "$TMP/condoo" "$REAL_BIN"
say "Installed $REAL_BIN"

# ---------- 2. the link ----------

step "2. Where the condoo command goes"
say "A link to the program goes in a folder on your PATH, so agents can run \`condoo\`."
BIN_DIR=$(expand_home "${BIN_DIR:-$(ask "Folder for the link" "$HOME/.local/bin")}")
mkdir -p "$BIN_DIR"
LINK="$BIN_DIR/condoo"
if [ -e "$LINK" ] || [ -L "$LINK" ]; then
	if [ -L "$LINK" ] && [ "$(readlink "$LINK")" = "$REAL_BIN" ]; then
		: # ours already
	elif confirm "$LINK already exists and isn't Condoo's link. Replace it?" n; then
		rm -f "$LINK"
	else
		die "left $LINK as it was; rerun with --bin-dir to pick another folder"
	fi
fi
ln -sf "$REAL_BIN" "$LINK"
say "Linked $LINK -> $REAL_BIN"

case ":$PATH:" in
*":$BIN_DIR:"*) say "$BIN_DIR is on your PATH." ;;
*)
	say ""
	say "$BIN_DIR isn't on your PATH. Add it in your shell's startup file (wherever you keep it;"
	say "zsh users with ZDOTDIR: \$ZDOTDIR/.zshenv or .zprofile):"
	say ""
	say "  bash/zsh:  export PATH=\"$BIN_DIR:\$PATH\""
	say "  fish:      fish_add_path \"$BIN_DIR\""
	say ""
	say "Then open a new terminal (and restart Herdr, so its agents see it)."
	;;
esac

# ---------- 3. your name ----------

step "3. Your name"
say "Items agents raise are \"waiting on\" you. Leave it as your login name, or pick another."
current=$("$REAL_BIN" config | sed -n 's/.*"name":"\([^"]*\)".*/\1/p')
source=$("$REAL_BIN" config | sed -n 's/.*"name_source":"\([^"]*\)".*/\1/p')
NAME=${NAME:-$(ask "Name" "$current")}
if [ "$NAME" != "$current" ] || [ "$source" = config ]; then
	"$REAL_BIN" config set name "$NAME" >/dev/null
	say "Saved: $NAME"
else
	say "Using your login name ($current); change it any time: condoo config set name \"...\""
fi

# ---------- 4. the Herdr panel ----------

step "4. The Herdr panel"
MOD_DIR="$HERDR_HOME/mods/condoo"
if [ "$WANT_MOD" = no ]; then
	say "Skipped (--no-mod)."
elif [ ! -d "$HERDR_HOME" ]; then
	say "No Herdr folder at $HERDR_HOME: skipped. (--herdr-home to point at it.)"
	WANT_MOD=no
elif confirm "Install the panel into $MOD_DIR?" y; then
	if [ "$IN_CHECKOUT" = 1 ]; then
		src="$SRC_DIR/mods/condoo"
	else
		fetch "$(release_url condoo-mod.tar.gz)" "$TMP/mod.tar.gz" || die "couldn't download the panel"
		want=$(awk '$2 == "condoo-mod.tar.gz" || $2 == "*condoo-mod.tar.gz" { print $1 }' "$TMP/checksums.txt" 2>/dev/null || true)
		[ -z "$want" ] || [ "$(sha256 "$TMP/mod.tar.gz")" = "$want" ] || die "checksum mismatch for the panel"
		mkdir -p "$TMP/mod" && tar -xzf "$TMP/mod.tar.gz" -C "$TMP/mod"
		src="$TMP/mod/mods/condoo"
	fi
	mkdir -p "$MOD_DIR"
	# Only the mod's own files: Herdr's jsconfig.json / .d.ts and bin.json are left as they are.
	for f in "$src"/*.js "$src/mod.json"; do
		case "$f" in *.d.ts) continue ;; esac
		cp "$f" "$MOD_DIR/"
	done
	printf '{ "path": %s }\n' "$(json_string "$REAL_BIN")" >"$MOD_DIR/bin.json"
	if command -v herdr-app >/dev/null 2>&1; then
		herdr-app check "$MOD_DIR" || say "(herdr-app check failed: see above)"
	fi
	say "Installed the panel."
else
	WANT_MOD=no
fi

# ---------- done ----------

step "Done"
"$REAL_BIN" config
if [ "$WANT_MOD" != no ]; then
	cat <<'EOF'

Two things to add yourself (this script doesn't edit them):

1. A key for the panel, in the "global" section of your Herdr keys.json (pick any free key;
   note that editing keys.json stops Herdr updating its default keys there):

     "ctrl-shift-y": "mod.open:condoo"

   The key opens the panel as a dialog. To dock it as a sidebar instead, ask your agent in
   Herdr: "add a sidebar on the right for Condoo". Any layout block showing the condoo mod
   becomes the panel (shipped layouts are read-only, so it clones one first), e.g.:

     { "id": "condoo", "type": "block", "content": { "mod": "condoo" },
       "size": { "px": 340 }, "min": 260, "max": 640, "hidden": true, "background": "chrome" }

2. Tell your agents about it: copy AGENTS-snippet.md into your CLAUDE.md / AGENTS.md.
EOF
fi
if [ "$IN_CHECKOUT" = 1 ]; then
	say "   It's at $SRC_DIR/AGENTS-snippet.md"
else
	say "   It's at $(release_url AGENTS-snippet.md)"
fi
