// Package ogimg renders Open Graph / Twitter Card preview images for
// bzlhub pages. Pure stdlib + golang.org/x/image (BSD-licensed Go
// fonts; no CGO; no external services). Output is a 1200×630 PNG —
// the size every OG/Twitter unfurl crawler expects for
// `twitter:card: summary_large_image`.
//
// Spec captures everything the renderer needs; Render is deterministic
// given the same Spec. Callers cache the output keyed off the Spec
// fields so a hermeticity-class change or new version invalidates the
// cached PNG without manual purge.
//
// Plan reference: docs/plans/32-og-image-generator.md.
package ogimg

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"strings"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Image dimensions match the Open Graph + Twitter Card "summary_large_image"
// spec (1200×630). Don't change these without also updating
// internal/server/headtags/headtags.go which emits the corresponding
// og:image:width / og:image:height meta tags.
const (
	imageW = 1200
	imageH = 630

	bottomBandH = 50 // height of the lower band that holds counts + host

	// Font sizes (pt at 72 DPI; pt==px here).
	fontModuleNameSize  = 96
	fontHermeticitySize = 32
	fontWordmarkSize    = 22
	fontFooterSize      = 14

	// Vertical baselines for each text block. Tweaking these is the
	// fastest way to nudge layout without rewriting layout maths.
	yWordmark    = 50
	yModuleName  = 280
	yHermeticity = 360
	yFooter      = imageH - 18

	// Side padding for left/right-aligned text.
	sidePadding = 40
)

// Spec is the renderable description of one OG card. Fields are
// stable contracts callers depend on for cache-key composition;
// renaming or removing a field is a breaking change for the cache.
type Spec struct {
	ModuleName    string // e.g. "rules_go" — empty for the generic fallback
	ModuleVersion string // e.g. "0.50.1" — empty when the page isn't version-scoped
	Hermeticity   string // one of the 7 classes (see hermeticityColor); empty if unknown
	RuleCount     int    // 0 = skip rendering
	DepCount      int    // 0 = skip rendering
	Versions      int    // 0 = skip rendering
	Host          string // host part rendered bottom-right, e.g. "bzlhub.com"

	// GeneratedAt is excluded from rendering but included in Spec so
	// callers that key their cache off Spec can mix it in only when
	// they want time-based invalidation. The renderer ignores it.
	GeneratedAt time.Time
}

// Palette mirrors the UI's --color-* tokens used elsewhere in canopy.
// Hex values are approximate matches for the oklch() definitions in
// ui/src/app.css; keeping them as constants here means rendering
// works without parsing CSS. When Plan 19 Idea I (semantic colour-
// token pass) lands, revisit these to keep cross-surface alignment.
var (
	colorBG     = parseHex("0a0e14") // dark navy, matches favicon
	colorBGElev = parseHex("141a23") // subtly lighter for the bottom band
	colorAccent = parseHex("7dd3fc") // cyan, matches favicon
	colorFG     = parseHex("e6edf3") // primary text
	colorFGMute = parseHex("8b9aab") // secondary text
)

// Parse the embedded Go fonts once at package init — TTF parsing is
// the heavy part of font rendering (~50-100ms per parse). Faces
// (which carry per-size glyph caches) are created cheaply per render
// in newFace, which is safe under concurrency because each render
// owns its own faces.
//
// Failing to parse means the binary's embedded font data is corrupt,
// which is unrecoverable; panic at init() rather than degrade
// silently at request time.
var (
	parsedBoldFont    *opentype.Font
	parsedRegularFont *opentype.Font
)

func init() {
	var err error
	if parsedBoldFont, err = opentype.Parse(gobold.TTF); err != nil {
		panic("ogimg: parse gobold: " + err.Error())
	}
	if parsedRegularFont, err = opentype.Parse(goregular.TTF); err != nil {
		panic("ogimg: parse goregular: " + err.Error())
	}
}

// Hermeticity-class colours mirror ui/src/app.css's --color-herm-*
// tokens. Approximate hex translations of the oklch source values
// (oklch -> srgb without colour-management; close enough for OG).
func hermeticityColor(class string) color.RGBA {
	switch class {
	case "pure-starlark":
		return parseHex("5fc97c") // green
	case "prebuilt-binaries-pinned":
		return parseHex("5fc5d4") // cyan
	case "build-from-source":
		return parseHex("7fb8d9") // blue
	case "network-fetch-pinned":
		return parseHex("d8c46a") // yellow
	case "network-fetch-unpinned":
		return parseHex("e09060") // orange
	case "requires-system-tools":
		return parseHex("d68070") // red-orange
	case "repository-rule-arbitrary-code":
		return parseHex("e07070") // red
	default:
		return colorFGMute
	}
}

// hermeticityLabel maps the API class string to a shorter display
// label that fits on the OG card. Matches what the UI's
// HermeticityBadge component shows in the .label field.
func hermeticityLabel(class string) string {
	switch class {
	case "pure-starlark":
		return "pure-starlark"
	case "prebuilt-binaries-pinned":
		return "prebuilt-pinned"
	case "build-from-source":
		return "build-from-source"
	case "network-fetch-pinned":
		return "fetch-pinned"
	case "network-fetch-unpinned":
		return "fetch-unpinned"
	case "requires-system-tools":
		return "system-tools"
	case "repository-rule-arbitrary-code":
		return "repo-rule-exec"
	default:
		return ""
	}
}

// Render writes a 1200×630 PNG to w. Deterministic given spec
// (excluding GeneratedAt). Returns nil on success. Callers that
// must always serve SOMETHING on failure should wrap with Generic.
func Render(w io.Writer, spec Spec) error {
	img := image.NewRGBA(image.Rect(0, 0, imageW, imageH))
	fillRect(img, img.Bounds(), colorBG)

	// Bottom band — subtly lighter for visual hierarchy.
	fillRect(img, image.Rect(0, imageH-bottomBandH, imageW, imageH), colorBGElev)

	// Faint accent dot in the top-left, evoking the favicon mark.
	drawBracketMark(img, 36, 36, 18, colorAccent)

	// Build font faces from the parsed fonts (parsed once at init —
	// cheap to make a face, safe under concurrency because each
	// face is owned by this single Render call).
	bigFace, err := newFace(parsedBoldFont, fontModuleNameSize)
	if err != nil {
		return fmt.Errorf("ogimg: face module: %w", err)
	}
	defer bigFace.Close()

	hermFace, err := newFace(parsedRegularFont, fontHermeticitySize)
	if err != nil {
		return fmt.Errorf("ogimg: face hermeticity: %w", err)
	}
	defer hermFace.Close()

	wordmarkFace, err := newFace(parsedBoldFont, fontWordmarkSize)
	if err != nil {
		return fmt.Errorf("ogimg: face wordmark: %w", err)
	}
	defer wordmarkFace.Close()

	smallFace, err := newFace(parsedRegularFont, fontFooterSize)
	if err != nil {
		return fmt.Errorf("ogimg: face footer: %w", err)
	}
	defer smallFace.Close()

	// Wordmark, top-left after the bracket mark.
	drawText(img, "bzlhub", 78, yWordmark, wordmarkFace, colorFG)

	// Version, top-right.
	if spec.ModuleVersion != "" {
		drawTextRight(img, "v"+spec.ModuleVersion, imageW-sidePadding, yWordmark, wordmarkFace, colorFGMute)
	}

	// Centre: module name (or generic tagline if empty).
	name := spec.ModuleName
	if name == "" {
		name = "bzlhub"
	}
	name = truncate(name, 24)
	drawTextCentered(img, name, imageW/2, yModuleName, bigFace, colorFG)

	// Hermeticity label below the module name.
	if label := hermeticityLabel(spec.Hermeticity); label != "" {
		c := hermeticityColor(spec.Hermeticity)
		drawTextCentered(img, label, imageW/2, yHermeticity, hermFace, c)
	} else if spec.ModuleName == "" {
		// Generic fallback tagline when there's no module to describe.
		drawTextCentered(img, "Bazel module registry with introspection", imageW/2, yHermeticity, hermFace, colorFGMute)
	}

	// Bottom band: counts on the left, host on the right.
	parts := bottomCounts(spec)
	if parts != "" {
		drawText(img, parts, sidePadding, yFooter, smallFace, colorFGMute)
	}
	if spec.Host != "" {
		drawTextRight(img, spec.Host, imageW-sidePadding, yFooter, smallFace, colorFGMute)
	}

	if err := png.Encode(w, img); err != nil {
		return fmt.Errorf("ogimg: encode png: %w", err)
	}
	return nil
}

// Generic writes the fallback PNG — wordmark + tagline only, no
// module-specific content. Always succeeds (fonts are embedded;
// no per-call inputs to validate).
func Generic(w io.Writer) error {
	return Render(w, Spec{Host: "bzlhub.com"})
}

// --- helpers ---

// newFace creates a font.Face from a font parsed at package init.
// Cheap (no TTF re-parse), safe under concurrency (each Render owns
// its own faces).
func newFace(parsed *opentype.Font, size float64) (faceCloser, error) {
	f, err := opentype.NewFace(parsed, &opentype.FaceOptions{
		Size:    size,
		DPI:     72, // pt == px at 72 DPI; the rest of the layout is in px
		Hinting: font.HintingFull,
	})
	if err != nil {
		return faceCloser{}, err
	}
	return faceCloser{Face: f}, nil
}

// faceCloser wraps font.Face with an idempotent Close so deferred
// cleanup is safe even if Render returns early.
type faceCloser struct{ font.Face }

func (f faceCloser) Close() error {
	if f.Face == nil {
		return nil
	}
	return f.Face.Close()
}

func fillRect(dst *image.RGBA, r image.Rectangle, c color.RGBA) {
	draw.Draw(dst, r, &image.Uniform{C: c}, image.Point{}, draw.Src)
}

func drawText(dst *image.RGBA, s string, x, y int, face font.Face, c color.RGBA) {
	d := &font.Drawer{
		Dst:  dst,
		Src:  &image.Uniform{C: c},
		Face: face,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(s)
}

func drawTextRight(dst *image.RGBA, s string, x, y int, face font.Face, c color.RGBA) {
	w := font.MeasureString(face, s).Ceil()
	drawText(dst, s, x-w, y, face, c)
}

func drawTextCentered(dst *image.RGBA, s string, cx, y int, face font.Face, c color.RGBA) {
	w := font.MeasureString(face, s).Ceil()
	drawText(dst, s, cx-w/2, y, face, c)
}

// drawBracketMark renders the //foo:bar Bazel-label evoking square
// brackets that match the favicon. Centre at (cx, cy), each bracket
// `size`-tall.
func drawBracketMark(dst *image.RGBA, cx, cy, size int, c color.RGBA) {
	stroke := 2
	leftX := cx - size
	rightX := cx + size - stroke
	topY := cy - size
	botY := cy + size - stroke
	// left bracket: top horizontal, vertical, bottom horizontal
	fillRect(dst, image.Rect(leftX, topY, leftX+size/2, topY+stroke), c)
	fillRect(dst, image.Rect(leftX, topY, leftX+stroke, botY+stroke), c)
	fillRect(dst, image.Rect(leftX, botY, leftX+size/2, botY+stroke), c)
	// right bracket: mirror
	fillRect(dst, image.Rect(rightX-size/2+stroke, topY, rightX+stroke, topY+stroke), c)
	fillRect(dst, image.Rect(rightX, topY, rightX+stroke, botY+stroke), c)
	fillRect(dst, image.Rect(rightX-size/2+stroke, botY, rightX+stroke, botY+stroke), c)
	// centre dot — the : in //foo:bar
	fillRect(dst, image.Rect(cx-2, cy-2, cx+2, cy+2), c)
}

// truncate caps s to at most max characters, appending "…" when it
// trims. Module names in BCR are short ASCII so this byte/rune count
// distinction doesn't bite; if it ever does, switch to []rune.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func bottomCounts(spec Spec) string {
	var parts []string
	if spec.RuleCount > 0 {
		parts = append(parts, fmt.Sprintf("%d rules", spec.RuleCount))
	}
	if spec.DepCount > 0 {
		parts = append(parts, fmt.Sprintf("%d deps", spec.DepCount))
	}
	if spec.Versions > 0 {
		parts = append(parts, fmt.Sprintf("%d versions", spec.Versions))
	}
	return strings.Join(parts, " · ")
}

func parseHex(hex string) color.RGBA {
	// Trivial 6-hex-digit parser; the hex strings above are vendor-locked.
	if len(hex) != 6 {
		return color.RGBA{255, 255, 255, 255}
	}
	r := hexByte(hex[0])<<4 | hexByte(hex[1])
	g := hexByte(hex[2])<<4 | hexByte(hex[3])
	b := hexByte(hex[4])<<4 | hexByte(hex[5])
	return color.RGBA{R: r, G: g, B: b, A: 255}
}

func hexByte(c byte) uint8 {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}

// RenderBytes is a convenience for callers that want the PNG in
// memory rather than streaming through an io.Writer — useful for
// HTTP handlers that set Content-Length before writing the body.
func RenderBytes(spec Spec) ([]byte, error) {
	var buf bytes.Buffer
	if err := Render(&buf, spec); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
