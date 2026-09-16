package tunnel

import "testing"

func TestExtractHost(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{"plain host", "localhost", "localhost"},
		{"host with port", "localhost:7420", "localhost"},
		{"ipv4 with port", "127.0.0.1:7420", "127.0.0.1"},
		{"bracketed ipv6 with port", "[::1]:7420", "[::1]"},
		{"bracketed ipv6 no port", "[::1]", "[::1]"},
		{"bare ipv6 no brackets", "::1", "::1"},
		{"uppercase normalized", "LOCALHOST:7420", "localhost"},
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractHost(tc.header); got != tc.want {
				t.Errorf("ExtractHost(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, h := range []string{"127.0.0.1", "localhost", "::1", "[::1]"} {
		if !IsLoopbackHost(h) {
			t.Errorf("IsLoopbackHost(%q) = false, want true", h)
		}
	}
	for _, h := range []string{"example.com", "0.0.0.0", ""} {
		if IsLoopbackHost(h) {
			t.Errorf("IsLoopbackHost(%q) = true, want false", h)
		}
	}
}

func TestEvaluateCapabilitiesGateOrder(t *testing.T) {
	cases := []struct {
		name       string
		gates      Gates
		binary     bool
		wantOK     bool
		wantReason string
	}{
		{"config disabled wins first", Gates{}, true, false, ReasonConfigDisabled},
		{
			"profile gate checked before host",
			Gates{Enabled: true, OperatorProfile: false, LoopbackHost: false},
			true, false, ReasonProfileGate,
		},
		{
			"host gate checked before binary",
			Gates{Enabled: true, OperatorProfile: true, LoopbackHost: false},
			false, false, ReasonHostGate,
		},
		{
			"binary consulted only once everything else passes",
			Gates{Enabled: true, OperatorProfile: true, LoopbackHost: true},
			false, false, ReasonBinaryMissing,
		},
		{
			"all gates pass",
			Gates{Enabled: true, OperatorProfile: true, LoopbackHost: true},
			true, true, ReasonOK,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := EvaluateCapabilities(tc.gates, tc.binary)
			if ok != tc.wantOK || reason != tc.wantReason {
				t.Errorf("EvaluateCapabilities(%+v, %v) = (%v, %q), want (%v, %q)",
					tc.gates, tc.binary, ok, reason, tc.wantOK, tc.wantReason)
			}
		})
	}
}
