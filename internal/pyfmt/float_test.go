package pyfmt

import (
	"math"
	"strconv"
	"testing"
)

// The expected column is what CPython actually printed, captured with:
//
//	/tmp/orch-py/bin/python - <<'PY' > internal/state/pyfloat_test.go
//	  ... (the generator in this file's git history)
//
// Values are given as raw IEEE-754 bits so the test input cannot drift
// through a Go literal that parses to a neighbouring double.
func TestFloatMatchesPythonRepr(t *testing.T) {
	cases := []struct {
		bits uint64
		want string
	}{
		{0x0000000000000000, "0.0"},
		{0x8000000000000000, "-0.0"},
		{0x3ff0000000000000, "1.0"},
		{0xbff0000000000000, "-1.0"},
		{0x3fdae147ae147ae1, "0.42"},
		{0x3ff8000000000000, "1.5"},
		{0x40b5180000000000, "5400.0"},
		{0x4059000000000000, "100.0"},
		{0x4008000000000000, "3.0"},
		{0x3fb999999999999a, "0.1"},
		{0x3fc999999999999a, "0.2"},
		{0x3fd3333333333334, "0.30000000000000004"},
		{0x3fd5555555555555, "0.3333333333333333"},
		{0x3fe5555555555555, "0.6666666666666666"},
		{0x3ee4f8b588e368f1, "1e-05"},
		{0x3f1a36e2eb1c432d, "0.0001"},
		{0x3f50624dd2f1a9fc, "0.001"},
		{0x3f202e4b6ce5dc68, "0.00012345"},
		{0x430c6bf526340000, "1000000000000000.0"},
		{0x4341c37937e08000, "1e+16"},
		{0x4376345785d8a000, "1e+17"},
		{0x444b1ae4d6e2ef50, "1e+21"},
		{0x4480f0cf064dd592, "1e+22"},
		{0x4340000000000000, "9007199254740992.0"},
		{0x4345ee2a2eb5a5c4, "1.2345678901234568e+16"},
		{0x7fefffffffffffff, "1.7976931348623157e+308"},
		{0x0000000000000001, "5e-324"},
		{0x0010000000000000, "2.2250738585072014e-308"},
		{0x40fe240c9fbe76c9, "123456.789"},
		{0xc0fe240c9fbe76c9, "-123456.789"},
		{0x3e7ad7f29abcaf48, "1e-07"},
		{0x3e8421f5f40d8376, "1.5e-07"},
		{0x43118b54f22aeb00, "1234567890123456.0"},
		{0x54b249ad2594c37d, "1e+100"},
		{0xd4b249ad2594c37d, "-1e+100"},
		{0x400921fb54442d11, "3.14159265358979"},
		{0x3fe0000000000000, "0.5"},
		{0x4004000000000000, "2.5"},
		{0x412e848000000000, "1000000.0"},
		{0x4376345785d8a000, "1e+17"},
	}
	for _, c := range cases {
		v := math.Float64frombits(c.bits)
		if got := Float(v); got != c.want {
			t.Errorf("Float(%#016x) = %q, Python says %q", c.bits, got, c.want)
		}
	}
}

func TestFloatRoundTrips(t *testing.T) {
	for _, v := range []float64{0.42, 1.5, 5400.0, 0.1 + 0.2, 1e-7, 1e21, 123456.789} {
		s := Float(v)
		back, err := strconvParseFloat(s)
		if err != nil {
			t.Fatalf("Float(%v) = %q which does not parse: %v", v, s, err)
		}
		if back != v {
			t.Errorf("Float(%v) = %q round-trips to %v", v, s, back)
		}
	}
}

func strconvParseFloat(s string) (float64, error) {
	return strconv.ParseFloat(s, 64)
}
