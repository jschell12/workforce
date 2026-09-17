// Package registry reads and writes the shared agent-coordination state.
//
// The directory lives under the repository's GIT COMMON DIR, not inside any one
// worktree, because every worktree shares that path and the whole point is that
// concurrent sessions can see each other. It is untracked local state.
//
// IT IS NOT WF'S DIRECTORY. Any agent following the multi-agent protocol
// registers here, so an entry with no `tool` field may belong to something wf
// has never heard of, and deleting it deregisters a live agent mid-task. Every
// destructive path must go through OwnedByWF.
package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Marker is the value of an entry's managed_by field for entries this tool
// wrote. Its absence is not proof of anything except that wf should keep its
// hands off.
//
// The field name and value are load-bearing for interop, not cosmetic: during
// the port both implementations read this directory, and a Go build that
// stamped a different marker would refuse to retire entries the Python wrote
// and vice versa, leaving sessions registered forever with nothing complaining.
const Marker = "workforce"

// Entry is one registered session.
//
// The field names and types mirror exactly what the Python implementation
// writes, because both read this directory during the port. created_at and
// heartbeat_at are unix seconds as NUMBERS: typing them as strings made
// encoding/json reject every real entry, and Read's skip-the-corrupt rule then
// dropped them silently, so `sessions` showed live workers with no role.
type Entry struct {
	AgentID     string `json:"agent_id"`
	BgID        string `json:"bg_id,omitempty"`
	Name        string `json:"name,omitempty"`
	Role        string `json:"role,omitempty"`
	Persona     string `json:"persona,omitempty"`
	Repo        string `json:"repo,omitempty"`
	Worktree    string `json:"worktree,omitempty"`
	Branch      string `json:"branch,omitempty"`
	PR          string `json:"pr,omitempty"`
	Head        string `json:"head,omitempty"`
	Tag         string `json:"tag,omitempty"`
	ForTag      string `json:"for_tag,omitempty"`
	ForName     string `json:"for_name,omitempty"`
	ForSession  string `json:"for_session,omitempty"`
	ForKind     string `json:"for_kind,omitempty"`
	ManagedBy   string `json:"managed_by,omitempty"`
	TokenKey    string `json:"token_key,omitempty"`
	Status      string `json:"status,omitempty"`
	CreatedAt   int64  `json:"created_at,omitempty"`
	HeartbeatAt int64  `json:"heartbeat_at,omitempty"`

	// path is where this entry was read from; not serialized.
	path string
}

// Path is the file this entry came from, empty for one not yet written.
func (e Entry) Path() string { return e.path }

// OwnedByWF reports whether wf wrote this entry and may therefore remove it.
//
// The second clause covers entries wf wrote before the marker existed: bg_id
// plus token_key is a shape the multi-agent protocol schema does not have,
// since it registers a pid and no token. Drop it once no pre-marker entries
// remain.
//
// It lives HERE rather than in the sweep that used to hold a second copy of it.
// The two disagreed: the sweep claimed a legacy entry and Remove then refused
// it, so the log and the counter both reported a drop that never happened.
func (e Entry) OwnedByWF() bool {
	return e.ManagedBy == Marker || (e.BgID != "" && e.TokenKey != "")
}

// Store is the coordination directory for one repository.
type Store struct{ dir string }

// Dir is the directory entries live in.
func (s Store) Dir() string { return s.dir }

// Open locates the coordination directory for a repository. It does not create
// it: reading must work on a repo where no agent has ever registered.
func Open(repoPath string, gitCommonDir func(string) (string, error)) (Store, error) {
	common, err := gitCommonDir(repoPath)
	if err != nil {
		return Store{}, err
	}
	return Store{dir: filepath.Join(common, "agent-coordination", "active")}, nil
}

// Read returns every entry, skipping any that will not parse.
//
// A corrupt file is skipped rather than fatal: it is not a live session, and
// refusing to read the whole registry because one agent wrote a truncated file
// would take the fleet down for a cosmetic fault.
func (s Store) Read() ([]Entry, error) {
	names, err := filepath.Glob(filepath.Join(s.dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	out := make([]Entry, 0, len(names))
	for _, n := range names {
		b, err := os.ReadFile(n)
		if err != nil {
			continue
		}
		var e Entry
		if err := json.Unmarshal(b, &e); err != nil {
			continue
		}
		e.path = n
		out = append(out, e)
	}
	return out, nil
}

// Write stores an entry atomically.
//
// Temp file then rename, because other worktrees read this directory
// concurrently and a half-written file parses as corrupt. Write also stamps
// Tool so a later sweep can tell its own entries from another agent's.
func (s Store) Write(e Entry) (string, error) {
	if e.AgentID == "" {
		return "", fmt.Errorf("entry needs an agent_id")
	}
	if strings.ContainsAny(e.AgentID, `/\`) {
		return "", fmt.Errorf("agent_id %q cannot contain a path separator", e.AgentID)
	}
	e.ManagedBy = Marker
	now := time.Now().Unix()
	if e.CreatedAt == 0 {
		e.CreatedAt = now
	}
	e.HeartbeatAt = now
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(s.dir, e.AgentID+".json")
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(s.dir, e.AgentID+".*.tmp")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return "", err
	}
	return dest, nil
}

// Remove deletes an entry, refusing one wf did not write.
func (s Store) Remove(e Entry) error {
	if !e.OwnedByWF() {
		return fmt.Errorf("entry %q was not written by %s; removing it would deregister another agent mid-task", e.AgentID, Marker)
	}
	if e.path == "" {
		return fmt.Errorf("entry %q has no path to remove", e.AgentID)
	}
	return os.Remove(e.path)
}

// GitCommonDir is the default resolver: the directory every worktree of a
// repository shares. Passed into Open rather than called by it so tests can
// substitute one without a repository on disk.
func GitCommonDir(repoPath string) (string, error) {
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return "", fmt.Errorf("%s is not a git repository", repoPath)
	}
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) {
		p = filepath.Join(repoPath, p)
	}
	return filepath.Abs(p)
}
