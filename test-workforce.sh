#!/usr/bin/env bash
# Tests for scripts/workforce. No network, no real `claude`, no real scredmgr:
# both are stubbed on PATH so the cap logic, the state/status union and the
# reviewer-brief guardrail are exercised for real rather than described.
#
# Run after touching scripts/workforce. It exists because three of the cases
# below are behaviours whose failure is silent: a cap that stops counting, a
# background session read as unknown, and an author writing its own review brief.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# WORKFORCE_BIN lets a regression run point at a patched copy while the suite
# itself stays in the real tree. Copying the whole directory to /tmp instead made
# the profile-reading case fail for a reason that had nothing to do with the
# change under test, which is a false signal in a harness whose whole job is
# telling true failures from false ones.
WF="${WORKFORCE_BIN:-${HERE}/workforce}"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

pass=0; fail=0; ran=0
ok(){ ran=$((ran+1)); pass=$((pass+1)); printf '  ok   %s\n' "$1"; }
no(){ ran=$((ran+1)); fail=$((fail+1)); printf '  FAIL %s\n     %s\n' "$1" "${2:-}"; }
check(){ # check <name> <expected-substring> <expected-rc> -- cmd...
  local name="$1" want="$2" rc_want="$3"; shift 4
  local out rc
  out="$("$@" 2>&1)"; rc=$?
  if [[ "$rc" != "$rc_want" ]]; then no "$name" "rc $rc, wanted $rc_want: ${out:0:200}"; return; fi
  if [[ -n "$want" && "$out" != *"$want"* ]]; then no "$name" "missing ${want@Q} in: ${out:0:300}"; return; fi
  ok "$name"
}

# ---------------------------------------------------------------- fixtures

REPO="${TMP}/permitguv"
mkdir -p "${REPO}"
git -C "${REPO}" init -q -b main
git -C "${REPO}" config user.email t@example.com
git -C "${REPO}" config user.name test
: > "${REPO}/README.md"
git -C "${REPO}" add -A && git -C "${REPO}" commit -qm init

mkdir -p "${TMP}/perm" "${TMP}/agents"
cat > "${TMP}/perm/worker.json" <<'J'
{"permissions":{"allow":[],"deny":[]}}
J
cp "${TMP}/perm/worker.json" "${TMP}/perm/reviewer.json"
: > "${TMP}/agents/implementer.md"
: > "${TMP}/agents/reviewer.md"

write_config(){ # write_config <worker-cap>
  cat > "${TMP}/roles.toml" <<T
[repo.permitguv]
path = "${REPO}"
slug = "jschell12/permitguv"

[repo.permitguv.role.worker]
persona = "implementer"
personas_allowed = ["implementer", "coordinator"]
token = "custom:TEST_IMPL"
cap = ${1}
cap_scope = "repo"
workspace = "worktree"
perm = "worker"

[repo.permitguv.role.reviewer]
persona = "reviewer"
personas_allowed = ["reviewer"]
token = "custom:TEST_REVIEW"
cap = 1
cap_scope = "pr"
workspace = "checkout"
perm = "reviewer"
T
}

# Stub `claude`: prints whatever AGENTS_JSON holds for `agents --json`.
cat > "${TMP}/claude-stub" <<'S'
#!/usr/bin/env bash
if [[ "${1:-}" == "agents" ]]; then
  [[ "${AGENTS_RC:-0}" != "0" ]] && exit "${AGENTS_RC}"
  printf '%s' "${AGENTS_JSON:-[]}"; exit 0
fi
[[ "${1:-}" == "--version" ]] && { echo "stub"; exit 0; }
exit 0
S
chmod +x "${TMP}/claude-stub"

cat > "${TMP}/scredmgr-stub" <<'S'
#!/usr/bin/env bash
[[ "${1:-}" == "get" ]] && { echo "ghp_STUBTOKEN_VALUE"; exit 0; }
exit 1
S
chmod +x "${TMP}/scredmgr-stub"

# A stub gh, so a PR's state and its verdicts are ours to set.
#
# Defined HERE, in setup, and not at the reconcile section where it used to
# live. Three cases above that point shell out to gh, so until 2026-09-17 they
# ran against the real one: authenticated on a laptop, which is why the suite
# read 84/84 for whoever wrote it, and `exit status 4` in CI and in any clean
# checkout. A suite that is hermetic only in the environment it was written in
# is not hermetic.
cat > "${TMP}/gh-stub" <<'S'
#!/usr/bin/env bash
case "$*" in
  *"--json state"*)      printf '{"state":"%s"}' "${GH_PR_STATE:-OPEN}" ;;
  *"--json headRefOid"*) printf '{"headRefOid":"%s"}' "${GH_HEAD:-aaaaaaaabbbbbbbbcccccccc}" ;;
  *"--json comments"*)   printf '%s' "${GH_COMMENTS:-{\"comments\":[]\}}" ;;
  *) exit 1 ;;
esac
S
chmod +x "${TMP}/gh-stub"
export WORKFORCE_GH="${TMP}/gh-stub"

# An identity for the calling session, for the same reason. wf resolves who is
# spawning a reviewer from CLAUDE_CODE_SESSION_ID, and a suite that inherits the
# ambient one passes inside a Claude session and fails everywhere else. Cases
# that are ABOUT identity set or unset it explicitly and still do.
export CLAUDE_CODE_SESSION_ID="${CLAUDE_CODE_SESSION_ID:-5e551011-0000-0000-0000-00000000cafe}"

export WORKFORCE_CONFIG="${TMP}/roles.toml"
export WORKFORCE_PERM_DIR="${TMP}/perm"
export WORKFORCE_LOG="${TMP}/wf.log"
export WORKFORCE_CLAUDE="${TMP}/claude-stub"
export WORKFORCE_SCREDMGR="${TMP}/scredmgr-stub"

write_config 3

# ---------------------------------------------------------------- cases

echo "workforce tests"

# wf must run from a login shell. `#!/usr/bin/env python3` used to resolve to the
# caller's PATH, and a login shell on macOS gets /usr/bin/python3 (3.9), so wf
# worked for the session that wrote it and raised ModuleNotFoundError in every
# plain one -- including the agent sessions it was built for.
#
# The INTERPRETER half of that is retired: a compiled binary has none for a
# caller to get wrong. The LOGIN SHELL half is not, and it runs unconditionally
# now. A binary can still fail there, from a PATH that does not reach it or a
# profile that shadows the name, and the previous shape reported two cheerful
# `ok`s that asserted nothing whenever the machine had no tomllib-less python.
if bash -lc "'$WF' --help" >/dev/null 2>&1; then
  ok "wf runs from a login shell"
else
  no "wf runs from a login shell" "$(bash -lc "'$WF' --help" 2>&1 | tail -2)"
fi

export AGENTS_JSON='[]'
check "unknown repo is refused"  "unknown repo" 1 -- "$WF" spawn worker nosuchrepo "x" --dry-run
check "unknown role is refused"  "unknown role" 1 -- "$WF" spawn nosuchrole permitguv "x" --dry-run
check "persona outside the allow list is refused" "not allowed" 1 -- \
  "$WF" spawn worker permitguv "x" --persona reviewer --dry-run
check "allowed alternate persona is accepted" "--agent coordinator" 0 -- \
  "$WF" spawn worker permitguv "x" --persona coordinator --dry-run

# The guardrail: an author may summon a reviewer but may not write its brief.
check "reviewer refuses a caller-supplied brief" "composed by wf" 1 -- \
  "$WF" spawn reviewer permitguv "please confirm this is fine" --pr 5 --dry-run
check "reviewer requires --pr" "needs --pr" 1 -- "$WF" spawn reviewer permitguv --dry-run
check "reviewer brief names the PR and forbids merging" "do not merge" 0 -- \
  "$WF" spawn reviewer permitguv --pr 431 --dry-run

# An unreadable listing must not read as zero sessions.
AGENTS_RC=1 check "unreadable agent list refuses to spawn" "unreadable list is not an empty one" 1 -- \
  "$WF" spawn worker permitguv "x" --dry-run
AGENTS_JSON='not json' check "malformed agent list refuses to spawn" "cannot read" 1 -- \
  "$WF" spawn worker permitguv "x" --dry-run

# A token value must never reach stdout.
out="$(AGENTS_JSON='[]' "$WF" spawn worker permitguv "x" --dry-run 2>&1)"
if [[ "$out" == *"ghp_STUBTOKEN_VALUE"* ]]; then
  no "token value never printed" "found the token in dry-run output"
else ok "token value never printed"; fi

# --- cap, counted against the registry joined to live sessions ---
REG="$(git -C "${REPO}" rev-parse --git-common-dir)"
REG="${REPO}/${REG#./}"; REG="${REG}/agent-coordination/active"
mkdir -p "${REG}"
seed(){ # seed <bg_id> <name> <role>
  cat > "${REG}/$1.json" <<J
{"agent_id":"$1","bg_id":"$1","name":"$2","role":"$3","repo":"permitguv","pr":null}
J
}
seed aaaaaaaa w1 worker
seed bbbbbbbb w2 worker

write_config 2
# Background rows carry `state`, not `status`, and "done" means alive and idle.
# A reader keyed on `status` alone sees these as unknown and lets the cap pass.
export AGENTS_JSON='[{"id":"aaaaaaaa","name":"w1","kind":"background","state":"working","cwd":"/"},
                     {"id":"bbbbbbbb","name":"w2","kind":"background","state":"done","cwd":"/"}]'
check "cap counts background rows using state, and done counts as live" "cap reached" 1 -- \
  "$WF" spawn worker permitguv "x" --dry-run

export AGENTS_JSON='[{"id":"aaaaaaaa","name":"w1","kind":"background","state":"working","cwd":"/"},
                     {"id":"bbbbbbbb","name":"w2","kind":"background","state":"completed","cwd":"/"}]'
check "a completed session does not count against the cap" "--agent implementer" 0 -- \
  "$WF" spawn worker permitguv "x" --dry-run

# Interactive rows use `status`; both vocabularies must count.
export AGENTS_JSON='[{"pid":1,"sessionId":"aaaaaaaa-1111-2222-3333-444444444444","name":"w1","kind":"interactive","status":"busy","cwd":"/"},
                     {"pid":2,"sessionId":"bbbbbbbb-1111-2222-3333-444444444444","name":"w2","kind":"interactive","status":"waiting","cwd":"/"}]'
check "cap counts interactive rows using status" "cap reached" 1 -- \
  "$WF" spawn worker permitguv "x" --dry-run

# reviewer cap is per PR, so a different PR is not blocked by a held one.
seed cccccccc r1 reviewer
python3 - "${REG}/cccccccc.json" <<'P'
import json,sys
p=sys.argv[1]; d=json.load(open(p)); d["pr"]="431"; json.dump(d,open(p,"w"))
P
export AGENTS_JSON='[{"id":"cccccccc","name":"r1","kind":"background","state":"working","cwd":"/"}]'
check "reviewer cap blocks the same PR" "cap reached" 1 -- \
  "$WF" spawn reviewer permitguv --pr 431 --dry-run
check "reviewer cap does not block a different PR" "--agent reviewer" 0 -- \
  "$WF" spawn reviewer permitguv --pr 999 --dry-run

# The brief must name a slug `gh` can resolve. "permitguv" is not a repo; the
# slug lives on the repo table, not the role table, and reaching it needs an
# explicit carry-through -- which is exactly the kind of thing that reverts.
check "reviewer brief names the owner/repo slug" "in jschell12/permitguv" 0 -- \
  "$WF" spawn reviewer permitguv --pr 12 --dry-run

# --- the machine-scoped `workforce` role ---
# It takes no repo, gets a fixed session name, runs with no persona and no token,
# and is counted by session name rather than by registry entry: launchd starts it,
# so a cleared registry or a reboot must not read as "none running".
cat >> "${TMP}/roles.toml" <<T

[defaults]
workspace_root = "${TMP}"

[role.workforce]
persona = ""
owner = "user"
token = "none"
cap = 1
cap_scope = "machine"
workspace = "workspace_root"
perm = "workforce"
T
cp "${TMP}/perm/worker.json" "${TMP}/perm/workforce.json"

export AGENTS_JSON='[]'
check "machine role needs no repo" "cwd: ${TMP}" 0 -- "$WF" spawn workforce --brief "standing by" --dry-run
check "machine role gets a fixed session name" "-n workforce" 0 -- \
  "$WF" spawn workforce --brief "standing by" --dry-run
check "machine role passes no --agent" "" 0 -- "$WF" spawn workforce --brief "b" --dry-run
out="$(AGENTS_JSON='[]' "$WF" spawn workforce --brief "b" --dry-run 2>&1)"
if [[ "$out" == *"--agent"* ]]; then no "machine role passes no --agent" "found --agent";   else ok "no --agent for an empty persona"; fi
# Asserts the binding itself. The old form grepped a diagnostic line naming
# GH_TOKEN; the environment no longer carries a token at all, so the settings
# key is both the real mechanism and the only honest thing to check.
if [[ "$out" == *"WF_GH_TOKEN_KEY=none"* ]]; then ok "machine role gets no token";   else no "machine role gets no token" "expected WF_GH_TOKEN_KEY=none, got: ${out:0:200}"; fi
check "machine role rejects a positional repo" "machine-scoped" 1 -- \
  "$WF" spawn workforce permitguv --brief "b" --dry-run

# A tokenless role must say so rather than say nothing: absent and "none" want
# opposite answers from the gh wrapper, and the role defined by holding neither
# account is the one a silent fallback would hand the broadest one.
grep -q '"WF_GH_TOKEN_KEY": "none"' <<<"$out" \
  && ok "a tokenless role says none rather than saying nothing" \
  || no "a tokenless role says none rather than saying nothing" "no explicit none in: ${out:0:200}"
# `wf spawn workforce "text"` binds "text" to the positional repo, so the
# machine-scoped error is what fires and it is the more useful one.
check "brief given twice is refused" "pass the brief once" 1 -- \
  "$WF" spawn worker permitguv "positional" --brief "flag"

# Counted by NAME: a live session called workforce blocks a second one even with
# an empty registry, which is the reboot case.
export AGENTS_JSON='[{"id":"dddddddd","name":"workforce","kind":"background","state":"done","cwd":"/"}]'
check "a live workforce session blocks a second, registry or not" "cap reached" 1 -- \
  "$WF" spawn workforce --brief "b" --dry-run
export AGENTS_JSON='[{"id":"dddddddd","name":"workforce","kind":"background","state":"completed","cwd":"/"}]'
check "a completed workforce session does not block" "cwd:" 0 -- \
  "$WF" spawn workforce --brief "b" --dry-run

# A role's standing instruction must reach --append-system-prompt, not the brief.
# The brief is a user message and decays; this is the mechanism it stands in for.
cat >> "${TMP}/roles.toml" <<'T'

[role.steered]
persona = ""
token = "none"
cap = 1
cap_scope = "machine"
workspace = "workspace_root"
perm = "workforce"
system_prompt = "Dispatch rather than do."
T
export AGENTS_JSON='[]'
check "system_prompt reaches --append-system-prompt" "--append-system-prompt 'Dispatch rather than do.'" 0 -- \
  "$WF" spawn steered --brief "b" --dry-run
# and a role without one passes no such flag
out="$(AGENTS_JSON='[]' "$WF" spawn worker permitguv "x" --dry-run 2>&1)"
if [[ "$out" == *"--append-system-prompt"* ]]; then
  no "no system prompt flag when the role declares none" "found the flag"
else ok "no system prompt flag when the role declares none"; fi

# --- liveness polarity ---
# `blocked` shipped a duplicate workforce session on 2026-09-10 because the
# liveness test was an allowlist and `blocked` was not in it. An UNKNOWN state
# must count as alive, so the worst case is refusing to start rather than
# starting a second one.
write_config 1
seed eeeeeeee w9 worker
export AGENTS_JSON='[{"id":"eeeeeeee","name":"w9","kind":"background","state":"blocked","cwd":"/"}]'
check "a blocked session counts as live" "cap reached" 1 -- \
  "$WF" spawn worker permitguv "x" --dry-run
export AGENTS_JSON='[{"id":"eeeeeeee","name":"w9","kind":"background","state":"a-state-nobody-has-seen","cwd":"/"}]'
check "an unknown state counts as live" "cap reached" 1 -- \
  "$WF" spawn worker permitguv "x" --dry-run
export AGENTS_JSON='[{"id":"eeeeeeee","name":"w9","kind":"background","state":"COMPLETED","cwd":"/"}]'
check "a dead state is matched case-insensitively" "--agent implementer" 0 -- \
  "$WF" spawn worker permitguv "x" --dry-run
rm -f "${REG}/eeeeeeee.json"
write_config 2

# --- paired names ---
# The session list shows a name and nothing else, so a reviewer must carry the
# tag of the worker whose PR it is reading. wf identifies the caller by
# CLAUDE_CODE_SESSION_ID[:8], which is a background session's short id: nothing
# is asked of the model, because an identity a model has to remember goes
# missing.
rm -f "${REG}"/*.json
export AGENTS_JSON='[]'
out="$("$WF" spawn worker permitguv "x" --dry-run 2>&1)"
if [[ "$out" =~ -n\ permitguv-[a-z0-9]{4}\  ]]; then ok "a worker name carries a minted tag"
else no "a worker name carries a minted tag" "got: ${out:0:200}"; fi

# Two workers must not collide on a tag.
t1="$("$WF" spawn worker permitguv "x" --dry-run 2>&1 | grep -oE '\-n permitguv-[a-z0-9]{4}')"
t2="$("$WF" spawn worker permitguv "x" --dry-run 2>&1 | grep -oE '\-n permitguv-[a-z0-9]{4}')"
if [[ -n "$t1" && -n "$t2" ]]; then ok "worker tags are minted per spawn"
else no "worker tags are minted per spawn" "t1=$t1 t2=$t2"; fi

# A reviewer spawned BY a registered worker carries that worker's tag.
mkreg_worker(){ printf '%s' "{\"agent_id\":\"abcd1234\",\"bg_id\":\"abcd1234\",\"name\":\"permitguv-k3n8\",\"role\":\"worker\",\"repo\":\"permitguv\",\"tag\":\"k3n8\",\"token_key\":\"custom:X\",\"managed_by\":\"workforce\"}" > "${REG}/abcd1234.json"; }
mkreg_worker
export AGENTS_JSON='[{"id":"abcd1234","name":"permitguv-k3n8","kind":"background","state":"working","cwd":"/"}]'
CLAUDE_CODE_SESSION_ID="abcd1234-1111-2222-3333-444444444444" \
  check "a reviewer carries its caller's tag" "-n rev-k3n8-501" 0 -- \
  "$WF" spawn reviewer permitguv --pr 501 --dry-run

# EVERY reviewer must be identifiable. Four more routes, then a refusal --
# there is no unpaired name.

# Route: the worktree we are standing in, when the session id matches nothing.
printf '%s' '{"agent_id":"bbbb2222","bg_id":"bbbb2222","name":"permitguv-w0rk","role":"worker","repo":"permitguv","tag":"w0rk","worktree":"'"${TMP}"'/wt","branch":"agent/x-worker","token_key":"custom:X","managed_by":"workforce"}' > "${REG}/bbbb2222.json"
mkdir -p "${TMP}/wt/deep"
( cd "${TMP}/wt/deep" && CLAUDE_CODE_SESSION_ID="99999999-0000-0000-0000-000000000000"     "$WF" spawn reviewer permitguv --pr 504 --dry-run 2>&1 ) > "${TMP}/wtout" 2>&1
if grep -q -- "-n rev-w0rk-504" "${TMP}/wtout"; then ok "the worktree identifies the worker"
else no "the worktree identifies the worker" "got: $(head -c 200 "${TMP}/wtout")"; fi

# Route: the PR's head branch, matched to the worker that cut it. Works from
# anywhere, including the main checkout.
export GH_HEADREF="agent/x-worker"
cat > "${TMP}/gh-branch" <<'S'
#!/usr/bin/env bash
case "$*" in
  *"--json headRefName"*) printf '{"headRefName":"%s"}' "${GH_HEADREF}" ;;
  *"--json headRefOid"*)  printf '{"headRefOid":"deadbeefdeadbeef"}' ;;
  *) exit 1 ;;
esac
S
chmod +x "${TMP}/gh-branch"
CLAUDE_CODE_SESSION_ID="99999999-0000-0000-0000-000000000000" WORKFORCE_GH="${TMP}/gh-branch" \
  check "the PR head branch identifies the worker" "-n rev-w0rk-505" 0 -- \
  "$WF" spawn reviewer permitguv --pr 505 --dry-run

# Route: a session wf can see but does not manage. Its own ref IS an identity --
# it is in `claude agents` and in ListAgents -- so the name carries it.
CLAUDE_CODE_SESSION_ID="beef9999-0000-0000-0000-000000000000" WORKFORCE_GH="${TMP}/gh-fail" \
  check "an unmanaged session names itself" "-n rev-beef-506" 0 -- \
  "$WF" spawn reviewer permitguv --pr 506 --dry-run

# A missing `gh` must not traceback out of a launcher: every caller already
# treats a non-zero exit as "skip or refuse". Found by a test that named a stub
# it never created.
CLAUDE_CODE_SESSION_ID="beef9999-0000-0000-0000-000000000000" WORKFORCE_GH="/nonexistent/gh" \
  check "a missing gh degrades instead of crashing" "-n rev-beef-509" 0 -- \
  "$WF" spawn reviewer permitguv --pr 509 --dry-run

# Said outright, which always wins.
check "--for wins over everything" "-n rev-mine-507" 0 -- \
  "$WF" spawn reviewer permitguv --pr 507 --for mine --dry-run

# Nothing at all: refuse rather than ship an anonymous reviewer.
out="$(env -u CLAUDE_CODE_SESSION_ID "$WF" spawn reviewer permitguv --pr 508 --dry-run 2>&1)"; rc=$?
if [[ $rc -ne 0 && "$out" == *"must be identifiable"* ]]; then ok "no identity at all is refused"
else no "no identity at all is refused" "rc=$rc out=${out:0:200}"; fi

rm -rf "${TMP}/wt"; rm -f "${REG}"/*.json; unset GH_HEADREF

# --- reconcile ---
# gh is stubbed in setup, above. GH_PR_STATE, GH_HEAD and GH_COMMENTS steer it.

mkreg(){ # mkreg <file> <json>
  printf '%s' "$2" > "${REG}/$1.json"
}
rm -f "${REG}"/*.json

# Somebody else's entry, in the _multi-agent-protocol.md shape. Reaping it would
# deregister a live agent mid-task. This is the case the first real dry run
# caught: ten of them alongside wf's own three.
mkreg foreign '{"agent_id":"foreign","pid":999,"worktree":"/tmp/x","branch":"b","task":"t","status":"working","heartbeat_at":"now"}'
export AGENTS_JSON='[]'
# Assert the COUNTS, not the absence of a string. The first version of this case
# grepped for "foreign", which is the agent_id and is never printed -- a foreign
# entry renders as `reap ? []` because it has no name. So the check passed both
# with the guard and without it, which is a check that cannot fail.
check "a foreign registry entry is never touched" "retire 0, reap 0, keep 0" 0 -- \
  "$WF" reconcile --dry-run
# And the file itself survives, which is what actually matters: a count can be
# right while the entry is gone.
[[ -f "${REG}/foreign.json" ]] \
  && ok "the foreign entry is still on disk" \
  || no "the foreign entry is still on disk" "reconcile deleted another agent's registration"

# wf's own entry, session gone -> reaped
mkreg ffffffff '{"agent_id":"ffffffff","bg_id":"ffffffff","name":"r9","role":"reviewer","repo":"permitguv","pr":"9","head":"aaaaaaaabbbb","token_key":"custom:X","managed_by":"workforce"}'
check "an entry with no live session is reaped" "reap" 0 -- "$WF" reconcile --dry-run

# live session, PR open, no verdict -> kept
export AGENTS_JSON='[{"id":"ffffffff","name":"r9","kind":"background","state":"idle","cwd":"/"}]'
GH_PR_STATE=OPEN check "a live reviewer with no verdict is kept" "keep 1" 0 -- "$WF" reconcile --dry-run

# PR merged -> retired whatever it is doing
GH_PR_STATE=MERGED check "a reviewer whose PR merged is retired" "is MERGED" 0 -- "$WF" reconcile --dry-run

# verdict at its recorded head, session idle -> retired
export GH_COMMENTS='{"comments":[{"body":"AGENT-REVIEW: APPROVED\nReviewed-Commit: aaaaaaaabbbb"}]}'
GH_PR_STATE=OPEN check "a reviewer that signed at its head is retired" "signed at" 0 -- "$WF" reconcile --dry-run

# same, but mid-turn -> left alone, or the write-up is truncated
export AGENTS_JSON='[{"id":"ffffffff","name":"r9","kind":"background","state":"working","cwd":"/"}]'
GH_PR_STATE=OPEN check "a reviewer still working is not retired mid-turn" "keep 1" 0 -- "$WF" reconcile --dry-run

# a verdict naming a DIFFERENT commit is not this round's -> kept
export AGENTS_JSON='[{"id":"ffffffff","name":"r9","kind":"background","state":"idle","cwd":"/"}]'
export GH_COMMENTS='{"comments":[{"body":"AGENT-REVIEW: APPROVED\nReviewed-Commit: 999999999999"}]}'
GH_PR_STATE=OPEN check "a verdict on another commit does not retire it" "keep 1" 0 -- "$WF" reconcile --dry-run

# gh unreadable -> skip, never act on a failed read
cat > "${TMP}/gh-fail" <<'S'
#!/usr/bin/env bash
exit 1
S
chmod +x "${TMP}/gh-fail"
WORKFORCE_GH="${TMP}/gh-fail" check "an unreadable PR is skipped, not retired" "cannot read PR" 0 -- \
  "$WF" reconcile --dry-run
# ...and the SUMMARY must say so. A run that cannot reach GitHub at all otherwise
# prints a cheerful "retire 0, reap 0" and reads as a clean sweep -- which is what
# happened for real: homebrew's gh shadowed the token-injecting wrapper, every
# read failed, and the log said nothing was there to do.
WORKFORCE_GH="${TMP}/gh-fail" check "an unreachable forge is named in the summary" "SKIPPED 1" 0 -- \
  "$WF" reconcile --dry-run
unset GH_COMMENTS
rm -f "${REG}"/*.json

# --- finished sessions are cleared out of the list ---
# A background session that ended stays in `claude agents --all`, and in the
# app's session list, until something removes it. rev-436 sat there a day after
# finishing. LIVENESS IS MEMBERSHIP IN THE PLAIN LISTING, not the state word:
# rev-436 said "done" in --all and was absent from the plain listing.
rm -f "${REG}"/*.json
cat > "${TMP}/claude-two-lists" <<'S'
#!/usr/bin/env bash
if [[ "${1:-}" == "agents" ]]; then
  [[ "$*" == *--all* ]] && { printf '%s' "${AGENTS_ALL:-[]}"; exit 0; }
  printf '%s' "${AGENTS_JSON:-[]}"; exit 0
fi
exit 0
S
chmod +x "${TMP}/claude-two-lists"
export WORKFORCE_ABSENCE="${TMP}/absent.json"
rm -f "$WORKFORCE_ABSENCE"

# ABSENT ONCE IS NOT FINISHED. The first version cleared on a single sighting and
# deleted rev-4162-313 nine minutes into a live review, transcript and all. First
# sighting must only start a clock.
WORKFORCE_CLAUDE="${TMP}/claude-two-lists" \
AGENTS_JSON='[]' \
AGENTS_ALL='[{"id":"cccc3333","name":"rev-436","kind":"background","state":"done","cwd":"/"}]' \
  check "a first sighting starts a clock, it does not clear" "not clearing before" 0 -- \
  "$WF" reconcile
# ...and a dry run must not start that clock either, or looking becomes acting.
rm -f "$WORKFORCE_ABSENCE"
WORKFORCE_CLAUDE="${TMP}/claude-two-lists" \
AGENTS_JSON='[]' \
AGENTS_ALL='[{"id":"cccc3333","name":"rev-436","kind":"background","state":"done","cwd":"/"}]' \
  "$WF" reconcile --dry-run >/dev/null 2>&1
if [[ -f "$WORKFORCE_ABSENCE" ]]; then no "a dry run does not write the absence clock" "it wrote $WORKFORCE_ABSENCE"
else ok "a dry run does not write the absence clock"; fi

# Absent long enough: now it clears.
printf '{"cccc3333": 1}' > "$WORKFORCE_ABSENCE"
WORKFORCE_CLAUDE="${TMP}/claude-two-lists" \
AGENTS_JSON='[]' \
AGENTS_ALL='[{"id":"cccc3333","name":"rev-436","kind":"background","state":"done","cwd":"/"}]' \
  check "a long-absent session is cleared" "clear   rev-436" 0 -- "$WF" reconcile --dry-run

# Reappearing must clear the clock, not carry a stale one.
printf '{"cccc3333": 1}' > "$WORKFORCE_ABSENCE"
WORKFORCE_CLAUDE="${TMP}/claude-two-lists" \
AGENTS_JSON='[{"id":"cccc3333","name":"rev-436","kind":"background","state":"working","cwd":"/"}]' \
AGENTS_ALL='[{"id":"cccc3333","name":"rev-436","kind":"background","state":"working","cwd":"/"}]' \
  "$WF" reconcile >/dev/null 2>&1
if grep -q cccc3333 "$WORKFORCE_ABSENCE" 2>/dev/null; then
  no "reappearing clears the absence clock" "stale entry survived"
else ok "reappearing clears the absence clock"; fi
rm -f "$WORKFORCE_ABSENCE"

# Live in the plain listing: never cleared, whatever word it carries.
WORKFORCE_CLAUDE="${TMP}/claude-two-lists" \
AGENTS_JSON='[{"id":"cccc3333","name":"rev-436","kind":"background","state":"done","cwd":"/"}]' \
AGENTS_ALL='[{"id":"cccc3333","name":"rev-436","kind":"background","state":"done","cwd":"/"}]' \
  check "a live session saying done is not cleared" "keep 0" 0 -- "$WF" reconcile --dry-run

# Somebody else's finished session is not ours to delete.
# Assert the ABSENCE, with the right polarity: check() wants a string that IS
# present, so an "is not touched" case has to be written out longhand.
out="$(WORKFORCE_CLAUDE="${TMP}/claude-two-lists" AGENTS_JSON='[]' \
  AGENTS_ALL='[{"id":"dddd4444","name":"permitguv-suppliers","kind":"background","state":"done","cwd":"/"}]' \
  "$WF" reconcile --dry-run 2>&1)"
if [[ "$out" == *"clear"* ]]; then
  no "a finished session wf did not name is left alone" "proposed clearing it: ${out:0:200}"
else ok "a finished session wf did not name is left alone"; fi

# --- orphans: live, wf-named, no registry entry ---
# reconcile is registry-driven, so an entry lost for any reason makes its session
# invisible to it. rev-436 was reaped while alive by the old liveness allowlist.
WORKFORCE_CLAUDE="${TMP}/claude-two-lists" \
AGENTS_JSON='[{"id":"eeee5555","name":"reviewer-permitguv-99999","kind":"background","state":"idle","cwd":"'"${REPO}"'"}]' \
AGENTS_ALL='[{"id":"eeee5555","name":"reviewer-permitguv-99999","kind":"background","state":"idle","cwd":"'"${REPO}"'"}]' \
  check "a legacy name's trailing number is not read as a PR" "name carries no PR number" 0 -- \
  "$WF" reconcile --dry-run

WORKFORCE_CLAUDE="${TMP}/claude-two-lists" WORKFORCE_GH="${TMP}/gh-stub" GH_PR_STATE=MERGED \
AGENTS_JSON='[{"id":"eeee5555","name":"rev-w0rk-777","kind":"background","state":"idle","cwd":"'"${REPO}"'"}]' \
AGENTS_ALL='[{"id":"eeee5555","name":"rev-w0rk-777","kind":"background","state":"idle","cwd":"'"${REPO}"'"}]' \
  check "an orphan whose PR is closed is retired" "orphan  rev-w0rk-777" 0 -- "$WF" reconcile --dry-run

WORKFORCE_CLAUDE="${TMP}/claude-two-lists" WORKFORCE_GH="${TMP}/gh-stub" GH_PR_STATE=OPEN \
AGENTS_JSON='[{"id":"eeee5555","name":"rev-w0rk-777","kind":"background","state":"idle","cwd":"'"${REPO}"'"}]' \
AGENTS_ALL='[{"id":"eeee5555","name":"rev-w0rk-777","kind":"background","state":"idle","cwd":"'"${REPO}"'"}]' \
  check "an orphan on an open PR is left alone" "still OPEN, left alone" 0 -- "$WF" reconcile --dry-run

# A session retired by the registry pass must not come back round as an orphan.
# The orphan pass reads a snapshot taken before the registry pass deleted
# anything, so without a guard it is stopped twice and counted twice -- observed
# for real as "retire 4" covering two sessions.
rm -f "${REG}"/*.json
printf '%s' '{"agent_id":"ffff6666","bg_id":"ffff6666","name":"rev-w0rk-778","role":"reviewer","repo":"permitguv","pr":"778","head":"aaaaaaaabbbb","tag":"w0rk","token_key":"custom:X","managed_by":"workforce"}' > "${REG}/ffff6666.json"
WORKFORCE_CLAUDE="${TMP}/claude-two-lists" WORKFORCE_GH="${TMP}/gh-stub" GH_PR_STATE=MERGED \
AGENTS_JSON='[{"id":"ffff6666","name":"rev-w0rk-778","kind":"background","state":"idle","cwd":"'"${REPO}"'"}]' \
AGENTS_ALL='[{"id":"ffff6666","name":"rev-w0rk-778","kind":"background","state":"idle","cwd":"'"${REPO}"'"}]' \
  check "a retired session is not counted again as an orphan" "retire 1," 0 -- "$WF" reconcile
# NOT --dry-run, deliberately: a dry run deletes no registry entry, so the
# session still looks registered and the orphan pass skips it either way. The
# bug this guards only exists on a real run, and a dry-run test for it passes
# with the guard removed -- which is a test that cannot fail.
rm -f "${REG}"/*.json

# A reviewer must not register with Remote Control. Every session that does
# leaves an entry in the Claude app's session list that outlives it -- `offline`,
# permanent, and clearable only by hand in the app. A reviewer runs for minutes
# and posts a verdict; it has no reason to be drivable from a phone.
if python3 -c "
import json,sys
d=json.load(open('${HERE}/config/perm/reviewer.json'))
sys.exit(0 if d.get('disableRemoteControl') is True else 1)
" 2>/dev/null; then ok "the reviewer profile disables Remote Control"
else no "the reviewer profile disables Remote Control" "disableRemoteControl is not true"; fi

# --- stop / rm / watch ---
rm -f "${REG}"/*.json
printf '%s' '{"agent_id":"aaaa9999","bg_id":"aaaa9999","name":"rev-w0rk-900","role":"reviewer","repo":"permitguv","pr":"900","tag":"w0rk","token_key":"custom:X","managed_by":"workforce"}' > "${REG}/aaaa9999.json"
export AGENTS_JSON='[{"id":"aaaa9999","name":"rev-w0rk-900","kind":"background","state":"idle","cwd":"/"}]'
export AGENTS_ALL="$AGENTS_JSON"
WORKFORCE_CLAUDE="${TMP}/claude-two-lists" \
  check "stop resolves a session by name" "would stop rev-w0rk-900" 0 -- "$WF" stop rev-w0rk-900 --dry-run
WORKFORCE_CLAUDE="${TMP}/claude-two-lists" \
  check "stop resolves a session by ref prefix" "would stop rev-w0rk-900" 0 -- "$WF" stop aaaa --dry-run
WORKFORCE_CLAUDE="${TMP}/claude-two-lists" \
  check "an unknown target is refused" "no session matching" 1 -- "$WF" stop nosuchthing --dry-run
WORKFORCE_CLAUDE="${TMP}/claude-two-lists" \
  check "rm plans the registry drop too" "drop its registry entry" 0 -- "$WF" rm rev-w0rk-900 --dry-run

# An ambiguous prefix must refuse rather than pick one: stopping the wrong
# session is not recoverable by whoever typed the wrong thing.
export AGENTS_JSON='[{"id":"aaaa1111","name":"rev-a","kind":"background","state":"idle","cwd":"/"},
                     {"id":"aaaa2222","name":"rev-b","kind":"background","state":"idle","cwd":"/"}]'
export AGENTS_ALL="$AGENTS_JSON"
WORKFORCE_CLAUDE="${TMP}/claude-two-lists" \
  check "an ambiguous prefix is refused, not guessed" "matches 2 sessions" 1 -- "$WF" stop aaaa --dry-run

# A worktree with uncommitted work is kept, not removed.
mkdir -p "${TMP}/dirtywt" && git -C "${TMP}/dirtywt" init -q -b main 2>/dev/null
git -C "${TMP}/dirtywt" config user.email t@e.com; git -C "${TMP}/dirtywt" config user.name t
: > "${TMP}/dirtywt/f"; git -C "${TMP}/dirtywt" add -A; git -C "${TMP}/dirtywt" commit -qm i
echo dirty > "${TMP}/dirtywt/f"
rm -f "${REG}"/*.json
printf '%s' '{"agent_id":"bbbb9999","bg_id":"bbbb9999","name":"permitguv-dirt","role":"worker","repo":"permitguv","worktree":"'"${TMP}"'/dirtywt","tag":"dirt","token_key":"custom:X","managed_by":"workforce"}' > "${REG}/bbbb9999.json"
export AGENTS_JSON='[{"id":"bbbb9999","name":"permitguv-dirt","kind":"background","state":"idle","cwd":"/"}]'
export AGENTS_ALL="$AGENTS_JSON"
WORKFORCE_CLAUDE="${TMP}/claude-two-lists" \
  check "a dirty worktree is kept, not removed" "KEEP worktree" 0 -- "$WF" rm permitguv-dirt --dry-run
WORKFORCE_CLAUDE="${TMP}/claude-two-lists" \
  check "--force says what it is overriding" "FORCED over: uncommitted changes" 0 -- \
  "$WF" rm permitguv-dirt --force --dry-run

WORKFORCE_CLAUDE="${TMP}/claude-two-lists" \
  check "watch names the attach command" "claude attach bbbb9999" 0 -- "$WF" watch permitguv-dirt --dry-run
rm -rf "${TMP}/dirtywt"; rm -f "${REG}"/*.json

# --- tail ---
# Reviewers run with disableRemoteControl, so they are invisible to the app and
# to ListAgents by design. tail is how you watch one without giving that up.
# `claude logs` is raw ANSI terminal replay and is not readable; the transcript
# is line-delimited JSON and is.
SID="cafe1234-0000-0000-0000-000000000000"
TDIR="$HOME/.claude/projects/-wf-test-fixture"
mkdir -p "$TDIR"
cat > "$TDIR/${SID}.jsonl" <<'T'
{"type":"custom-title","title":"noise"}
{"type":"agent-setting","x":1}
{"type":"user","message":{"content":"Review pull request #900."}}
{"type":"assistant","message":{"content":[{"type":"text","text":"Reading the diff."}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git log --oneline -3"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","content":"abc123 a commit"}]}}
{"type":"attachment","x":1}
T
export AGENTS_JSON='[{"id":"cafe1234","sessionId":"'"$SID"'","name":"rev-fix-900","kind":"background","state":"working","cwd":"/"}]'
export AGENTS_ALL="$AGENTS_JSON"
out="$(WORKFORCE_CLAUDE="${TMP}/claude-two-lists" "$WF" tail rev-fix-900 -n 50 2>&1)"
for want in "Reading the diff." "git log --oneline -3" "Review pull request #900."; do
  if [[ "$out" == *"$want"$'\n'* || "$out" == *"$want" ]]; then ok "tail renders: ${want:0:28}"
  else no "tail renders: ${want:0:28}" "missing from: ${out:0:200}"; fi
done
# Bookkeeping rows and tool results are the bulk of a transcript; rendering them
# is how a tail becomes unreadable.
if [[ "$out" == *"noise"* || "$out" == *"abc123"* || "$out" == *"attachment"* ]]; then
  no "tail skips bookkeeping and tool results" "leaked: ${out:0:200}"
else ok "tail skips bookkeeping and tool results"; fi
# A session with no transcript must say so, not print nothing and exit 0.
export AGENTS_JSON='[{"id":"dead0000","sessionId":"dead0000-1111-1111-1111-111111111111","name":"rev-fix-901","kind":"background","state":"working","cwd":"/"}]'
export AGENTS_ALL="$AGENTS_JSON"
WORKFORCE_CLAUDE="${TMP}/claude-two-lists" \
  check "tail says so when there is no transcript" "no transcript on disk" 1 -- "$WF" tail rev-fix-901
rm -rf "$TDIR"
unset AGENTS_ALL

# ------------------------------------------------- token binding via settings
# The env a spawn exports does NOT reach the session: `claude --bg` is a client
# and the daemon spawns the session. So the binding is the --settings file, and
# these cases assert the thing that actually carries it.
#
# Failure here is silent in the worst way. A reviewer whose token did not bind
# falls through gh-wrapper's default to the human's own account, reads the pull
# request perfectly well, and signs a verdict that is a self-approval. That
# shipped permitguv#447 before this was fixed, twice, each time with the session
# disclosing the mismatch in its own verdict and unable to do anything about it.
SESS="${TMP}/session"
mkdir -p "$SESS"
export WORKFORCE_SESSION_DIR="$SESS"

check "spawn reports the token key it will bind" \
  "WF_GH_TOKEN_KEY=custom:TEST_REVIEW" 0 -- \
  "$WF" spawn reviewer permitguv --pr 9 --dry-run

check "spawn passes a per-session settings file" \
  "--settings ${SESS}/" 0 -- "$WF" spawn reviewer permitguv --pr 9 --dry-run

rm -f "${SESS}"/*.json
SF_OUT="$("$WF" spawn reviewer permitguv --pr 9 --dry-run 2>&1)"
# A dry run PRINTS the settings and writes nothing. The file binds a credential
# and pins a session's permissions, so reading exactly what would be written is
# the point of a dry run; creating it during one is a side effect a dry run must
# not have.
if ls "${SESS}"/*.json >/dev/null 2>&1; then
  no "a dry run writes no settings file" "$(ls "${SESS}"/*.json)"
else
  ok "a dry run writes no settings file"
fi
grep -q '"WF_GH_TOKEN_KEY": "custom:TEST_REVIEW"' <<<"$SF_OUT" \
  && ok "settings carry the key name" \
  || no "settings carry the key name" "env block absent"
# The role profile must survive the merge, or the spawn silently drops the
# permission rules the role exists to impose.
grep -q '"permissions"' <<<"$SF_OUT" \
  && ok "settings keep the role permission profile" \
  || no "settings keep the role permission profile" "permissions dropped"
# The KEY, never the value. The scredmgr stub returns ghp_STUBTOKEN_VALUE, so
# this fails loudly if anyone ever resolves the secret before writing.
grep -q 'ghp_STUBTOKEN_VALUE' <<<"$SF_OUT" \
  && no "settings hold the key, not the secret" "a token VALUE was printed" \
  || ok "settings hold the key, not the secret"
# --------------------------------- no token in the spawn environment
# `claude --bg` is a client, and when no daemon is running it starts one that
# inherits this environment for its lifetime. A token exported here bound the
# DAEMON, not the session, and the daemon then handed it to every session it
# spawned afterwards regardless of role -- which is how a reviewer for one repo
# came up holding a different repo's reviewer account and 404'd on a private repo.
#
# So wf must clear GH_TOKEN/GITHUB_TOKEN before exec. Asserted by making the
# stub claude print its own view of them.
cat > "${TMP}/claude-echo-env" <<'S'
#!/usr/bin/env bash
if [[ "${1:-}" == "agents" ]]; then printf '%s' "${AGENTS_JSON:-[]}"; exit 0; fi
# wf captures our stdout and prints its own summary, so report through a file.
printf 'GH_TOKEN=[%s] GITHUB_TOKEN=[%s]\n' "${GH_TOKEN:-}" "${GITHUB_TOKEN:-}" > "${SPAWNENV_OUT}"
echo "aaaa1111"
exit 0
S
chmod +x "${TMP}/claude-echo-env"
SPAWNENV_OUT="${TMP}/spawnenv.txt"
GH_TOKEN="ghp_AMBIENT_SHOULD_NOT_TRAVEL" GITHUB_TOKEN="ghp_ALSO_NOT" \
  SPAWNENV_OUT="$SPAWNENV_OUT" WORKFORCE_CLAUDE="${TMP}/claude-echo-env" \
  WORKFORCE_SESSION_DIR="$SESS" "$WF" spawn reviewer permitguv --pr 11 >/dev/null 2>&1
if [[ "$(cat "$SPAWNENV_OUT" 2>/dev/null)" == "GH_TOKEN=[] GITHUB_TOKEN=[]" ]]; then
  ok "no ambient token reaches the spawned client"
else
  no "no ambient token reaches the spawned client" "got: $(cat "$SPAWNENV_OUT" 2>/dev/null)"
fi

# The key still has to be validated: an unreadable one must fail the spawn
# rather than starting a session that silently holds the wrong account.
cat > "${TMP}/scredmgr-empty" <<'S'
#!/usr/bin/env bash
exit 1
S
chmod +x "${TMP}/scredmgr-empty"
WORKFORCE_SCREDMGR="${TMP}/scredmgr-empty" WORKFORCE_SESSION_DIR="$SESS" \
  check "an unreadable token key fails the spawn" "could not read" 1 -- \
  "$WF" spawn reviewer permitguv --pr 12 --dry-run

# ---------------------------------------------------------------- canary
# The suite must be able to fail. If this ever passes, the harness is broken and
# every "ok" above is worthless -- which is the failure mode this guards.
CANARY_OUT="$("$WF" spawn worker permitguv "x" --dry-run 2>&1)"
if [[ "$CANARY_OUT" == *"this string is not in the output"* ]]; then
  no "harness canary" "check() matched a string that cannot be present"
else
  ok "harness canary (a wrong expectation would fail)"
fi

echo
echo "ran ${ran}, passed ${pass}, failed ${fail}"
[[ "${ran}" -ge 81 ]] || { echo "FAIL: only ${ran} cases ran; expected at least 81"; exit 1; }
[[ "${fail}" -eq 0 ]] || exit 1
