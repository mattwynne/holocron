---
name: holocron
description: Find prior AI coding-agent/Pi sessions, recover context, locate artifacts created in earlier sessions, or reopen old sessions using the holocron CLI. Use when Matt mentions holocron, prior sessions, previous agent work, missing Google Docs/files/URLs, or asks to “find the session”.
metadata:
  short-description: Search and reopen prior agent sessions
---

# Holocron

Use this skill when Matt asks to find prior agent work, recover context from an earlier Pi session, locate a missing artifact, or reopen a session.

Holocron is Matt's local CLI for searching and reopening AI coding-agent sessions.

## Basic commands

```sh
holocron search "query" --json
holocron open <ID>
```

Search results include session IDs, cwd, titles, update times, and `resume_command` / `resume_argv`.

## Practical workflow

1. Search with distinctive terms from the artifact or task:
   ```sh
   holocron search "KMC proposal Google Doc" --json
   ```
2. Inspect likely results for `cwd`, title, update time, and `resume_command`.
3. If you only need a specific artifact, use `rg` on the session `.jsonl` for URLs, filenames, tool calls, or document IDs:
   ```sh
   rg -n "docs.google|gdocs|document/d|KMC" /path/to/session.jsonl
   ```
4. Reopen the session only when interactive context is needed:
   ```sh
   holocron open <ID>
   ```
   Or run the returned `resume_command`.

## Pi sessions outside the normal tree

Some useful Pi sessions may be app-managed and stored outside `~/.pi/agent/sessions`, for example:

```sh
~/git/mattwynne/pa/data/email-triage/sessions/*.jsonl
```

Holocron can find these when indexed. This was useful for finding the KMC proposal session and the Google Doc URL.

## Notes

- Prefer `holocron search ... --json` for precise machine-readable results.
- Use `rg` against session JSONL when you want a URL or tool result quickly, rather than reopening the whole session.
- Report any recovered artifact as a clickable link where possible.
