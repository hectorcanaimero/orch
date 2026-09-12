package snapshot

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// Branding is the white-label block as the document carries it.
//
// It is the ONE optional field in a schema-1 snapshot, and that is a decision
// rather than a drift — see docs/SNAPSHOT-SCHEMA.md. A project without
// branding produces exactly the bytes it produced before this existed, because
// every field is `omitempty` and the struct is omitted whole when nothing is
// set. A viewer built against schema 1 ignores a key it does not know, which
// is what makes this additive rather than incompatible.
type Branding struct {
	Name string `json:"name,omitempty"`
	// Logo is always a `data:` URI, never a path. The document has to stand
	// alone: a viewer holding this JSON has no access to the operator's
	// filesystem, and a snapshot that pointed at one would render a broken
	// image on every machine but the one that made it.
	Logo        string `json:"logo,omitempty"`
	AccentColor string `json:"accent_color,omitempty"`
	Footer      string `json:"footer,omitempty"`
}

// Empty reports whether nothing was configured.
func (b Branding) Empty() bool {
	return b.Name == "" && b.Logo == "" && b.AccentColor == "" && b.Footer == ""
}

// maxLogoBytes caps the raw image before encoding.
//
// 128 KiB, because this file is embedded in a document a live viewer re-fetches
// on a timer — a 2 MB logo would be re-downloaded every refresh interval for
// as long as the page is open. It is generous for a logo and small enough that
// nobody notices the snapshot carrying it.
const maxLogoBytes = 128 * 1024

// logoTypes are the image formats every surface can render.
//
// PNG and JPEG and nothing else, and the reason is the PDF: `fpdf` draws
// raster images, so an SVG logo would appear on the web and be silently
// missing from the printed page. A white-label feature whose logo shows up on
// two surfaces out of three is worse than one that says which formats work.
var logoTypes = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
}

// ResolveLogo turns what the operator configured into a `data:` URI.
//
// Accepts a path to a local file, or a `data:` URI already (for an operator
// who would rather paste one into config.yaml than keep a file around). A path
// is read and encoded at build time — see Branding.Logo for why the document
// may not reference one.
//
// Every failure names the file and what was wrong with it. A logo that does
// not appear is invisible, so a silent skip here would send the operator
// looking at the SPA, the PDF and their browser cache before suspecting the
// path.
func ResolveLogo(pathOrURI string) (string, error) {
	s := strings.TrimSpace(pathOrURI)
	if s == "" {
		return "", nil
	}
	if strings.HasPrefix(s, "data:") {
		return validateLogoDataURI(s)
	}

	info, err := os.Stat(s)
	if err != nil {
		return "", fmt.Errorf("presentation.branding.logo: %w", err)
	}
	if info.Size() > maxLogoBytes {
		return "", fmt.Errorf(
			"presentation.branding.logo: %s is %d KiB; the limit is %d KiB "+
				"because the logo travels inside every snapshot a viewer re-fetches",
			s, info.Size()/1024, maxLogoBytes/1024)
	}

	raw, err := os.ReadFile(s) // #nosec G304 -- the operator names their own logo in config.yaml
	if err != nil {
		return "", fmt.Errorf("presentation.branding.logo: %w", err)
	}

	// The declared extension is not consulted: `http.DetectContentType` reads
	// the magic bytes, so a PNG named `.jpg` works and a text file named
	// `.png` is caught here rather than as a broken image three surfaces
	// later.
	mime := strings.SplitN(http.DetectContentType(raw), ";", 2)[0]
	if _, ok := logoTypes[mime]; !ok {
		return "", fmt.Errorf(
			"presentation.branding.logo: %s is %s; only PNG and JPEG work on "+
				"every surface (the PDF cannot draw an SVG)", s, mime)
	}

	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw), nil
}

// validateLogoDataURI checks a URI the operator pasted, to the same standard
// as a file: the same formats, the same size, and the base64 has to decode.
func validateLogoDataURI(uri string) (string, error) {
	const marker = ";base64,"
	i := strings.Index(uri, marker)
	if i < 0 {
		return "", fmt.Errorf("presentation.branding.logo: a data URI must be base64 " +
			"(`data:image/png;base64,…`)")
	}
	mime := strings.TrimPrefix(uri[:i], "data:")
	if _, ok := logoTypes[mime]; !ok {
		return "", fmt.Errorf(
			"presentation.branding.logo: the data URI declares %s; only PNG and "+
				"JPEG work on every surface (the PDF cannot draw an SVG)", mime)
	}

	raw, err := base64.StdEncoding.DecodeString(uri[i+len(marker):])
	if err != nil {
		return "", fmt.Errorf("presentation.branding.logo: the data URI's base64 does "+
			"not decode: %w", err)
	}
	if len(raw) > maxLogoBytes {
		return "", fmt.Errorf(
			"presentation.branding.logo: the data URI carries %d KiB; the limit is %d KiB",
			len(raw)/1024, maxLogoBytes/1024)
	}
	// What it SAYS it is has to be what it IS: a data URI declaring PNG and
	// carrying something else renders as a broken image, and the operator
	// pasted it from somewhere they trusted.
	if got := strings.SplitN(http.DetectContentType(raw), ";", 2)[0]; got != mime {
		return "", fmt.Errorf("presentation.branding.logo: the data URI declares %s "+
			"but carries %s", mime, got)
	}
	return uri, nil
}

// DecodeLogo turns the document's data URI back into bytes and a format name,
// for a renderer that draws rather than embeds — the PDF.
//
// Returns ok=false for an empty logo, which is the common case, so a caller
// writes one branch rather than two.
func DecodeLogo(dataURI string) (raw []byte, format string, ok bool) {
	const marker = ";base64,"
	i := strings.Index(dataURI, marker)
	if i < 0 {
		return nil, "", false
	}
	format, known := logoTypes[strings.TrimPrefix(dataURI[:i], "data:")]
	if !known {
		return nil, "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(dataURI[i+len(marker):])
	if err != nil {
		return nil, "", false
	}
	return decoded, format, true
}
