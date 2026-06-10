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
	"context"
	"os"
	"testing"
	"time"
)

// TestListenerRejectsForeignUID verifies SO_PASSCRED authentication: a
// datagram whose sender uid does not match allowedUID (and is not root) is
// dropped, so READY=1 from an unauthorized process cannot release the gate.
// The sender here is the test process (its real uid); configuring the
// listener with a different allowedUID makes that sender "foreign" without
// needing to actually run as another user.
func TestListenerRejectsForeignUID(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("root datagrams are always accepted; cannot fake a foreign uid as root")
	}
	foreign := os.Getuid() + 1 // never matches the test process's own uid
	n, err := Listen("foreign-uid", os.Getpid(), foreign)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer n.Close()

	dialAndSend(t, n.Path(), "READY=1\n")

	// The datagram must be dropped: Ready stays false past a short wait.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := n.WaitReady(ctx); err == nil {
		t.Fatal("READY=1 from a foreign uid should have been dropped, but Ready fired")
	}
	if n.Ready() {
		t.Error("Ready should remain false after a foreign-uid datagram")
	}
}

// TestListenerAcceptsOwnUID is the positive control: with allowedUID set to
// the sender's uid, READY=1 is accepted (the SO_PASSCRED path does not block
// legitimate notifications).
func TestListenerAcceptsOwnUID(t *testing.T) {
	t.Parallel()
	n, err := Listen("own-uid", os.Getpid(), os.Getuid())
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer n.Close()

	dialAndSend(t, n.Path(), "READY=1\n")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := n.WaitReady(ctx); err != nil {
		t.Fatalf("READY=1 from own uid should be accepted: %v", err)
	}
}
