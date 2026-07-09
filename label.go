package main

// label.go — a printable, self-contained HTML label for a physical volume.
//
// The page opens in a new browser tab and is print-ready at common label sizes.
// It carries a Code128 barcode of the volume's barcode (the same string the
// "scan a barcode" lookup expects, so a printed label round-trips straight back
// into the catalog), a QR of the volume ID, and the human-readable identity:
// label, kind, capacity, created date, and — when resolved — the drive's serial
// and model. Everything is inlined as data: URIs so the tab needs no server and
// the page can be saved as a single file.

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"image/png"
	"strings"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/code128"
	qrcode "github.com/skip2/go-qrcode"
)

// code128DataURI renders text as a Code128 barcode PNG, scaled to a printable
// width, returned as a data: URI ready to drop into an <img src>.
func code128DataURI(text string) (string, error) {
	bc, err := code128.Encode(text)
	if err != nil {
		return "", err
	}
	scaled, err := barcode.Scale(bc, 520, 120)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, scaled); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// qrDataURI renders payload as a QR PNG data: URI.
func qrDataURI(payload string) (string, error) {
	png, err := qrcode.Encode(payload, qrcode.Medium, 240)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

// humanBytes renders a byte count as a compact human string (e.g. "931.5 GB").
func humanBytes(n int64) string {
	if n <= 0 {
		return "—"
	}
	const unit = 1000.0
	f := float64(n)
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	for f >= unit && i < len(units)-1 {
		f /= unit
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d %s", n, units[i])
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

// volumeLabelHTML builds the full printable HTML page for a volume. barcodeText
// is what the Code128 encodes — normally v.Barcode; callers pass a fallback when
// the volume has none yet.
func volumeLabelHTML(v *Volume, barcodeText, labelW, labelH string) (string, error) {
	if strings.TrimSpace(labelW) == "" {
		labelW = "2.25in"
	}
	if strings.TrimSpace(labelH) == "" {
		labelH = "1.25in"
	}
	if strings.TrimSpace(barcodeText) == "" {
		barcodeText = fmt.Sprintf("VOL-%d", v.ID)
	}
	bcURI, err := code128DataURI(barcodeText)
	if err != nil {
		return "", fmt.Errorf("encoding Code128 barcode: %w", err)
	}
	qrURI, err := qrDataURI(fmt.Sprintf("MNEMOVOL:%d", v.ID))
	if err != nil {
		return "", fmt.Errorf("encoding QR: %w", err)
	}

	esc := html.EscapeString
	created := ""
	if !v.CreatedAt.IsZero() {
		created = v.CreatedAt.UTC().Format("2006-01-02")
	}
	capacity := humanBytes(v.DeviceSize)

	// Identity rows shown only when present.
	var idRows strings.Builder
	row := func(k, val string) {
		if strings.TrimSpace(val) == "" {
			return
		}
		idRows.WriteString(fmt.Sprintf(`<tr><td class="k">%s</td><td class="v">%s</td></tr>`, esc(k), esc(val)))
	}
	row("Location", v.Location)
	row("Serial", v.Serial)
	row("Model", v.Model)
	row("Capacity", capacity)
	row("Created", created)
	row("Volume ID", fmt.Sprintf("%d", v.ID))

	note := ""
	if v.DeviceNote != "" {
		note = fmt.Sprintf(`<div class="note">⚠ %s</div>`, esc(v.DeviceNote))
	}

	page := fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8">
<title>Label — %s</title>
<style>
  :root{--w:%s;--h:%s}
  *{box-sizing:border-box}
  body{font:13px/1.35 -apple-system,Segoe UI,Roboto,Arial,sans-serif;margin:0;background:#f3f4f6;color:#111}
  .bar{padding:12px;display:flex;gap:8px;align-items:center;flex-wrap:wrap;background:#fff;border-bottom:1px solid #ddd}
  .bar button{font:inherit;padding:6px 12px;border:1px solid #bbb;border-radius:6px;background:#fff;cursor:pointer}
  .bar button.primary{background:#111;color:#fff;border-color:#111}
  .bar .grp{display:flex;gap:4px;align-items:center}
  .stage{padding:24px;display:flex;justify-content:center}
  .label{width:var(--w);height:var(--h);background:#fff;border:1px solid #000;border-radius:4px;
    padding:6px 8px;display:flex;flex-direction:column;overflow:hidden}
  .label .name{font-weight:700;font-size:15px;line-height:1.1;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
  .label .kind{font-size:10px;color:#333;text-transform:uppercase;letter-spacing:.04em}
  .label .mid{display:flex;gap:6px;align-items:center;flex:1;min-height:0;margin-top:2px}
  .label .bc{flex:1;min-width:0;display:flex;flex-direction:column;justify-content:center}
  .label .bc img{width:100%%;height:auto;max-height:0.42in;object-fit:contain}
  .label .bc .txt{font:11px ui-monospace,Consolas,monospace;text-align:center;letter-spacing:.06em}
  .label .qr img{height:0.62in;width:0.62in}
  .label .foot{font-size:8.5px;color:#333;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
  table.id{border-collapse:collapse;margin-top:14px;font-size:12px;max-width:var(--w)}
  table.id .k{color:#666;padding:1px 10px 1px 0}
  table.id .v{font-family:ui-monospace,Consolas,monospace}
  .note{margin-top:8px;font-size:11px;color:#8a5a00;background:#fff8e6;border:1px solid #f0d999;border-radius:6px;padding:6px 8px;max-width:var(--w)}
  .hint{color:#666;font-size:11px}
  @media print{
    .bar,.hint,table.id,.note{display:none}
    body{background:#fff}
    .stage{padding:0}
    .label{border:1px solid #000}
    @page{size:var(--w) var(--h);margin:0}
  }
</style></head><body>
<div class="bar">
  <button class="primary" onclick="window.print()">🖨 Print</button>
  <div class="grp"><span class="hint">Size:</span>
    <button onclick="setSize('2.25in','1.25in')">2.25×1.25″</button>
    <button onclick="setSize('4in','2in')">4×2″</button>
    <button onclick="setSize('62mm','29mm')">62×29mm</button>
    <button onclick="setSize('100mm','62mm')">100×62mm</button>
  </div>
  <span class="hint">Code128 scans into the barcode lookup; QR carries the volume ID.</span>
</div>
<div class="stage">
  <div class="label">
    <div class="name">%s</div>
    <div class="kind">%s</div>
    <div class="mid">
      <div class="bc"><img src="%s" alt="Code128 %s"><div class="txt">%s</div></div>
      <div class="qr"><img src="%s" alt="QR volume %d"></div>
    </div>
    <div class="foot">%s%s</div>
  </div>
</div>
<table class="id">%s</table>
%s
<script>
  function setSize(w,h){document.documentElement.style.setProperty('--w',w);document.documentElement.style.setProperty('--h',h);
    // keep @page in sync for the print box
    for(const s of document.styleSheets)try{for(const r of s.cssRules)if(r.type===CSSRule.PAGE_RULE)r.style.size=w+' '+h}catch(e){}}
</script>
</body></html>`,
		esc(v.Label),
		labelW, labelH,
		esc(v.Label),
		esc(nonEmpty(v.Kind, "VOLUME")),
		bcURI, esc(barcodeText), esc(barcodeText),
		qrURI, v.ID,
		esc(capacityFoot(capacity)), esc(footTail(v)),
		idRows.String(),
		note,
	)
	return page, nil
}

// labelSizeParts splits a stored label size ("2.25in 1.25in") into width/height,
// falling back to the default 2.25×1.25″ when unset or malformed.
func labelSizeParts(size string) (w, h string) {
	f := strings.Fields(strings.TrimSpace(size))
	if len(f) == 2 {
		return f[0], f[1]
	}
	return "2.25in", "1.25in"
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// capacityFoot / footTail build the tiny footer line printed on the label
// itself (kept short so it never overflows a small label).
func capacityFoot(capacity string) string {
	if capacity == "—" {
		return ""
	}
	return capacity
}

func footTail(v *Volume) string {
	parts := []string{}
	if v.Location != "" {
		parts = append(parts, v.Location)
	}
	if v.Serial != "" {
		parts = append(parts, "S/N "+v.Serial)
	}
	if len(parts) == 0 {
		return ""
	}
	sep := " · "
	if humanBytes(v.DeviceSize) == "—" {
		sep = ""
	}
	return sep + strings.Join(parts, " · ")
}
