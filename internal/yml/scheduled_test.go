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

package yml

import (
	"strings"
	"testing"
)

func TestScheduledStartupAccepted(t *testing.T) {
	t.Parallel()
	cfg, err := Unmarshal([]byte(`
processes:
  - name: backup
    command: /bin/backup
    startup: scheduled
    schedule: "0 3 * * *"
`))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if cfg.Processes[0].Startup != "scheduled" {
		t.Errorf("Startup = %q, want scheduled", cfg.Processes[0].Startup)
	}
	if cfg.Processes[0].Schedule != "0 3 * * *" {
		t.Errorf("Schedule = %q, want %q", cfg.Processes[0].Schedule, "0 3 * * *")
	}
}

func TestScheduleWithDisabledStartupAccepted(t *testing.T) {
	t.Parallel()
	cfg, err := Unmarshal([]byte(`
processes:
  - name: backup
    command: /bin/backup
    startup: disabled
    schedule: "0 3 * * *"
`))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if cfg.Processes[0].Startup != "disabled" {
		t.Errorf("Startup = %q, want disabled", cfg.Processes[0].Startup)
	}
	if cfg.Processes[0].Schedule != "0 3 * * *" {
		t.Errorf("Schedule = %q, want %q", cfg.Processes[0].Schedule, "0 3 * * *")
	}
}

func TestScheduledEnvGate(t *testing.T) {
	yaml := []byte(`
processes:
  - name: backup
    command: /bin/backup
    startup: "{{.ENABLE_BACKUP:-disabled}}"
    schedule: "0 3 * * *"
`)
	t.Run("off by default", func(t *testing.T) {
		cfg, err := Unmarshal(yaml)
		if err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if cfg.Processes[0].Startup != "disabled" {
			t.Errorf("Startup = %q, want disabled", cfg.Processes[0].Startup)
		}
	})
	t.Run("flipped on via env", func(t *testing.T) {
		withEnv(t, map[string]string{"ENABLE_BACKUP": "scheduled"})
		cfg, err := Unmarshal(yaml)
		if err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if cfg.Processes[0].Startup != "scheduled" {
			t.Errorf("Startup = %q, want scheduled", cfg.Processes[0].Startup)
		}
	})
}

func TestScheduledConfigErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "missing schedule",
			yaml: `
processes:
  - name: backup
    command: /bin/backup
    startup: scheduled
`,
			wantErr: "schedule is required",
		},
		{
			name: "invalid cron expression",
			yaml: `
processes:
  - name: backup
    command: /bin/backup
    startup: scheduled
    schedule: "61 * * * *"
`,
			wantErr: "schedule",
		},
		{
			name: "schedule on non-scheduled process",
			yaml: `
processes:
  - name: web
    command: /bin/web
    schedule: "0 3 * * *"
`,
			wantErr: "schedule",
		},
		{
			name: "schedule on oneshot process",
			yaml: `
processes:
  - name: backup
    command: /bin/backup
    startup: oneshot
    schedule: "0 3 * * *"
`,
			wantErr: "schedule",
		},
		{
			name: "disabled with invalid cron expression",
			yaml: `
processes:
  - name: backup
    command: /bin/backup
    startup: disabled
    schedule: "61 * * * *"
`,
			wantErr: "schedule",
		},
		{
			name: "disabled with schedule and on-success",
			yaml: `
processes:
  - name: backup
    command: /bin/backup
    startup: disabled
    schedule: "0 3 * * *"
    on-success: restart
`,
			wantErr: "on-success",
		},
		{
			name: "disabled with schedule and ordering fields",
			yaml: `
processes:
  - name: web
    command: /bin/web
  - name: backup
    command: /bin/backup
    startup: disabled
    schedule: "0 3 * * *"
    after: [web]
`,
			wantErr: "scheduled",
		},
		{
			name: "other process depends on disabled scheduled service",
			yaml: `
processes:
  - name: backup
    command: /bin/backup
    startup: disabled
    schedule: "0 3 * * *"
  - name: web
    command: /bin/web
    after: [backup]
`,
			wantErr: "scheduled",
		},
		{
			name: "on-success rejected",
			yaml: `
processes:
  - name: backup
    command: /bin/backup
    startup: scheduled
    schedule: "0 3 * * *"
    on-success: restart
`,
			wantErr: "on-success",
		},
		{
			name: "on-failure rejected",
			yaml: `
processes:
  - name: backup
    command: /bin/backup
    startup: scheduled
    schedule: "0 3 * * *"
    on-failure: shutdown
`,
			wantErr: "on-failure",
		},
		{
			name: "backoff rejected",
			yaml: `
processes:
  - name: backup
    command: /bin/backup
    startup: scheduled
    schedule: "0 3 * * *"
    backoff-delay: 1s
`,
			wantErr: "backoff",
		},
		{
			name: "invalid startup-timeout rejected",
			yaml: `
processes:
  - name: backup
    command: /bin/backup
    startup: scheduled
    schedule: "0 3 * * *"
    startup-timeout: soon
`,
			wantErr: "startup-timeout",
		},
		{
			name: "other process depends on scheduled via after",
			yaml: `
processes:
  - name: backup
    command: /bin/backup
    startup: scheduled
    schedule: "0 3 * * *"
  - name: web
    command: /bin/web
    after: [backup]
`,
			wantErr: "scheduled",
		},
		{
			name: "other process requires scheduled",
			yaml: `
processes:
  - name: backup
    command: /bin/backup
    startup: scheduled
    schedule: "0 3 * * *"
  - name: web
    command: /bin/web
    requires: [backup]
`,
			wantErr: "scheduled",
		},
		{
			name: "scheduled process with ordering fields",
			yaml: `
processes:
  - name: web
    command: /bin/web
  - name: backup
    command: /bin/backup
    startup: scheduled
    schedule: "0 3 * * *"
    after: [web]
`,
			wantErr: "scheduled",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Unmarshal([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestScheduledStartupFromTemplate(t *testing.T) {
	withEnv(t, map[string]string{"START_BACKUP": "scheduled"})
	cfg, err := Unmarshal([]byte(`
processes:
  - name: backup
    command: /bin/backup
    startup: "{{.START_BACKUP}}"
    schedule: "*/5 * * * *"
`))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if cfg.Processes[0].Startup != "scheduled" {
		t.Errorf("Startup = %q, want scheduled", cfg.Processes[0].Startup)
	}
}
