package cmd

import "testing"

// The platform writes "ready", not "running", for a healthy app or database.
// --wait must stop there, or it waits on a healthy resource until the timeout.
func TestTerminalStatus(t *testing.T) {
	cases := map[string][2]bool{
		"ready":         {true, false},
		"running":       {true, false},
		"stopped":       {true, false},
		"suspended":     {true, false},
		"failed":        {true, true},
		"delete_failed": {true, true},
		"deploying":     {false, false},
		"provisioning":  {false, false},
		"starting":      {false, false},
		"deleting":      {false, false},
		"":              {false, false},
	}
	for status, want := range cases {
		done, failed := terminalStatus(status)
		if done != want[0] || failed != want[1] {
			t.Errorf("terminalStatus(%q) = (%v,%v), want (%v,%v)", status, done, failed, want[0], want[1])
		}
	}
}
