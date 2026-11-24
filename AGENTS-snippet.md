# Questions for the user: Condoo

The user keeps a list of questions and requests from agents in Condoo (a panel in Herdr,
backed by the `condoo` command). Use it for anything you need from them while doing
research or other long tasks.

## When you have questions

- **One question:** ask it in the conversation as normal.
- **More than one question:** don't ask them in the conversation. Put **every one** in
  Condoo, one item per question, then tell the user in one line how many you added and stop
  to wait for answers:

  ```sh
  condoo ask "What is required to do for this?" \
    -s "Ticket gives two paths to take. Which path is correct?" \
    -d "The ticket says ... Options:
  1. ... (because ...)
  2. ..." \
    --agent "<your name> (<what you're working on>)" --priority normal
  ```

  - The title is the question itself.
  - `-s` summary: one short line (under ~100 characters) shown on the card in the list.
  - `-d` description: the full context, options and your recommendation (markdown), so it
    can be answered without reading your scrollback. It's shown when they open the item.
  - `--agent`: your own name (Claude, Codex, Gemini, ...) and the task, so they know who
    is asking and about what.
  - `--priority`: `urgent` if you're blocked on it, `high` if it shapes the rest of the
    work, otherwise `normal` (or `low`).
  - Your terminal is recorded automatically (`$HC_TERMINAL_ID`), so their answers are typed
    into this terminal as `Answer to your question (condoo #N) "...": ...` when you're
    idle. If they say they've answered and you didn't receive it, read it with
    `condoo get N` (the `answer` field).
  - Carry on with any work that doesn't depend on the answers.
  - When an answer arrives, act on it; the item is already marked completed.

## Other things you need from them

Something they must do rather than answer (approve a prompt, grant access, review a file):

```sh
condoo request "Approve the Linear reads" -s "The agent is paused on permission prompts" \
  -d "Run hc attach t2r and choose option 2 ..." --agent "<your name> (<task>)"
```

Mark it done once it's resolved: `condoo set N --status completed`.

## Comments

Add progress notes to an item rather than rewriting its description:
`condoo comment N "Done for menu-service; venue-service waits on #19" --agent "<your name> (<task>)"`.
Their comments reach you as `Comment from <name> on condoo #N "...": ...` (typed in when
you're idle; several at once arrive as one `N updates from Condoo:` message); `condoo get N`
lists every comment (`comments`, oldest first).

## Assigned items

They may assign you an item: `Condoo #N is assigned to you ...`. Work on it, and reply
only with comments: progress, questions and the result, with
`condoo comment N "..." --agent "<your name> (<task>)"`. Don't change its status or close
it; they do that. Their comments on it reach you. If you raised the item yourself, you have
its context: do it yourself, or give a sub-agent a brief with what it needs (sub-agents
don't see your conversation). Don't add, change or
delete their own tasks (`kind: task`) unless they ask. `condoo list` prints everything as
JSON; `condoo --help` lists every command.
