package spawn

import (
	"crypto/rand"
	"math/big"
)

// tagAlphabet omits 0/O/1/I/l on purpose: these get read aloud, typed from a
// phone screen, and grepped.
const tagAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// MintTag returns a short handle not already in use.
//
// A session list shows a name and nothing else, so `reviewer-permitguv-79497`
// cannot be paired with the worker whose pull request it is reading. A tag both
// ends carry fixes that at a glance: `permitguv-k3n8` and `rev-k3n8-437`.
func MintTag(existing map[string]bool) string {
	for _, width := range []int{4, 4, 6} {
		for i := 0; i < 200; i++ {
			t := randTag(width)
			if !existing[t] {
				return t
			}
		}
	}
	return randTag(8)
}

func randTag(n int) string {
	b := make([]byte, n)
	for i := range b {
		// crypto/rand rather than math/rand: tags are collision-checked, but a
		// predictable sequence across concurrent spawns makes the check work
		// harder for no reason.
		k, err := rand.Int(rand.Reader, big.NewInt(int64(len(tagAlphabet))))
		if err != nil {
			k = big.NewInt(int64(i % len(tagAlphabet)))
		}
		b[i] = tagAlphabet[k.Int64()]
	}
	return string(b)
}
