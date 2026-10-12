// Package sms counts the parts a message body will be split into, by the
// GSM 03.38 rules the handset follows. It is what a message costs on the
// network, so callers can say so before it goes out.
//
// The dashboard's send form counts the same way (ui/src/lib/sms.ts); the two
// must agree.
package sms

import (
	"strings"
	"unicode/utf16"
)

// Encoding is how a body travels.
type Encoding string

const (
	GSM7 Encoding = "GSM-7"
	UCS2 Encoding = "UCS-2"
)

const (
	gsm7Basic = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?" +
		"¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà"

	// Extension table characters cost two septets: an escape, then the
	// character.
	gsm7Extended = "^{}\\[~]|€\f"
)

// Segments describes how a body is split.
type Segments struct {
	Encoding Encoding
	// Units is the length in the encoding's units: septets for GSM-7, UTF-16
	// code units for UCS-2.
	Units int
	Parts int
	// Remaining is how many units fit before the next part starts.
	Remaining int
}

// Count splits body the way the handset will.
func Count(body string) Segments {
	septets, gsm := 0, true
	for _, r := range body {
		switch {
		case strings.ContainsRune(gsm7Basic, r):
			septets++
		case strings.ContainsRune(gsm7Extended, r):
			septets += 2
		default:
			gsm = false
		}
		if !gsm {
			break
		}
	}

	// A single part holds 160 septets or 70 UCS-2 units; once concatenated,
	// each part gives up room for the reassembly header.
	enc, units, single, multi := GSM7, septets, 160, 153
	if !gsm {
		enc, units, single, multi = UCS2, len(utf16.Encode([]rune(body))), 70, 67
	}
	if units == 0 {
		return Segments{Encoding: enc, Remaining: single}
	}
	parts, capacity := 1, single
	if units > single {
		parts = (units + multi - 1) / multi
		capacity = parts * multi
	}
	return Segments{Encoding: enc, Units: units, Parts: parts, Remaining: capacity - units}
}
