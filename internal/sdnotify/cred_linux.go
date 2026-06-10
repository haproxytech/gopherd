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

package sdnotify

import (
	"net"
	"syscall"
)

// credEnabled reports whether the kernel will attach SCM_CREDENTIALS to
// received datagrams on this platform. Linux: yes.
const credEnabled = true

// oobSize is the ancillary-data buffer size needed to receive one
// SCM_CREDENTIALS control message.
var oobSize = syscall.CmsgSpace(syscall.SizeofUcred)

// enablePassCred turns on SO_PASSCRED so datagrams carry sender credentials.
func enablePassCred(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var sockErr error
	if cerr := raw.Control(func(fd uintptr) {
		sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_PASSCRED, 1)
	}); cerr != nil {
		return cerr
	}
	return sockErr
}

// parseSenderUID extracts the sender uid from ReadMsgUnix ancillary data.
// ok is false when no SCM_CREDENTIALS message is present (treat as untrusted).
func parseSenderUID(oob []byte) (uid int, ok bool) {
	scms, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return 0, false
	}
	for i := range scms {
		if scms[i].Header.Level != syscall.SOL_SOCKET || scms[i].Header.Type != syscall.SCM_CREDENTIALS {
			continue
		}
		cred, err := syscall.ParseUnixCredentials(&scms[i])
		if err != nil {
			return 0, false
		}
		return int(cred.Uid), true
	}
	return 0, false
}
