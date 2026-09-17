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
type Pairs interface {
	Load() map[string]Pair
	Save(map[string]Pair)
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

func (f FilePairs) Save(m map[string]Pair) {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return
	}
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

// Record adds or replaces one pairing. Called at spawn, not at sweep.
func Record(p Pairs, name string, pair Pair) {
	if p == nil || name == "" || pair.Watching == "" {
		return
	}
	m := p.Load()
	m[name] = pair
	p.Save(m)
}
