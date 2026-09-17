package cli

import "golang.org/x/sys/unix"

// accessW reports whether the current process can write to a path. Checking the
// mode bits is not the same question: ownership, groups and ACLs all decide it.
type unixAccess struct{}

var unixVar = unixAccess{}

func (unixAccess) Access(p string) error { return unix.Access(p, unix.W_OK) }
