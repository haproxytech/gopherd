package service

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// ResolveCredential resolves user/group names and IDs to a syscall.Credential.
// Returns nil if no user or group information is specified.
// Numeric IDs take precedence over names. If a user is specified without a group,
// the user's primary group is used. If only a group is specified, the current
// process UID is preserved.
func ResolveCredential(userName, groupName string, userID, groupID *int) (*syscall.Credential, error) {
	var uid, gid uint32
	var hasUser, hasGroup bool

	if userID != nil {
		uid = uint32(*userID)
		hasUser = true
	} else if userName != "" {
		u, err := user.Lookup(userName)
		if err != nil {
			return nil, fmt.Errorf("lookup user %q: %w", userName, err)
		}
		id, err := strconv.ParseUint(u.Uid, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("parse uid for %q: %w", userName, err)
		}
		uid = uint32(id)
		hasUser = true
		// Use user's primary group as default
		if groupID == nil && groupName == "" {
			id, err := strconv.ParseUint(u.Gid, 10, 32)
			if err != nil {
				return nil, fmt.Errorf("parse gid for %q: %w", userName, err)
			}
			gid = uint32(id)
			hasGroup = true
		}
	}

	if groupID != nil {
		gid = uint32(*groupID)
		hasGroup = true
	} else if groupName != "" {
		g, err := user.LookupGroup(groupName)
		if err != nil {
			return nil, fmt.Errorf("lookup group %q: %w", groupName, err)
		}
		id, err := strconv.ParseUint(g.Gid, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("parse gid for group %q: %w", groupName, err)
		}
		gid = uint32(id)
		hasGroup = true
	}

	if !hasUser && !hasGroup {
		return nil, nil
	}

	// If only group is specified, preserve current process UID
	if !hasUser && hasGroup {
		uid = uint32(os.Getuid())
	}

	return &syscall.Credential{Uid: uid, Gid: gid}, nil
}
