package desktop

import (
	"syscall"
	"unsafe"
)

// The tray menu is a native popup menu (TrackPopupMenu: keyboard, submenus, closing on a click elsewhere, Windows 11's rounded
// border and shadow), but its entries are drawn by gitsi (owner-draw): a native entry is one line of one color, while the menu
// needs a two-line title, a grey right-hand column, a count as a badge and glyphs in the theme's color. Measuring and drawing
// happen on the loop's thread (WM_MEASUREITEM, WM_DRAWITEM) while the menu is open.

var (
	msimg32 = syscall.NewLazyDLL("msimg32.dll")

	procAlphaBlend            = msimg32.NewProc("AlphaBlend")
	procCreateSolidBrush      = gdi32.NewProc("CreateSolidBrush")
	procCreateFontIndirectW   = gdi32.NewProc("CreateFontIndirectW")
	procSelectObject          = gdi32.NewProc("SelectObject")
	procSetTextColor          = gdi32.NewProc("SetTextColor")
	procSetBkMode             = gdi32.NewProc("SetBkMode")
	procCreateCompatibleDC    = gdi32.NewProc("CreateCompatibleDC")
	procDeleteDC              = gdi32.NewProc("DeleteDC")
	procGetTextExtentPoint32W = gdi32.NewProc("GetTextExtentPoint32W")
	procRoundRect             = gdi32.NewProc("RoundRect")
	procGetStockObject        = gdi32.NewProc("GetStockObject")
	procExcludeClipRect       = gdi32.NewProc("ExcludeClipRect")
	procFillRect              = user32.NewProc("FillRect")
	procDrawTextW             = user32.NewProc("DrawTextW")
	procGetDC                 = user32.NewProc("GetDC")
	procReleaseDC             = user32.NewProc("ReleaseDC")
	procSystemParametersInfoW = user32.NewProc("SystemParametersInfoW")
	procSetMenuInfo           = user32.NewProc("SetMenuInfo")
)

const (
	wmDrawItem    = 0x002B
	wmMeasureItem = 0x002C
	odtMenu       = 1
	odsSelected   = 0x0001
	mfOwnerDraw   = 0x0100
	mimBackground = 0x0002
	spiGetNCM     = 0x0029
	nullPen       = 8
	transparentBk = 1
	fwBold        = 700

	dtCenter     = 0x0001
	dtRight      = 0x0002
	dtVCenter    = 0x0004
	dtSingleLine = 0x0020
	dtEllipsis   = 0x8000
	dtHidePrefix = 0x100000

	// Layout at 100% (the process is not DPI-aware: Windows scales it like the rest of the menu).
	rowHeight   = 32
	titleHeight = 48
	sepHeight   = 9
	padLeft     = 12
	iconGap     = 12
	padRight    = 12
	columnGap   = 24
	chevronSize = 12
	badgeHeight = 18
	hoverInset  = 4
	hoverRadius = 8
)

type winRect struct{ left, top, right, bottom int32 }

type winSize struct{ cx, cy int32 }

// measureItem is MEASUREITEMSTRUCT.
type measureItem struct {
	ctlType, ctlID, itemID, itemWidth, itemHeight uint32
	itemData                                      uintptr
}

// drawItem is DRAWITEMSTRUCT.
type drawItem struct {
	ctlType, ctlID, itemID, itemAction, itemState uint32
	hwndItem, hdc                                 uintptr
	rc                                            winRect
	itemData                                      uintptr
}

// menuInfo is MENUINFO.
type menuInfo struct {
	size, mask, style, cyMax uint32
	back                     uintptr
	contextHelpID            uint32
	menuData                 uintptr
}

// logFont is LOGFONTW.
type logFont struct {
	height, width, escapement, orientation, weight int32
	italic, underline, strikeOut, charSet          uint8
	outPrecision, clipPrecision, quality, pitch    uint8
	faceName                                       [32]uint16
}

// nonClientMetrics is NONCLIENTMETRICSW: only the menu font is used.
type nonClientMetrics struct {
	size                                                 uint32
	borderWidth, scrollWidth, scrollHeight, captionWidth int32
	captionHeight                                        int32
	captionFont                                          logFont
	smCaptionWidth, smCaptionHeight                      int32
	smCaptionFont                                        logFont
	menuWidth, menuHeight                                int32
	menuFont, statusFont, messageFont                    logFont
	paddedBorderWidth                                    int32
}

// palette is the menu's colors (COLORREF: 0x00BBGGRR) for the theme in use, the same as the design.
type palette struct {
	back, hover, text, detail, disabled, line, badge, badgeText uint32
	glyph, glyphDim                                             [3]uint8
}

func colorref(r, g, b uint8) uint32 { return uint32(r) | uint32(g)<<8 | uint32(b)<<16 }

var (
	darkPalette = palette{back: colorref(0x2c, 0x2c, 0x2c), hover: colorref(0x3a, 0x3a, 0x3a), text: colorref(0xff, 0xff, 0xff),
		detail: colorref(0xb5, 0xb5, 0xb5), disabled: colorref(0xa3, 0xa3, 0xa3), line: colorref(0x3d, 0x3d, 0x3d),
		badge: colorref(0x0f, 0x76, 0x6e), badgeText: colorref(0xff, 0xff, 0xff),
		glyph: [3]uint8{0xff, 0xff, 0xff}, glyphDim: [3]uint8{0xb5, 0xb5, 0xb5}}
	lightPalette = palette{back: colorref(0xf9, 0xf9, 0xf9), hover: colorref(0xe8, 0xe8, 0xe8), text: colorref(0x1b, 0x1b, 0x1b),
		detail: colorref(0x5c, 0x5c, 0x5c), disabled: colorref(0x6e, 0x6e, 0x6e), line: colorref(0xe0, 0xe0, 0xe0),
		badge: colorref(0x0f, 0x76, 0x6e), badgeText: colorref(0xff, 0xff, 0xff),
		glyph: [3]uint8{0x1b, 0x1b, 0x1b}, glyphDim: [3]uint8{0x5c, 0x5c, 0x5c}}
)

// entry is one drawn entry: the item, its glyph's bitmap and its parts.
type entry struct {
	it            MenuItem
	label, detail string // Label split at the tab
	sub           bool
	glyph         uintptr
}

// menuBuild holds what one opening of the menu creates: the actions by id, the entries by item data, and the GDI objects (fonts,
// brushes, bitmaps), freed once the menu is gone.
type menuBuild struct {
	actions []func()
	entries []entry
	bitmaps []uintptr
	state   IconState
	pal     palette
	size    int // glyph size
	font    uintptr
	bold    uintptr
	small   uintptr
	brushes map[uint32]uintptr
	check   uintptr
	chevron uintptr
	drawn   bool // set up for drawing (fonts and brushes); the tests' buildMenu is not
}

// newMenuBuild prepares the fonts and colors of one opening.
func newMenuBuild(st IconState, light bool) *menuBuild {
	b := &menuBuild{actions: []func(){nil}, state: st, pal: darkPalette, size: 16, brushes: map[uint32]uintptr{}, drawn: true}
	if light {
		b.pal = lightPalette
	}
	ncm := nonClientMetrics{}
	ncm.size = uint32(unsafe.Sizeof(ncm))
	lf := logFont{height: -12, weight: 400}
	copy(lf.faceName[:], syscall.StringToUTF16("Segoe UI"))
	if r, _, _ := procSystemParametersInfoW.Call(spiGetNCM, uintptr(ncm.size), uintptr(unsafe.Pointer(&ncm)), 0); r != 0 {
		lf = ncm.menuFont
	}
	b.font, _, _ = procCreateFontIndirectW.Call(uintptr(unsafe.Pointer(&lf)))
	bold := lf
	bold.weight = fwBold
	b.bold, _, _ = procCreateFontIndirectW.Call(uintptr(unsafe.Pointer(&bold)))
	small := bold
	small.height = lf.height * 11 / 12
	b.small, _, _ = procCreateFontIndirectW.Call(uintptr(unsafe.Pointer(&small)))
	b.check = b.glyphBitmap(glyphCheck, b.size, b.pal.glyph)
	b.chevron = b.glyphBitmap(glyphChevron, chevronSize, b.pal.glyphDim)
	return b
}

// buildMenu creates a plain menu (the tests' entry point, without drawing); popup uses newMenuBuild.
func buildMenu(items []MenuItem, actions *[]func()) uintptr {
	b := &menuBuild{actions: *actions, size: 16}
	m := b.build(items)
	*actions = b.actions
	return m
}

// build creates a popup menu with its submenus. Each entry gets the next id, its action is kept at that index and its drawing data
// at its item data.
func (b *menuBuild) build(items []MenuItem) uintptr {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return 0
	}
	if b.drawn {
		mi := menuInfo{mask: mimBackground, back: b.brush(b.pal.back)} // the margins around the entries, in the entries' color
		mi.size = uint32(unsafe.Sizeof(mi))
		procSetMenuInfo.Call(menu, uintptr(unsafe.Pointer(&mi)))
	}
	for _, it := range items {
		e := entry{it: it, label: it.Label, sub: len(it.Sub) > 0}
		for i := 0; i < len(it.Label); i++ {
			if it.Label[i] == '\t' {
				e.label, e.detail = it.Label[:i], it.Label[i+1:]
				break
			}
		}
		if it.Icon != "" {
			e.glyph = b.glyphBitmap(it.Icon, b.glyphSize(it), b.pal.glyph)
		}
		data := uintptr(len(b.entries))
		b.entries = append(b.entries, e)
		flags := uintptr(mfOwnerDraw)
		if it.Label == "" {
			procAppendMenuW.Call(menu, flags|mfSeparator, 0, data)
			continue
		}
		if it.Disabled {
			flags |= mfGrayed
		}
		if e.sub {
			sub := b.build(it.Sub)
			if sub == 0 {
				continue
			}
			procAppendMenuW.Call(menu, flags|mfPopup, sub, data)
			continue
		}
		id := len(b.actions)
		b.actions = append(b.actions, it.Action)
		procAppendMenuW.Call(menu, flags, uintptr(id), data)
	}
	return menu
}

// glyphBitmap makes a 32-bit bitmap with premultiplied alpha for a glyph (0 when the build does not draw).
func (b *menuBuild) glyphBitmap(g Glyph, px int, c [3]uint8) uintptr {
	if px <= 0 {
		return 0
	}
	pix := GlyphPixels(g, px, c[0], c[1], c[2], b.state)
	bi := bitmapInfoHeader{width: int32(px), height: -int32(px), planes: 1, bitCount: 32}
	bi.size = uint32(unsafe.Sizeof(bi))
	var bits uintptr
	h, _, _ := procCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if h == 0 || bits == 0 {
		return 0
	}
	procRtlMoveMemory.Call(bits, uintptr(unsafe.Pointer(&pix[0])), uintptr(len(pix)))
	b.bitmaps = append(b.bitmaps, h)
	return h
}

// glyphSize: the title's mark is a little larger than the entries' glyphs.
func (b *menuBuild) glyphSize(it MenuItem) int {
	if it.Icon == GlyphLogo && it.Note != "" {
		return 20
	}
	return b.size
}

func (b *menuBuild) brush(c uint32) uintptr {
	if b.brushes == nil {
		b.brushes = map[uint32]uintptr{}
	}
	if h, ok := b.brushes[c]; ok {
		return h
	}
	h, _, _ := procCreateSolidBrush.Call(uintptr(c))
	b.brushes[c] = h
	return h
}

func (b *menuBuild) free() {
	for _, h := range b.bitmaps {
		procDeleteObject.Call(h)
	}
	b.bitmaps = nil
	for _, h := range b.brushes {
		procDeleteObject.Call(h)
	}
	b.brushes = nil
	for _, h := range []uintptr{b.font, b.bold, b.small} {
		if h != 0 {
			procDeleteObject.Call(h)
		}
	}
	b.font, b.bold, b.small = 0, 0, 0
}

func (b *menuBuild) entry(data uintptr) (entry, bool) {
	if int(data) >= len(b.entries) {
		return entry{}, false
	}
	return b.entries[data], true
}

// textWidth measures a text in a font.
func textWidth(hdc, font uintptr, s string) int32 {
	if s == "" {
		return 0
	}
	u := syscall.StringToUTF16(s)
	old, _, _ := procSelectObject.Call(hdc, font)
	var sz winSize
	procGetTextExtentPoint32W.Call(hdc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&sz)))
	procSelectObject.Call(hdc, old)
	return sz.cx
}

// badgeWidth is a badge's pill: at least round, wider for longer counts.
func (b *menuBuild) badgeWidth(hdc uintptr, s string) int32 {
	w := textWidth(hdc, b.small, s) + 12
	if w < badgeHeight {
		w = badgeHeight
	}
	return w
}

// measure answers WM_MEASUREITEM.
func (b *menuBuild) measure(m *measureItem) {
	e, ok := b.entry(m.itemData)
	if !ok {
		return
	}
	if e.it.Label == "" {
		m.itemWidth, m.itemHeight = 40, sepHeight
		return
	}
	hdc, _, _ := procGetDC.Call(0)
	defer procReleaseDC.Call(0, hdc)
	font := b.font
	if e.it.Default {
		font = b.bold
	}
	w := textWidth(hdc, font, e.label)
	if e.it.Note != "" {
		if n := textWidth(hdc, b.font, e.it.Note); n > w {
			w = n
		}
	}
	right := int32(0)
	if e.detail != "" {
		right += columnGap + textWidth(hdc, b.font, e.detail)
	}
	if e.it.Badge != "" {
		right += columnGap + b.badgeWidth(hdc, e.it.Badge)
	}
	if e.sub {
		right += 8 + chevronSize
	}
	m.itemWidth = uint32(padLeft + int32(b.size) + iconGap + w + right + padRight)
	m.itemHeight = rowHeight
	if e.it.Note != "" {
		m.itemHeight = titleHeight
	}
}

// draw answers WM_DRAWITEM: the background (and the hover), the glyph or check mark, the label, and on the right the detail, the
// badge and the submenu's chevron.
func (b *menuBuild) draw(d *drawItem) {
	e, ok := b.entry(d.itemData)
	if !ok {
		return
	}
	hdc, rc := d.hdc, d.rc
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), b.brush(b.pal.back))
	// The system draws its own submenu arrow after this message unless the item's area is excluded from the clip.
	defer procExcludeClipRect.Call(hdc, uintptr(rc.left), uintptr(rc.top), uintptr(rc.right), uintptr(rc.bottom))
	if e.it.Label == "" {
		line := winRect{rc.left + hoverInset, (rc.top + rc.bottom) / 2, rc.right - hoverInset, (rc.top+rc.bottom)/2 + 1}
		procFillRect.Call(hdc, uintptr(unsafe.Pointer(&line)), b.brush(b.pal.line))
		return
	}
	if d.itemState&odsSelected != 0 && !e.it.Disabled && !e.it.Default { // the title is not a choice
		b.roundRect(hdc, winRect{rc.left + hoverInset, rc.top + 1, rc.right - hoverInset, rc.bottom - 1}, hoverRadius, b.pal.hover)
	}
	procSetBkMode.Call(hdc, transparentBk)
	x := rc.left + padLeft
	mid := (rc.top + rc.bottom) / 2
	switch {
	case e.it.Checked:
		b.blit(hdc, b.check, x, mid-int32(b.size)/2, int32(b.size))
	case e.glyph != 0:
		gs := int32(b.glyphSize(e.it))
		b.blit(hdc, e.glyph, x, mid-gs/2, gs)
	}
	x += int32(b.size) + iconGap

	right := rc.right - padRight
	if e.sub {
		b.blit(hdc, b.chevron, right-chevronSize, mid-chevronSize/2, chevronSize)
		right -= chevronSize + 8
	}
	if e.it.Badge != "" {
		w := b.badgeWidth(hdc, e.it.Badge)
		pill := winRect{right - w, mid - badgeHeight/2, right, mid + badgeHeight/2}
		b.roundRect(hdc, pill, badgeHeight, b.pal.badge)
		b.text(hdc, b.small, b.pal.badgeText, e.it.Badge, pill, dtCenter)
		right -= w + 8
	}
	if e.detail != "" {
		b.text(hdc, b.font, b.pal.detail, e.detail, winRect{x, rc.top, right, rc.bottom}, dtRight)
		right -= textWidth(hdc, b.font, e.detail) + columnGap
	}
	color := b.pal.text
	if e.it.Disabled {
		color = b.pal.disabled
	}
	font := b.font
	if e.it.Default {
		font = b.bold
	}
	if e.it.Note != "" {
		b.text(hdc, font, color, e.label, winRect{x, rc.top + 6, right, mid + 1}, 0)
		b.text(hdc, b.font, b.pal.detail, e.it.Note, winRect{x, mid + 1, right, rc.bottom - 6}, 0)
		return
	}
	b.text(hdc, font, color, e.label, winRect{x, rc.top, right, rc.bottom}, 0)
}

func (b *menuBuild) text(hdc, font uintptr, color uint32, s string, r winRect, align uintptr) {
	u := syscall.StringToUTF16(s)
	old, _, _ := procSelectObject.Call(hdc, font)
	procSetTextColor.Call(hdc, uintptr(color))
	procDrawTextW.Call(hdc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&r)),
		align|dtVCenter|dtSingleLine|dtEllipsis|dtHidePrefix)
	procSelectObject.Call(hdc, old)
}

func (b *menuBuild) roundRect(hdc uintptr, r winRect, radius int32, color uint32) {
	pen, _, _ := procGetStockObject.Call(nullPen)
	oldPen, _, _ := procSelectObject.Call(hdc, pen)
	oldBrush, _, _ := procSelectObject.Call(hdc, b.brush(color))
	procRoundRect.Call(hdc, uintptr(r.left), uintptr(r.top), uintptr(r.right+1), uintptr(r.bottom+1), uintptr(radius), uintptr(radius))
	procSelectObject.Call(hdc, oldBrush)
	procSelectObject.Call(hdc, oldPen)
}

// blit draws a premultiplied 32-bit bitmap with its alpha.
func (b *menuBuild) blit(hdc, bmp uintptr, x, y, px int32) {
	if bmp == 0 {
		return
	}
	mem, _, _ := procCreateCompatibleDC.Call(hdc)
	if mem == 0 {
		return
	}
	defer procDeleteDC.Call(mem)
	old, _, _ := procSelectObject.Call(mem, bmp)
	const blend = 0x01FF0000 // AC_SRC_OVER, constant alpha 255, AC_SRC_ALPHA
	procAlphaBlend.Call(hdc, uintptr(x), uintptr(y), uintptr(px), uintptr(px), mem, 0, 0, uintptr(px), uintptr(px), blend)
	procSelectObject.Call(mem, old)
}
