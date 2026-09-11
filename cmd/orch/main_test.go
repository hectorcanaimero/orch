package main

import "testing"

func TestRunExitCodes(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"version flag succeeds", []string{"--version"}, 0},
		{"status outside a project fails", []string{"status"}, 1},
		{"unknown subcommand is a usage error", []string{"bogus"}, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(tc.args); got != tc.want {
				t.Errorf("run(%v) = %d, want %d", tc.args, got, tc.want)
			}
		})
	}
}
