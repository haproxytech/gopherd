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

// ResolveCredential resolves user/group names and IDs to a syscall.Credential.
// Returns nil if no user or group information is specified.
// Numeric IDs take precedence over names. If a user is specified without a
// group (by name or by id), the user's primary group from /etc/passwd is
// used — the numeric-id form requires the uid to exist in passwd, otherwise
// an error is returned and the operator must set group-id explicitly. If only
// a group is specified, the current process UID is preserved. When resolving
// by username or by numeric uid with a passwd entry, supplementary groups are
// populated automatically.
//
// strictGroups drops the user's auto-inherited supplementary groups (docker,
// wheel, ...) when an explicit group is also set, for least privilege.
func ResolveCredential(userName, groupName string, userID, groupID *int, strictGroups bool) (*syscall.Credential, error) {
	// Bound numeric IDs to the kernel's 32-bit uid_t/gid_t before the uint32
	// conversions below. Both edges silently misdrop privilege: -1 becomes
	// (uid_t)-1 = "don't change", and multiples of 2^32 truncate to 0 (root) —
	// so a config that looks like a privilege drop keeps an unintended UID.
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
		// Mirror the name-form branch below: if the operator supplied only
		// user-id (no group-id, no group name), look up the uid in
		// /etc/passwd to derive the primary gid and supplementary groups.
		// Without this lookup we would fall through to os.Getgid() — which
		// is gopherd's gid, typically 0 (root) for a PID 1 container — and
		// silently leave the child in the root group.
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
		// Use user's primary group as default
		if groupID == nil && groupName == "" {
			id, err := strconv.ParseUint(u.Gid, 10, 32)
			if err != nil {
				return nil, fmt.Errorf("parse gid for %q: %w", userName, err)
			}
			gid = uint32(id)
			hasGroup = true
		}
		// Resolve supplementary groups.
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

	// If only group is specified, preserve current process UID.
	// Explicitly set Groups to just the target GID so the child does not
	// inherit the parent's supplementary groups (which may include root).
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
