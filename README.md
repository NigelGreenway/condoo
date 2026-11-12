# Condoo

**Context + Todo.** One list for everything your coding agents need from you: their
questions, their requests, and your own tasks. Agents add items with the `condoo` command;
you see and answer them in a panel in [Herdr](https://herdr.dev). An answer is typed straight
back into the agent that asked.

Runs on Linux, macOS and Windows. A single binary, no runtime to install.

## Install

**Linux and macOS**

```sh
curl -fsSL https://github.com/NigelGreenway/condoo/releases/latest/download/install.sh | sh
```

**Windows** (PowerShell)

```powershell
& ([scriptblock]::Create((irm https://github.com/NigelGreenway/condoo/releases/latest/download/install.ps1)))
```

The installer asks before each step:

1. Where the `condoo` command goes. Default: `~/.local/bin` (Linux, macOS);
   `%LOCALAPPDATA%\Programs\Condoo` (Windows).
2. Whether to add that folder to your PATH. Windows: it can add it to your user PATH.
   Linux and macOS: it prints the line to add and never edits your shell files.
3. Your name on the list. Default: your login name.
4. Whether to install the Herdr panel.

From a checkout with Go installed, `sh install.sh` (or `.\install.ps1`) builds from source
instead of downloading. `--help` lists the options, including `--yes` for no questions.

## Set up in Herdr

1. **A key for the panel.** In the `"global"` section of your Herdr `keys.json` (any free key):

   ```json
   "ctrl-a /": "mod.open:condoo"
   ```

   The key opens the panel as a dialog.

2. **Optional: a sidebar.** Ask your agent in Herdr: *"add a sidebar on the right for
   Condoo"*. Any layout block showing the `condoo` mod becomes the panel, and the key then
   slides it in and out.

3. **Tell your agents.** Copy [`AGENTS-snippet.md`](AGENTS-snippet.md) into your
   `CLAUDE.md`, `AGENTS.md` or equivalent. It tells agents to put their questions in Condoo
   when they have more than one.

## Using the panel

Three sections: **Needs your reply** (agents' requests), **My tasks** and **Questions**. Items
belong to the Herdr space they were raised in; `w` switches between this space and all.

Closing a space never hides its items. They move under **Detached Spaces**, shown in every
space and tagged with the old name. Open a space with the same name again (most spaces are
named after their repo or folder) and they go back into it by themselves. `m` moves an item
to any space, or to Personal.

| Key | Does |
|---|---|
| `j` `k` | move |
| `enter` | answer a question in your editor, go to a request's agent, or edit a task |
| `a` | add an item; `>` adds a sub-task |
| `s` `p` | change status / priority |
| `m` | move to another space |
| `x` | delete |
| `?` | every key |

Answers open in `$VISUAL` or `$EDITOR`. Without one set: `vim`, `vi` or `nano`, or on Windows
`edit`, `nvim`, `vim`, `nano` or `notepad`, whichever is found first.

## The command

```sh
condoo ask "What is required to do for this?" -s "one-line summary" \
  -d "full context and options" --agent "Claude (TICKET-123)" --priority high
condoo request "Approve the Linear reads" --agent "Codex (t2r)"
condoo add "Review the migration"          # one of your own tasks
condoo list                                # everything, as JSON
condoo get 12                              # one item, with its answer
condoo set 12 --status completed
condoo move 12 --space <space id>          # with its sub-items; --space "" for none
condoo --help
```

Every command prints JSON. Inside Herdr, the agent's terminal is recorded from
`$HC_TERMINAL_ID`, so answers can be sent back to it.

## Settings

```sh
condoo config                      # your name, the database, and where each came from
condoo config set name "Ada"
condoo config set db "~/Dropbox/condoo.sqlite"
condoo config set name ""          # back to the default
```

Environment variables override the config file: `CONDOO_NAME`, `CONDOO_DB`, `CONDOO_CONFIG`.
`CONDOO_AGENT` sets the default `--agent`.

## Where things live

| | Linux | macOS | Windows |
|---|---|---|---|
| Database | `~/.local/share/condoo/condoo.sqlite` | `~/Library/Application Support/Condoo/condoo.sqlite` | `%LOCALAPPDATA%\Condoo\condoo.sqlite` |
| Settings | `~/.config/condoo/config.json` | `~/Library/Application Support/Condoo/config.json` | `%APPDATA%\Condoo\config.json` |
| Program | `~/.local/share/condoo/bin/condoo`, linked from your chosen folder | `~/Library/Application Support/Condoo/bin/condoo`, linked | your chosen folder |
| Herdr panel | `~/.config/herdr-app/mods/condoo` | same | same |

On Linux, `XDG_DATA_HOME`, `XDG_CONFIG_HOME` and `XDG_CACHE_HOME` are honoured.

## Uninstall

```sh
sh install.sh --uninstall          # Windows: .\install.ps1 -Uninstall
```

It removes the program, the link and the panel, and keeps your list and settings (it prints
where they are).

## Development

```sh
go test ./...                      # the command's tests
node --test test/*.test.mjs        # the panel's space matching
go build ./cmd/condoo
herdr-app check mods/condoo         # the panel
```

CI runs the tests and an install, upgrade and uninstall on Linux, macOS and Windows. Pushing a
`v*` tag builds every binary, runs each on its own OS, and attaches them to a GitHub release
with `checksums.txt` (see `.github/workflows/release.yml`).

## Licence

Condoo is free software: you can redistribute it and/or modify it under the terms of the
[GNU General Public License v3.0](LICENSE) as published by the Free Software Foundation.
