package stack

import (
	"strings"
	"testing"
)

func TestSelectMode_V01_V04(t *testing.T) {
	tests := []struct {
		version  string
		explicit Mode
		want     Mode
		wantErr  bool
	}{
		{"18.11.7-ee", "", ModeLegacy, false},
		{"19.0.99", "", ModeLegacy, false},
		{"19.1.0-ee", "", ModeNative, false},
		{"20.0.0", "", ModeNative, false},
		{"", ModeLegacy, ModeLegacy, false},
		{"", ModeNative, ModeNative, false},
		{"", "", "", true},
		{"18.11.0", ModeNative, "", true},
		{"19.1.0", ModeLegacy, "", true},
		{"nonsense", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.version+"/"+string(tt.explicit), func(t *testing.T) {
			got, err := SelectMode(tt.version, tt.explicit)
			if got != tt.want || (err != nil) != tt.wantErr {
				t.Fatalf("got (%q, %v), want (%q, err=%v)", got, err, tt.want, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), string(FindingInvalidArguments)) {
				t.Fatalf("error is not typed invalid arguments: %v", err)
			}
		})
	}
}

func FuzzSelectModeNeverPanics(f *testing.F) {
	f.Add("19.1.0-ee")
	f.Add("999999999999999999999999999999.1")
	f.Fuzz(func(t *testing.T, version string) {
		mode, err := SelectMode(version, "")
		if err == nil && mode != ModeLegacy && mode != ModeNative {
			t.Fatalf("unexpected successful mode %q", mode)
		}
	})
}
