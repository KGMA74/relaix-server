package sms

import (
	"strings"
	"testing"
)

func TestCount(t *testing.T) {
	tests := []struct {
		name string
		body string
		want Segments
	}{
		{"empty", "", Segments{GSM7, 0, 0, 160}},
		{"short", "Votre code est 482913", Segments{GSM7, 21, 1, 139}},
		{"one full part", strings.Repeat("a", 160), Segments{GSM7, 160, 1, 0}},
		{"two parts", strings.Repeat("a", 161), Segments{GSM7, 161, 2, 145}},
		{"extended costs two", "€", Segments{GSM7, 2, 1, 158}},
		{"accents in the table stay GSM-7", "éèàù", Segments{GSM7, 4, 1, 156}},
		{"one foreign char switches", "café ç", Segments{UCS2, 6, 1, 64}},
		{"ucs2 two parts", strings.Repeat("ç", 71), Segments{UCS2, 71, 2, 63}},
		{"emoji is two units", "🙂", Segments{UCS2, 2, 1, 68}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Count(tt.body); got != tt.want {
				t.Errorf("Count(%q) = %+v, want %+v", tt.body, got, tt.want)
			}
		})
	}
}
