# Porting wf to Go

Status: complete. All nine commands are ported, the Python is removed, and
`make check` runs the unit tests, vet, gofmt and the acceptance suite (84 cases)
against a freshly built binary.

Deployment is NOT part of this repo and does not happen by merging it. Until
schellout's sync scripts build rather than copy, a sync run after this merge
trips a preflight that aborts the WHOLE ai-config deploy, not just wf. Land that
first.

## Why

The Python is 1394 lines in a single file with no type checking, and the parts
that matter most are the parts a reader has to hold in their head: which of
three states a `token` field is in, whether a cap counts per machine or per pull
request, whether a session row means alive. Each of those has already caused a
production failure, and each is a type in Go.

Distribution is the other half. A public tool that installs by copying a script
and hoping the host's Python is new enough is a worse tool than one that ships a
binary. The current file re-execs itself under a different interpreter when it
finds Python older than 3.11, which is a workaround for a problem a compiled
binary does not have.

## What does not change

These are behaviours that exist because something went wrong once. Porting them
faithfully is the point, and each keeps its comment explaining the incident:

- **The credential travels as a KEY, in the `--settings` file.** Not a value,
  not an environment variable. The environment does not survive the hop to the
  daemon; a CLI argument does.
- **Absent, `"none"`, and a key are three instructions, not two.** Now
  `config.Token` with a `TokenKind`, so a caller cannot collapse them by
  accident.
- **A reviewer's brief is composed by wf and a caller-supplied one is refused.**
  An author who summons its own reviewer must not also frame what it looks at.
- **Every reviewer is attributable, or it does not start.** Four resolution
  routes, then refusal. An anonymous reviewer is the thing the naming exists to
  prevent.
- **Liveness is membership in the plain listing, never a state word.** To be a
  type, not a string comparison at each call site.
- **A cap is a number AND a scope.** `config.CapScope` makes the pair
  inseparable.

## What the redesign changes

- **`wf roles`** is new. "What can I spawn, and as whom" required reading
  roles.toml. Its absence is how a repo went months without a reviewer role and
  nobody noticed until a spawn failed.
- **`--json` is global**, not a flag on one subcommand.
- **Commands take an explicit `Env`** (paths, streams, config) instead of module
  globals, so a test drives the real command against a scratch tree.
- **Config errors name the file they came from** and how to point elsewhere.
- **`ls` becomes `sessions`**, with `ls` kept as an alias. The old name is worth
  keeping under the fingers and worth not being the documented one.
- Structured errors instead of `die()` and a bare exit.

## Cutover

Nothing about `~/.local/bin/workforce` changes by merging this branch: the
installed binary is replaced by the sync, which has to learn to build first.
Two consumers make a silent regression expensive rather than obvious: a launchd
job shells out every 120 seconds, and every reviewer spawn goes through it.

Order: schellout's sync change, then this, then a sync run.

## Testing

`test-workforce.sh` drives the binary from outside with `claude` and `scredmgr`
stubbed, so it is language-agnostic and stays the acceptance gate. Where the
redesign changes a surface it pins, the case gets ported rather than deleted,
and the port is called out in review.

Go unit tests cover what the shell suite cannot reach cheaply: config edge
cases, origin resolution, cap arithmetic. `TestParsesShippedConfig` asserts
against the repo's shipped `config/roles.example.toml` rather than a fixture,
because a parser that only ever sees its own fixtures is not evidence.
