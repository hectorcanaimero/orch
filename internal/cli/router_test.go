package cli

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadLine(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"a full line", "y\n", "y\n", false},
		{"EOF right away — an unattended stdin", "", "", false},
		{"EOF after text with no trailing newline", "y", "y", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readLine(strings.NewReader(tc.in))
			if (err != nil) != tc.wantErr {
				t.Fatalf("readLine(%q) error = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("readLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestReadLineReportsARealReadError(t *testing.T) {
	_, err := readLine(errReader{})
	if err == nil {
		t.Fatal("readLine did not report a non-EOF read error")
	}
	if errors.Is(err, io.EOF) {
		t.Error("a real read error was reported as EOF")
	}
}
