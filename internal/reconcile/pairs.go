package reconcile

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Pair records that one session exists only to accompany another.
//
// The registry cannot hold this. It lives under a repository's git common dir,
// and a machine-scoped role has no repository, so a role paired to a caller has
// nowhere in the registry to say so. This file is machine-level, beside the
// absent-since clock, for the same reason that one is.
type Pair struct {
	// BgID is the accompanying session: the one that gets stopped.
	BgID string `json:"bg_id"`
	// Watching is the ref of the session it accompanies. When that session is
	// gone for good, the accompanying one has nothing left to do.
	Watching string `json:"watching"`

	// MissingSince is when the watched session was FIRST seen absent, or zero
	// while it is present.
	//
	// The pairing keeps its own clock rather than borrowing the absent-since
	// one, which was the first design and was wrong: that map is pruned by the
	// pass that owns it, which does not know these refs and drops them, so the
	// clock reset to zero on every sweep and the window never elapsed. Two
	// passes sharing one map with different key populations is the bug; a
	// record that carries its own clock cannot have it.
	MissingSince int64 `json:"missing_since,omitempty"`
}

// Pairs persists the set, keyed by the accompanying session's name.
//
// Update is the only mutator, deliberately. Load-then-Save from two processes
// loses whichever write lands second, and this file has two writers with very
// different rhythms: a spawn, occasionally, and a sweep every two minutes. A
// spawn landing inside a sweep's window would have its pairing dropped, and the
// session it just started would then never retire -- the exact condition the
// pairing exists to remove, returning quietly and rarely.
//
// The atomic rename that was already here prevents a CORRUPT file. It does
// nothing about a lost update, which is a different failure and the one that
// bites.
type Pairs interface {
	// Update runs fn against the current set under an exclusive lock and
	// persists the result when fn reports a change. fn must be quick: it holds
	// the lock, and a spawn blocks behind it.
	Update(fn func(map[string]Pair) bool)
	// Load is a read without the lock, for callers that only report.
	Load() map[string]Pair
}

// FilePairs persists to JSON.
type FilePairs struct{ Path string }

func (f FilePairs) Load() map[string]Pair {
	b, err := os.ReadFile(f.Path)
	if err != nil {
		return map[string]Pair{}
	}
	var m map[string]Pair
	if err := json.Unmarshal(b, &m); err != nil {
		// A corrupt file is not a reason to start stopping sessions. Reading it
		// as empty means nothing is retired by pairing until it is rewritten,
		// which is the harmless direction.
		return map[string]Pair{}
	}
	return m
}

func (f FilePairs) save(m map[string]Pair) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	tmp := f.Path + ".new"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, f.Path)
}

// Update takes an exclusive lock on a sidecar, re-reads inside it, applies fn,
// and writes back. The lock is on `<path>.lock` rather than on the file itself
// because the write replaces the file by rename, which would drop a lock held
// on the old inode.
//
// A lock that cannot be taken is not a reason to skip the write: losing the
// pairing is the failure being prevented. It proceeds unlocked and accepts the
// small race rather than guaranteeing the loss.
func (f FilePairs) Update(fn func(map[string]Pair) bool) {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return
	}
	if unlock, err := lockFile(f.Path + ".lock"); err == nil {
		defer unlock()
	}
	m := f.Load()
	if fn(m) {
		f.save(m)
	}
}

// Record adds or replaces one pairing. Called at spawn, not at sweep.
func Record(p Pairs, name string, pair Pair) {
	if p == nil || name == "" || pair.Watching == "" {
		return
	}
	p.Update(func(m map[string]Pair) bool {
		m[name] = pair
		return true
	})
}
