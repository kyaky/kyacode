package main

// The accept-side of the install-on-miss confirmation, pinned at its own
// io.Reader seam (second-instance review, ticket 5). The spawn-level
// integration pins in main_test.go drive the harness end to end with
// "y" and the cancelled default with EOF — but before this seam existed,
// deleting "yes" or the case fold from confirmYes red NOTHING. The
// consume-before-exec ordering (answer read, then installer, then child)
// stays the spawn pins' job; the answer taxonomy is now this table's.

import (
	"bytes"
	"testing"
)

func TestConfirmYesAnswerTable(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{"y", "y\n", true},
		{"Y", "Y\n", true},
		{"yes", "yes\n", true},
		{"YES upper", "YES\n", true},
		{"Yes mixed", "Yes\n", true},
		{"padded y", "  y  \n", true},
		{"padded yes", "\tyes ", true}, // no trailing newline at all
		{"CRLF from a Windows pipe", "y\r\n", true},
		{"the default: no", "n\n", false},
		{"empty read at EOF", "", false},
		{"bare newline", "\n", false},
		{"garbage", "maybe\n", false},
		{"near-miss prefix", "yy\n", false},
		{"near-miss word", "yeah\n", false},
		{"prompt echo is not an answer", "y/n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := confirmYes(bytes.NewReader([]byte(tc.in))); got != tc.want {
				t.Errorf("confirmYes(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
