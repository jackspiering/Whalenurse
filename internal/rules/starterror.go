package rules

import "strings"

// Cause is why a container failed to start, as read from State.Error. All
// error-string matching lives in this file because the strings vary by Engine
// version; every pattern is backed by a fixture in testdata/inspect.
type Cause string

const (
	CauseNone Cause = ""
	// Stale references to a network_mode: container:<parent> target.
	CauseParentGone    Cause = "parent-gone"    // parent recreated or removed
	CauseParentStopped Cause = "parent-stopped" // parent exists but is not running
	CauseNetworkGone   Cause = "network-gone"   // network ... not found
	// "Other" causes: start first, then recreate (plan §3.3 rule 1).
	CauseMissingMount   Cause = "missing-mount"
	CausePortAllocated  Cause = "port-allocated"
	CauseExecNotFound   Cause = "exec-not-found"
	CauseExecPermission Cause = "exec-permission"
	CauseNotADirectory  Cause = "not-a-directory"
	CauseOther          Cause = "other"
)

// IsStaleReference reports whether the cause is one of the stale-reference
// shapes, which need the parent or a recreate rather than a plain start.
func (c Cause) IsStaleReference() bool {
	return c == CauseParentGone || c == CauseParentStopped || c == CauseNetworkGone
}

// ClassifyStartError maps a non-empty State.Error to a Cause. The order
// matters: the stale-reference strings are checked first.
func ClassifyStartError(msg string) Cause {
	if msg == "" {
		return CauseNone
	}
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "joining network namespace of container") && strings.Contains(m, "no such container"):
		return CauseParentGone
	case strings.Contains(m, "cannot join network namespace of a non running container"):
		return CauseParentStopped
	case strings.Contains(m, "network") && strings.Contains(m, "not found"):
		return CauseNetworkGone
	case strings.Contains(m, "failed to fulfil mount request"),
		strings.Contains(m, "invalid mount config") && strings.Contains(m, "bind source path does not exist"):
		return CauseMissingMount
	case strings.Contains(m, "port is already allocated"), strings.Contains(m, "address already in use"):
		return CausePortAllocated
	case strings.Contains(m, "not a directory"):
		// Docker appends a misleading "mount a directory onto a file" hint here; ignore it.
		return CauseNotADirectory
	case strings.Contains(m, "permission denied") && strings.Contains(m, "exec:"):
		return CauseExecPermission
	case strings.Contains(m, "exec:") && strings.Contains(m, "no such file or directory"):
		return CauseExecNotFound
	}
	return CauseOther
}
