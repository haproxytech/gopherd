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

//go:build !linux

package sdnotify

import "net"

// credEnabled is false off Linux: SO_PASSCRED / SCM_CREDENTIALS are
// unavailable, so the listener cannot verify senders and accepts any datagram.
const credEnabled = false

// oobSize is zero off Linux — ReadMsgUnix is never asked for ancillary data.
var oobSize = 0

func enablePassCred(_ *net.UnixConn) error { return nil }

func parseSenderUID(_ []byte) (uid int, ok bool) { return 0, false }
