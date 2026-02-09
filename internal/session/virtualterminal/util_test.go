package virtualterminal

import "testing"

func TestIsCtrlEnterSequence(t *testing.T) {
	tests := []struct {
		name string
		seq  []byte
		want bool
	}{
		{"kitty format", []byte("\x1b[13;5u"), true},
		{"xterm format", []byte("\x1b[27;5;13~"), true},
		{"shift+enter kitty (not ctrl)", []byte("\x1b[13;2u"), false},
		{"plain enter", []byte("\x1b[13u"), false},
		{"too short", []byte("\x1b["), false},
		{"wrong introducer", []byte("\x1bO13;5u"), false},
		{"empty", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCtrlEnterSequence(tt.seq); got != tt.want {
				t.Errorf("IsCtrlEnterSequence(%q) = %v, want %v", tt.seq, got, tt.want)
			}
		})
	}
}

func TestIsCtrlEscapeSequence(t *testing.T) {
	tests := []struct {
		name string
		seq  []byte
		want bool
	}{
		{"kitty format", []byte("\x1b[27;5u"), true},
		{"xterm format", []byte("\x1b[27;5;27~"), true},
		{"plain escape", []byte("\x1b[27u"), false},
		{"shift+escape", []byte("\x1b[27;2u"), false},
		{"too short", []byte("\x1b["), false},
		{"wrong introducer", []byte("\x1bO27;5u"), false},
		{"empty", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCtrlEscapeSequence(tt.seq); got != tt.want {
				t.Errorf("IsCtrlEscapeSequence(%q) = %v, want %v", tt.seq, got, tt.want)
			}
		})
	}
}

func TestIsShiftEnterSequence(t *testing.T) {
	tests := []struct {
		name string
		seq  []byte
		want bool
	}{
		{"kitty format", []byte("\x1b[13;2u"), true},
		{"xterm format", []byte("\x1b[27;2;13~"), true},
		{"ctrl+enter (not shift)", []byte("\x1b[13;5u"), false},
		{"plain enter", []byte("\x1b[13u"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsShiftEnterSequence(tt.seq); got != tt.want {
				t.Errorf("IsShiftEnterSequence(%q) = %v, want %v", tt.seq, got, tt.want)
			}
		})
	}
}
