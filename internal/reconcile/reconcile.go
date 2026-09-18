// Package reconcile retires finished sessions and drops registry entries that
// nothing backs.
//
// It is the only destructive thing wf does, and a launchd job runs it every 120
// seconds, so its selection rules matter more than its output. Both jobs are
// keyed on state written by the forge or by the session list, never by this
// tool: a sweep that can satisfy its own exit condition is a sweep that acts on
// its own output.
package reconcile

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jschell12/workforce/internal/config"
	"github.com/jschell12/workforce/internal/registry"
	"github.com/jschell12/workforce/internal/session"
)

// Forge is the pull-request state this sweep reads. An implementation that
// cannot answer must return an error rather than a zero value: "cannot read"
// and "not open" lead to opposite actions, and conflating them retires a live
// reviewer whenever the network hiccups.
type Forge interface {
	PRState(slug, pr string) (string, error)
	SignedAt(slug, pr, head string) (bool, error)
}

// Control stops and removes sessions.
type Control interface {
	Stop(ref string) error
	Remove(ref string) error
}

// Store is the per-repository registry this sweep may edit.
type Store interface {
	Read() ([]registry.Entry, error)
	Remove(registry.Entry) error
}

// Options configures one sweep.
type Options struct {
	DryRun  bool
	Out     io.Writer
	Absence Absence // where the absent-since clock lives; nil disables the clear pass
	Pairs   Pairs   // caller pairings; nil disables the pairing pass
	Now     int64   // unix seconds; zero means time.Now()
}

// Result counts what a sweep did.
type Result struct {
	Retired, Reaped, Kept, Skipped, Orphans, Cleared, Unpaired int
}

func (r Result) String() string {
	s := fmt.Sprintf("retire %d, reap %d, keep %d", r.Retired, r.Reaped, r.Kept)
	if r.Skipped > 0 {
		// Shouted on purpose: never let an unreachable forge read as a quiet,
		// successful run. A sweep that could not see GitHub kept everything
		// because it was blind, not because everything was fine.
		s += fmt.Sprintf(", SKIPPED %d (could not read the forge)", r.Skipped)
	}
	if r.Cleared > 0 {
		s += fmt.Sprintf(", clear %d", r.Cleared)
	}
	if r.Unpaired > 0 {
		s += fmt.Sprintf(", unpaired %d", r.Unpaired)
	}
	if r.Orphans > 0 {
		s += fmt.Sprintf(", orphans seen %d", r.Orphans)
	}
	return s
}

// Repo pairs a configured repository with the registry it owns.
type Repo struct {
	Name  string
	Slug  string
	Path  string
	Store Store
}

// ownedByWF reports whether wf wrote this entry and may therefore act on it.
//
// agent-coordination/active/ is SHARED. Every agent following the multi-agent
// protocol registers there in its own schema (pid, worktree, branch, areas, and
// no bg_id), several of them live, and deleting one deregisters an agent
// mid-task. Caught on the first dry run against a real registry: the sweep
// proposed reaping ten foreign entries alongside its own three.
//
// The second clause covers entries wf wrote before the marker existed: bg_id
// plus token_key is a shape the protocol schema does not have. Drop it once no
// pre-marker entries remain.
func ownedByWF(e registry.Entry) bool { return e.OwnedByWF() }

var revName = regexp.MustCompile(`^rev-(?:[0-9a-z]+-)?(\d+)$`)

// Run performs one sweep.
func Run(repos []Repo, live session.Plain, all []session.Session, forge Forge, ctl Control, opt Options) (Result, error) {
	var res Result
	liveRefs := map[string]bool{}
	stateOf := map[string]string{}
	for _, s := range live.Live() {
		liveRefs[s.Ref()] = true
		stateOf[s.Ref()] = s.StateWord()
	}

	// Snapshotted BEFORE the registry pass deletes anything. Computed after, every
	// session this run retires looks unregistered, so the orphan pass picks it up
	// again. That only shows on a real run: a dry run deletes nothing and the bug
	// hides.
	knownAtStart := map[string]bool{}
	entriesByRepo := make([][]registry.Entry, len(repos))
	for i, r := range repos {
		entries, err := r.Store.Read()
		if err != nil {
			continue
		}
		entriesByRepo[i] = entries
		for _, e := range entries {
			if e.BgID != "" {
				knownAtStart[e.BgID] = true
			}
		}
	}

	// Refs this run has already acted on. The orphan pass reads a snapshot taken
	// before the registry pass deleted anything, so without this a session
	// retired above comes back round as an orphan: stopped twice, counted twice.
	handled := map[string]bool{}
	act := ""
	if opt.DryRun {
		act = "would "
	}

	for i, repo := range repos {
		for _, e := range entriesByRepo[i] {
			if !ownedByWF(e) {
				continue
			}
			ref, name := e.BgID, e.Name
			if name == "" {
				name = "?"
			}

			// A blank ref joins no live session by construction, so reaping on
			// it removes the entry of a session that is running. It also puts
			// "" into handled, which suppresses the clear pass for every row
			// with an empty ref.
			if ref == "" {
				fmt.Fprintf(opt.Out, "skip    %s [] entry has no bg_id; cannot tell if it is live\n", name)
				res.Kept++
				res.Skipped++
				continue
			}
			if !liveRefs[ref] {
				fmt.Fprintf(opt.Out, "reap    %s [%s] %sdrop registry entry (no live session)\n", name, ref, act)
				if !opt.DryRun {
					// NOT swallowed. This is the one error that means "I
					// refused to touch something", and dropping it made the
					// line and the counter both claim a drop that did not
					// happen, reprinted every 120 seconds.
					if err := repo.Store.Remove(e); err != nil {
						fmt.Fprintf(opt.Out, "        NOT dropped: %v\n", err)
						res.Kept++
						continue
					}
				}
				handled[ref] = true
				res.Reaped++
				continue
			}
			if e.Role != "reviewer" || e.PR == "" {
				res.Kept++
				continue
			}

			why, skip := retireReason(repo.Slug, e, stateOf[ref], forge)
			if skip != "" {
				fmt.Fprintf(opt.Out, "skip    %s [%s] %s\n", name, ref, skip)
				res.Kept++
				res.Skipped++
				continue
			}
			if why == "" {
				res.Kept++
				continue
			}
			handled[ref] = true
			fmt.Fprintf(opt.Out, "retire  %s [%s] %sstop+rm (%s)\n", name, ref, act, why)
			if !opt.DryRun {
				_ = ctl.Stop(ref)
				_ = ctl.Remove(ref)
				if err := repo.Store.Remove(e); err != nil {
					fmt.Fprintf(opt.Out, "        session stopped, entry NOT dropped: %v\n", err)
				}
			}
			res.Retired++
		}
	}

	res.Orphans = sweepOrphans(repos, live, knownAtStart, handled, forge, ctl, opt, &res)
	sweepAbsent(live, all, handled, ctl, opt, &res)
	sweepPairs(liveRefs, stateOf, handled, ctl, opt, &res)
	return res, nil
}

// sweepPairs retires a session that exists only to accompany another, once the
// session it accompanies is gone.
//
// It is here because reconcile's other passes are reviewer-shaped: they select
// on `rev-` names and on a registry entry with role "reviewer", and a
// machine-scoped role has neither. Without this, an accompanying session polls
// on forever after the thing it was watching ended, and a cap of one on a fixed
// name means the next spawn refuses until somebody stops it by hand.
//
// ABSENT ONCE IS NOT GONE. The watched session's ref is put through the same
// absent-since clock the clear pass uses, for the same reason and with the same
// window: a single sweep cannot tell "ended" from "not listed at this instant",
// and acting on one sighting is what once reaped a reviewer nine minutes into
// its review. A watcher left running half an hour too long costs a little
// money. One stopped early takes the correction it was about to make with it.
func sweepPairs(liveRefs map[string]bool, stateOf map[string]string, handled map[string]bool, ctl Control, opt Options, res *Result) {
	if opt.Pairs == nil {
		return
	}
	now := opt.Now
	if now == 0 {
		now = time.Now().Unix()
	}

	// Two phases on purpose. The decision runs under the file lock so a spawn
	// cannot lose its row to this sweep; the stopping runs outside it, because
	// `claude stop` is a subprocess and holding a lock across it would block
	// every spawn for as long as it takes.
	type doomed struct {
		name, bgID, watching string
		gone                 int64
	}
	var retire []doomed

	opt.Pairs.Update(func(pairs map[string]Pair) bool {
		changed := false
		for name, pair := range pairs {
			// The accompanying session is already gone: the record is all that
			// is left, and keeping it would hold a cap against nothing. This is
			// also what clears a record left behind below, when an earlier pass
			// had already stopped the session.
			if !liveRefs[pair.BgID] {
				delete(pairs, name)
				changed = true
				continue
			}
			// A wedged watcher is doing nothing and cannot be woken, but it is
			// still listed, so it holds the cap. Its own clock, on the same
			// sustained-window discipline: `blocked` seen once is not `blocked`
			// for good, and stopping a session that was briefly slow would take
			// whatever it was about to say with it.
			if strings.EqualFold(stateOf[pair.BgID], "blocked") {
				if pair.WedgedSince == 0 {
					pair.WedgedSince = now
					pairs[name] = pair
					changed = true
					res.Kept++
					continue
				}
				if now-pair.WedgedSince >= AbsentBeforeClear && !handled[pair.BgID] {
					retire = append(retire, doomed{name, pair.BgID, "itself (wedged)", now - pair.WedgedSince})
					if !opt.DryRun {
						delete(pairs, name)
						changed = true
					}
					continue
				}
				res.Kept++
				continue
			}
			if pair.WedgedSince != 0 {
				// It came back. A wedge that did not stick is not a wedge.
				pair.WedgedSince = 0
				pairs[name] = pair
				changed = true
			}
			if liveRefs[pair.Watching] {
				// Up. An earlier absence did not stick, so the clock resets.
				if pair.MissingSince != 0 {
					pair.MissingSince = 0
					pairs[name] = pair
					changed = true
				}
				res.Kept++
				continue
			}
			if pair.MissingSince == 0 {
				pair.MissingSince = now
				pairs[name] = pair
				changed = true
				res.Kept++
				continue
			}
			if now-pair.MissingSince < AbsentBeforeClear {
				res.Kept++
				continue
			}
			if handled[pair.BgID] {
				// Another pass stopped it this sweep. Leave the record; the
				// next sweep clears it through the liveRefs check above.
				continue
			}
			retire = append(retire, doomed{name, pair.BgID, pair.Watching, now - pair.MissingSince})
			if !opt.DryRun {
				delete(pairs, name)
				changed = true
			}
		}
		return changed && !opt.DryRun
	})

	for _, d := range retire {
		fmt.Fprintf(opt.Out, "reconcile: unpair %s [%s] watched %s, gone for %ds\n",
			d.name, d.bgID, d.watching, d.gone)
		if !opt.DryRun {
			_ = ctl.Stop(d.bgID)
			_ = ctl.Remove(d.bgID)
		}
		handled[d.bgID] = true
		res.Unpaired++
	}
}

// sweepAbsent removes sessions that are still listed by --all but have been
// gone from the live listing for a sustained window.
//
// The window is the whole mechanism; see AbsentBeforeClear for what happened
// without it. The clock starts at the first sighting and is kept in a file,
// because one sweep cannot tell "gone" from "not listed at this instant".
//
// Saving only the refs seen absent THIS run is what resets the clock for a
// session that came back: a ref missing from the new map has its history
// dropped, so a later disappearance starts counting again from zero.
func sweepAbsent(live session.Plain, all []session.Session, handled map[string]bool,
	ctl Control, opt Options, res *Result) {
	if opt.Absence == nil || len(all) == 0 {
		return
	}
	now := opt.Now
	if now == 0 {
		now = time.Now().Unix()
	}
	liveNow := map[string]bool{}
	for _, s := range live {
		liveNow[s.Ref()] = true
	}
	seen := opt.Absence.Load()
	stillAbsent := map[string]int64{}
	act := ""
	if opt.DryRun {
		act = "would "
	}
	for _, s := range all {
		ref, name := s.Ref(), s.Name
		if liveNow[ref] || handled[ref] {
			continue
		}
		if !strings.HasPrefix(name, "rev-") && !strings.HasPrefix(name, "reviewer-") {
			continue
		}
		first, ok := seen[ref]
		if !ok {
			first = now
		}
		stillAbsent[ref] = first
		age := now - first
		if age < AbsentBeforeClear {
			fmt.Fprintf(opt.Out, "wait    %s [%s] absent %ds; not clearing before %ds\n",
				name, ref, age, AbsentBeforeClear)
			res.Kept++
			continue
		}
		fmt.Fprintf(opt.Out, "clear   %s [%s] %srm (absent %ds, still listed)\n", name, ref, act, age)
		if !opt.DryRun {
			_ = ctl.Remove(ref)
		}
		res.Cleared++
	}
	// A dry run must not advance the clock: reading the output would otherwise
	// bring a real deletion closer.
	if !opt.DryRun {
		opt.Absence.Save(stillAbsent)
	}
}

// retireReason returns why this reviewer's round is over, or a skip reason.
func retireReason(slug string, e registry.Entry, state string, forge Forge) (why, skip string) {
	st, err := forge.PRState(slug, e.PR)
	if err != nil {
		return "", fmt.Sprintf("cannot read PR #%s", e.PR)
	}
	if st != "OPEN" {
		// Its job is moot regardless of what it is doing.
		return fmt.Sprintf("PR #%s is %s", e.PR, st), ""
	}
	if e.Head == "" {
		return "", ""
	}
	signed, err := forge.SignedAt(slug, e.PR, e.Head)
	if err != nil {
		return "", fmt.Sprintf("cannot read verdicts on #%s", e.PR)
	}
	// Only when it is not mid-turn: a verdict may be posted before the session
	// has finished writing up, and stopping it there truncates the one artifact
	// anyone reads.
	// Lowercased, and see session.deadStates for why an allowlist is the wrong
	// polarity: this is the last place an unrecognised state word gets a live
	// session stopped. It stays an allowlist because the question is "is it
	// mid-turn", not "is it alive", but it must at least not miss "Working".
	if signed && !midTurn(state) {
		return "signed at " + short(e.Head), ""
	}
	return "", ""
}

// sweepOrphans handles live sessions wf started that no registry entry knows
// about.
//
// reconcile is registry-driven, so an entry lost for any reason makes its
// session invisible to it forever. Not hypothetical: one reviewer was reaped
// while alive by a liveness allowlist that read `blocked` as dead, then ran for
// a day with nothing able to see it.
//
// It acts ONLY when the name itself carries the pull request and that pull
// request is closed. `rev-<tag>-<pr>` and `rev-<pr>` do; `reviewer-<repo>-<stamp>`
// does NOT, because its trailing number is a timestamp and reading it as a pull
// request number would stop a live reviewer on a pull request nobody named.
// Those are reported and left alone.
func sweepOrphans(repos []Repo, live session.Plain, known, handled map[string]bool,
	forge Forge, ctl Control, opt Options, res *Result) int {
	slugByPath := map[string]string{}
	for _, r := range repos {
		slugByPath[filepath.Clean(r.Path)] = r.Slug
	}
	act := ""
	if opt.DryRun {
		act = "would "
	}
	n := 0
	for _, s := range live.Live() {
		ref, name := s.Ref(), s.Name
		if known[ref] || handled[ref] || !strings.HasPrefix(name, "rev-") && !strings.HasPrefix(name, "reviewer-") {
			continue
		}
		n++
		m := revName.FindStringSubmatch(name)
		slug := slugByPath[filepath.Clean(s.Cwd)]
		if m == nil || slug == "" {
			reason := "cwd is not a known repo"
			if m == nil {
				reason = "name carries no PR number"
			}
			fmt.Fprintf(opt.Out, "orphan  %s [%s] no registry entry; %s, left alone\n", name, ref, reason)
			continue
		}
		st, err := forge.PRState(slug, m[1])
		if err != nil {
			fmt.Fprintf(opt.Out, "orphan  %s [%s] cannot read PR #%s, left alone\n", name, ref, m[1])
			res.Skipped++
			continue
		}
		if st == "OPEN" {
			fmt.Fprintf(opt.Out, "orphan  %s [%s] PR #%s still %s, left alone\n", name, ref, m[1], st)
			continue
		}
		fmt.Fprintf(opt.Out, "orphan  %s [%s] %sstop+rm (PR #%s is %s)\n", name, ref, act, m[1], st)
		if !opt.DryRun {
			_ = ctl.Stop(ref)
			_ = ctl.Remove(ref)
		}
		res.Retired++
	}
	return n
}

// expandHome resolves a leading ~, which roles.toml uses throughout.
func expandHome(p string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~"))
}

// midTurn reports whether a session is still producing output.
func midTurn(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "working", "busy", "thinking", "compacting":
		return true
	}
	return false
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// Repos adapts a parsed config into the sweep's view of the world.
//
// expand is applied to Path as well as handed to open. Every path in roles.toml
// is written with a leading ~, and the orphan pass compares Path against a
// session's cwd, which is absolute. Storing the raw form made that pass match
// nothing on every real machine while printing "cwd is not a known repo": no
// damage, and no orphan ever acted on either.
func Repos(c *config.Config, open func(path string) (Store, error)) []Repo {
	var out []Repo
	for name, r := range c.Repos {
		st, err := open(r.Path)
		if err != nil {
			continue
		}
		slug := r.Slug
		if slug == "" {
			slug = name
		}
		out = append(out, Repo{Name: name, Slug: slug, Path: expandHome(r.Path), Store: st})
	}
	return out
}
