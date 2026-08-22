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
	"testing"
	"time"
)

func TestNewScheduled(t *testing.T) {
	t.Parallel()
	svc := mustNew(t, Process{Command: "true", Startup: "scheduled", Schedule: "0 3 * * *"}, "")
	if !svc.Scheduled {
		t.Error("expected scheduled")
	}
	if svc.Oneshot {
		t.Error("scheduled must not be oneshot (it does not run in startup layers)")
	}
	if !svc.Enabled {
		t.Error("scheduled should be enabled")
	}
	if svc.Schedule == nil {
		t.Fatal("expected parsed schedule")
	}
	// The parsed schedule must actually be the configured one.
	after := time.Date(2026, 8, 22, 1, 0, 0, 0, time.UTC)
	want := time.Date(2026, 8, 22, 3, 0, 0, 0, time.UTC)
	if got := svc.Schedule.Next(after); !got.Equal(want) {
		t.Errorf("Schedule.Next = %v, want %v", got, want)
	}
}

func TestNewScheduledInvalidExpression(t *testing.T) {
	t.Parallel()
	// yml validates earlier, but New must reject too so a caller bypassing yml
	// cannot create a scheduled service with a nil schedule.
	if _, err := New(Process{Command: "true", Startup: "scheduled", Schedule: "61 * * * *"}, ""); err == nil {
		t.Error("expected error for invalid cron expression")
	}
}

func TestNewNotScheduledByDefault(t *testing.T) {
	t.Parallel()
	svc := mustNew(t, Process{Command: "true"}, "")
	if svc.Scheduled || svc.Schedule != nil {
		t.Error("non-scheduled service must have Scheduled=false and nil Schedule")
	}
}
