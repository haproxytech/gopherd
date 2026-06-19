// Copyright 2026 HAProxy Technologies LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"fmt"
	"math"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// ResolveCredential resolves user/group names and IDs to a syscall.Credential,
// or nil if neither is specified. Numeric IDs take precedence over names. A user
// without an explicit group inherits its primary group and supplementary groups
// from /etc/passwd; the numeric-uid form requires a passwd entry (else it errors
// so the operator sets group-id explicitly). A group-only spec preserves the
// current process UID.
//
// strictGroups drops auto-inherited supplementary groups (docker, wheel, ...)
// when an explicit group is also set, for least privilege.
func ResolveCredential(userName, groupName string, userID, groupID *int, strictGroups bool) (*syscall.Credential, error) {
	// Bound numeric IDs before the uint32 conversions below: both edges silently
	// misdrop privilege. -1 becomes (uid_t)-1 = "don't change", and multiples of
	// 2^32 truncate to 0 (root), so an apparent privilege drop keeps root.
	if err := validateNumericID("user-id", userID); err != nil {
		return nil, err
	}
	if err := validateNumericID("group-id", groupID); err != nil {
		return nil, err
	}

	var uid, gid uint32
	var groups []uint32
	var hasUser, hasGroup bool

	if userID != nil {
		uid = uint32(*userID)
		hasUser = true
		// With only user-id given, look up the uid in /etc/passwd for its
		// primary gid and supplementary groups. Otherwise gid would default to
		// os.Getgid() (typically root in a PID 1 container), leaving the child
		// in the root group.
		if groupID == nil && groupName == "" {
			u, err := user.LookupId(strconv.Itoa(*userID))
			if err != nil {
				return nil, fmt.Errorf("user-id %d not found in /etc/passwd; specify group-id explicitly: %w", *userID, err)
			}
			id, err := strconv.ParseUint(u.Gid, 10, 32)
			if err != nil {
				return nil, fmt.Errorf("parse gid for uid %d: %w", *userID, err)
			}
			gid = uint32(id)
			hasGroup = true
			if groupIDs, gerr := u.GroupIds(); gerr == nil {
				for _, gidStr := range groupIDs {
					g, perr := strconv.ParseUint(gidStr, 10, 32)
					if perr == nil {
						groups = append(groups, uint32(g))
					}
				}
			}
		}
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
		if groupID == nil && groupName == "" {
			id, err := strconv.ParseUint(u.Gid, 10, 32)
			if err != nil {
				return nil, fmt.Errorf("parse gid for %q: %w", userName, err)
			}
			gid = uint32(id)
			hasGroup = true
		}
		groupIDs, err := u.GroupIds()
		if err == nil {
			for _, gidStr := range groupIDs {
				g, err := strconv.ParseUint(gidStr, 10, 32)
				if err == nil {
					groups = append(groups, uint32(g))
				}
			}
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

	// Group-only: keep the current UID, but set Groups to just the target GID so
	// the child does not inherit the parent's supplementary groups (maybe root).
	if !hasUser && hasGroup {
		uid = uint32(os.Getuid())
		groups = []uint32{gid}
	}

	// With an explicit group, strict mode keeps only that primary group.
	explicitGroup := groupID != nil || groupName != ""
	if strictGroups && hasUser && explicitGroup {
		groups = []uint32{gid}
	}

	return &syscall.Credential{Uid: uid, Gid: gid, Groups: groups}, nil
}

// validateNumericID rejects a uid/gid outside [0, MaxUint32] (nil = unset, ok).
// See ResolveCredential for why both bounds matter. int64 widening keeps the
// upper-bound compare valid on 32-bit platforms (where it cannot trigger).
func validateNumericID(field string, id *int) error {
	if id == nil {
		return nil
	}
	if *id < 0 || int64(*id) > math.MaxUint32 {
		return fmt.Errorf("%s must be in range [0, %d], got %d", field, int64(math.MaxUint32), *id)
	}

	return nil
}
