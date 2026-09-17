// Package forge reads pull-request state through the gh CLI.
//
// Every read names the repository explicitly. `refs/pull/<n>/head` and a bare
// pull request number both resolve against whatever repository the process is
// standing in, and numbers collide across repos: measured on one night, six of
// six pull requests reviewed had a same-numbered pull request in another repo
// at a different commit. The failure is silent, because the wrong answer is a
// real answer about a real pull request.
package forge

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// Client shells out to gh.
type Client struct{ Bin string }

// PRState returns OPEN, MERGED or CLOSED.
//
// An empty or unrecognised state is an ERROR, not a value. A body of `null`,
// `{}`, or any object without `state` unmarshals cleanly to "", and a caller
// asking "is it still OPEN" reads that as no. reconcile then stops and removes
// a live reviewer mid-review, taking its transcript, from a sweep that runs
// every 120 seconds off a launchd timer.
//
// The Python guaranteed this in gh_json's docstring: "None and {} are different
// answers and reconcile must not conflate them: a failed read is not 'no
// verdict', and acting on it would retire a live reviewer mid-review." Enforced
// HERE rather than at each call site, because the next call site will forget.
func (c Client) PRState(slug, pr string) (string, error) {
	var out struct {
		State string `json:"state"`
	}
	if err := c.json(slug, pr, "state", &out); err != nil {
		return "", err
	}
	switch out.State {
	case "OPEN", "MERGED", "CLOSED":
		return out.State, nil
	case "":
		return "", fmt.Errorf("gh pr view %s --repo %s returned no state field", pr, slug)
	default:
		return "", fmt.Errorf("gh pr view %s --repo %s returned an unrecognised state %q", pr, slug, out.State)
	}
}

// HeadBranch is the branch a pull request was opened from.
func (c Client) HeadBranch(slug, pr string) (string, error) {
	var out struct {
		HeadRefName string `json:"headRefName"`
	}
	if err := c.json(slug, pr, "headRefName", &out); err != nil {
		return "", err
	}
	return out.HeadRefName, nil
}

// HeadOID is the commit a reviewer is answering for.
//
// Recorded at spawn because approval binds to a commit: without it reconcile
// can only retire on the pull request closing, and the "signed at <head>" route
// never fires at all.
func (c Client) HeadOID(slug, pr string) (string, error) {
	var out struct {
		HeadRefOid string `json:"headRefOid"`
	}
	if err := c.json(slug, pr, "headRefOid", &out); err != nil {
		return "", err
	}
	return out.HeadRefOid, nil
}

var reviewedCommit = regexp.MustCompile(`(?i)Reviewed-Commit:\s*([0-9a-f]{7,40})`)

// SignedAt reports whether a verdict comment names this head commit.
//
// Approval binds to a commit, so a verdict at an earlier head does not count:
// the author pushed a fix and that round is stale. Prefix-matching in both
// directions handles a verdict that abbreviates the sha.
func (c Client) SignedAt(slug, pr, head string) (bool, error) {
	if head == "" {
		return false, nil
	}
	var out struct {
		Comments []struct {
			Body string `json:"body"`
		} `json:"comments"`
	}
	if err := c.json(slug, pr, "comments", &out); err != nil {
		return false, err
	}
	for _, cm := range out.Comments {
		if !strings.Contains(cm.Body, "AGENT-REVIEW:") {
			continue
		}
		m := reviewedCommit.FindStringSubmatch(cm.Body)
		if m == nil {
			continue
		}
		if strings.HasPrefix(m[1], first(head, 7)) || strings.HasPrefix(head, first(m[1], 7)) {
			return true, nil
		}
	}
	return false, nil
}

func (c Client) json(slug, pr, field string, into any) error {
	cmd := exec.Command(c.Bin, "pr", "view", pr, "--repo", slug, "--json", field)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("gh pr view %s --repo %s: %w", pr, slug, err)
	}
	return json.Unmarshal(out, into)
}

func first(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
