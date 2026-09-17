# workforce

`wf` starts the Claude Code sessions that do work on this machine, and it is the
only thing that should. Not because it is nicer than typing `claude --bg`, but
because it holds three things a session cannot hold for itself:

- **Which GitHub token a role gets.** A reviewer holds the reviewer account; a
  worker holds the implementer account; a supervisor holds neither. A session
  that starts its own child hands over whatever token it happens to have.
- **How many of a role may run.** The cap lives in `roles.toml`, not in a
  persona file a model can talk itself out of.
- **What a reviewer is asked to look at.** `wf` composes the brief from a fixed
  template and refuses a caller-supplied one, so an author who summons its own
  reviewer cannot also frame what that reviewer looks for.

## Install

    brew install jschell12/tap/workforce

`wf` is the command; `workforce` is the same binary under the name scripts and
launchd plists use, and both are installed.

From source, which is what a machine that also deploys its own config does:

    make install        # builds and installs to ~/.local/bin

Then give it a config. `wf` reads `~/.claude/workforce/roles.toml` and the
permission profiles beside it in `~/.claude/workforce/perm/`:

    mkdir -p ~/.claude/workforce
    cp config/roles.example.toml ~/.claude/workforce/roles.toml
    cp -r config/perm ~/.claude/workforce/perm
    $EDITOR ~/.claude/workforce/roles.toml

`wf roles` prints what it resolved and `wf doctor` checks every persona,
permission profile and credential key the config names. Run doctor first: a
role whose token key does not resolve fails at spawn time, and a reviewer that
cannot authenticate looks exactly like one that found nothing to say.

## Layout

    cmd/wf/              the CLI entry point
    internal/            config, session, registry, spawn, reconcile, ...
    test-workforce.sh    84 cases, no network, `claude` and `scredmgr` stubbed
    config/roles.example.toml   annotated starting config
    config/perm/*.json          per-role permission profiles

Go 1.26 or newer. `make install` builds and installs; `make check` runs the
unit tests, vet, gofmt and the acceptance suite. The binary is deliberately not
committed: it is platform-specific and multi-megabyte.

Deployed to `~/.local/bin/workforce` with config under `~/.claude/workforce/`.
Those runtime paths are overridable: `WORKFORCE_CONFIG`, `WORKFORCE_PERM_DIR`,
`WORKFORCE_SESSION_DIR`, `WORKFORCE_CLAUDE`, `WORKFORCE_SCREDMGR`.

## How the token actually binds, and why that sentence is here

`claude --bg` is a client. The daemon spawns the session, so an environment
exported before the call never arrives. `wf` bound tokens that way until
2026-09-11, which meant every spawned session started with `GH_TOKEN` unset,
`gh-wrapper.sh` fell through to its default account, and **every reviewer the
fleet summoned signed as the human** — including, for a self-summoned reviewer,
as the author of the code under review.

The binding now travels in the `--settings` file, which is a CLI argument and
survives the hop. It carries the scredmgr **key**, never the value, so the
secret itself never reaches disk, an environment variable, or a process
listing. `token =
"none"` is written explicitly rather than omitted, because absent and `none`
want opposite answers: absent takes the default account, `none` stays
unauthenticated, and the one role defined by holding neither account is the one
that must not get that wrong.

## The reviewer, end to end

    wf spawn reviewer <repo> --pr <N>

A worker runs that after it opens a pull request, and then stops caring. The
verdict lands on the pull request, which is where it belongs; nobody waits on a
reply.

What happens between the command and a running session:

**It works out who is asking.** `resolveOrigin` tries five routes, most specific
first: an explicit `--for` tag, the calling session matched against the registry
by `CLAUDE_CODE_SESSION_ID`, the worktree the caller is standing in, the pull
request's own head branch matched to the worker that cut it, then the caller's
bare session ref. With none of those it refuses. An anonymous
reviewer is the thing the tag exists to prevent, so refusing beats shipping one.
The name pairs the two ends: worker `myrepo-k3n8` summons `rev-k3n8-322`.

**It writes the brief.** `REVIEWER_BRIEF` is a fixed template and a
caller-supplied brief is refused outright. This is the whole reason an author
may summon its own reviewer: it structurally cannot also say what that reviewer
looks for, and without the refusal "review this" quietly becomes "confirm this".

**It binds the reviewer account** through the `--settings` path described above,
so the session signs as the reviewer rather than as whoever called it.

**It applies `config/perm/reviewer.json`.** Read-only git, plus `gh pr
view/diff/checks/comment/review`. Denied: `git push`, `gh pr create`, `gh pr
merge`, `claude --bg`, `scredmgr`. A reviewer must not author, because anything
it authors forfeits the approval it would otherwise give. It also sets
`disableRemoteControl`, since a session that connects Remote Control leaves an
entry in the Claude app list that outlives it.

**It loads the `reviewer` persona** from `~/.claude/agents/`. One pull request,
one `AGENT-REVIEW:` verdict naming the exact head commit, then done. It does not
fix what it finds and does not merge.

**`wf reconcile` retires it**, on a 120-second timer from the same LaunchAgent
that keeps the `workforce` session alive: once the pull request leaves `OPEN`, or
once a verdict names the head it was spawned for and it is not mid-turn.

### A spawned reviewer and a dispatched one are not the same thing

The same persona file also backs a `reviewer` **subagent**, and the difference
decides what it is allowed to do. A subagent shares its parent's process, so it
holds the author's `GH_TOKEN`; it declares `githubRole: reviewer` against that
credential, the mismatch is refused, and it can only report back. `wf spawn`
starts a separate process and binds the role's token through its own settings,
so declaration and token agree and it signs.

The distinction is the process boundary, not who asked. That is why an author
summoning its own reviewer is fine, and why dispatching one as a subagent to
save a process is not.

## Tests

    ./test-workforce.sh

They stub `claude` and `scredmgr` rather than mocking the logic, and they end in
a canary that fails if the harness stops being able to fail. Run them after
touching `workforce`; several behaviours here fail silently in production — a
cap that stops counting, a session read as dead while alive, a token that does
not bind.

## Deployment

**This repo is the source.** A sync script BUILDS from a local clone
(`WORKFORCE_REPO`) into `~/.local/bin`, and copies `config/` into
`~/.claude/workforce/`. It refuses rather than skipping when that clone is
missing, because leaving whatever `wf` is already installed while printing a
success line makes a never-provisioned machine look like one a release behind.
So: commit here, then run the sync.

Building rather than copying is why the sync needs a Go toolchain. A machine
without one is told so; it is not quietly left on an old binary.

`gh-wrapper.sh` is not in this repo. It is the other half of the token binding,
resolving `WF_GH_TOKEN_KEY` through `scredmgr run`, but it is a general `gh`
wrapper rather than a `wf` file.
