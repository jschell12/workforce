// Package worktree decides whether a session's worktree can be thrown away.
//
// The bar is "nothing would be lost", not "probably fine". Every uncertain
// answer is a refusal, because the two outcomes are not comparable: keeping a
// directory that could have gone costs disk, and removing one that should have
// stayed destroys work nobody has a copy of.
package worktree

import (
	"os"
	"os/exec"
	"strings"
)

// Verdict is the answer and the reason, which is always printed. A refusal
// nobody can read is a refusal nobody can act on.
type Verdict struct {
	Safe bool
	Why  string
}

// Runner runs a git command in a directory. Injected so tests can drive real
// repositories without this package reaching for a global.
type Runner func(dir string, args ...string) (stdout string, err error)

// Git is the default Runner.
func Git(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return string(out), err
}

// SafeToRemove reports whether a worktree holds nothing that would be lost.
func SafeToRemove(path string, run Runner) Verdict {
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		return Verdict{true, "already gone"}
	}
	dirty, err := run(path, "status", "--porcelain")
	if err != nil {
		// Not readable is not the same as clean. A directory git cannot answer
		// for is exactly the one not to delete.
		return Verdict{false, "cannot read git status"}
	}
	if strings.TrimSpace(dirty) != "" {
		return Verdict{false, "uncommitted changes"}
	}
	// `@{u}` fails when the branch has no upstream, which is itself the reason
	// to keep it: commits that exist in exactly one place are the ones worth
	// protecting.
	ahead, err := run(path, "rev-list", "--count", "@{u}..HEAD")
	if err != nil {
		return Verdict{false, "no upstream branch"}
	}
	n := strings.TrimSpace(ahead)
	if n != "" && n != "0" {
		return Verdict{false, n + " unpushed commit(s)"}
	}
	return Verdict{true, "clean and pushed"}
}
