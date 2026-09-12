package report

import "strings"

// The core PDF fonts (Helvetica, Times, Courier) are single-byte: a string
// handed to fpdf is written as CP1252, not as UTF-8. Go strings are UTF-8, so
// every accent in a Spanish executive summary — and every em dash in an
// English one — reaches the page as mojibake unless it is transcoded first.
//
// This is the kind of mistake that survives a byte-level test written from the
// code: the PDF is valid, the text is there, and it reads "Anlisis" on screen.
// The test asserts the ENCODED bytes for exactly that reason.
//
// fpdf ships a translator for this, but it reads a `.map` file from a font
// directory at runtime — a file we would have to ship and locate. For the
// range this document uses, the table is small enough to be obvious.

// cp1252Specials are the characters whose CP1252 byte is NOT their Unicode
// code point. Everything else below U+0100 maps one to one, which covers
// every accented letter Spanish, Portuguese and English need.
var cp1252Specials = map[rune]byte{
	'€': 0x80, // €
	'‚': 0x82, // ‚
	'ƒ': 0x83, // ƒ
	'„': 0x84, // „
	'…': 0x85, // …
	'†': 0x86, // †
	'‡': 0x87, // ‡
	'ˆ': 0x88, // ˆ
	'‰': 0x89, // ‰
	'Š': 0x8A, // Š
	'‹': 0x8B, // ‹
	'Œ': 0x8C, // Œ
	'Ž': 0x8E, // Ž
	'‘': 0x91, // '
	'’': 0x92, // '
	'“': 0x93, // "
	'”': 0x94, // "
	'•': 0x95, // •
	'–': 0x96, // –
	'—': 0x97, // —
	'˜': 0x98, // ˜
	'™': 0x99, // ™
	'š': 0x9A, // š
	'›': 0x9B, // ›
	'œ': 0x9C, // œ
	'ž': 0x9E, // ž
	'Ÿ': 0x9F, // Ÿ
}

// latin1 transcodes a UTF-8 string to CP1252 for the core fonts.
//
// A rune with no CP1252 spelling becomes "?" rather than being dropped: a
// missing character leaves a sentence that reads as if it were written that
// way, and a "?" tells the reader something was lost. Nothing in this
// document's vocabulary should reach that branch — it is there for a project
// name somebody wrote in Cyrillic.
func latin1(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r < 0x80:
			// ASCII, and the conversion is checked by the case itself.
			b.WriteByte(byte(r)) // #nosec G115 -- r < 0x80 on this branch
		case cp1252Specials[r] != 0:
			b.WriteByte(cp1252Specials[r])
		case r <= 0xFF:
			b.WriteByte(byte(r)) // #nosec G115 -- r <= 0xFF on this branch
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}
