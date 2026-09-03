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

package control

import (
	"fmt"
	"time"
)

// DefaultWaitTimeout bounds a `--wait` when no `--timeout` is given.
const DefaultWaitTimeout = 60 * time.Second

// MaxWaitTimeout caps `--timeout`; a wait holds a connection slot and must
// end before clientReadIdleTimeout.
const MaxWaitTimeout = 5 * time.Minute

// ActionOptions carries the `--wait` / `--timeout` flags from the CLI to the daemon.
type ActionOptions struct {
	// Timeout bounds the wait; zero means DefaultWaitTimeout.
	Timeout time.Duration
	// Wait blocks the reply until the action has taken effect.
	Wait bool
}

// wire renders the options as trailing command tokens.
func (o ActionOptions) wire() string {
	if !o.Wait {
		return ""
	}
	if o.Timeout > 0 {
		return "--wait --timeout " + o.Timeout.String()
	}
	return "--wait"
}

// extractWaitFlags strips `--wait` / `--timeout <dur>` from args, returning the positional rest.
func extractWaitFlags(args []string) (positional []string, opts ActionOptions, err error) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--wait":
			opts.Wait = true
		case "--timeout":
			if i+1 >= len(args) {
				return nil, opts, fmt.Errorf("--timeout requires a duration (e.g. 15s)")
			}
			d, perr := time.ParseDuration(args[i+1])
			if perr != nil {
				return nil, opts, fmt.Errorf("invalid --timeout %q: %w", args[i+1], perr)
			}
			if d <= 0 {
				return nil, opts, fmt.Errorf("--timeout must be positive, got %q", args[i+1])
			}
			if d > MaxWaitTimeout {
				return nil, opts, fmt.Errorf("--timeout must not exceed %s, got %q", MaxWaitTimeout, args[i+1])
			}
			opts.Timeout = d
			i++
		default:
			positional = append(positional, args[i])
		}
	}
	if opts.Timeout > 0 && !opts.Wait {
		return nil, opts, fmt.Errorf("--timeout requires --wait")
	}
	return positional, opts, nil
}
