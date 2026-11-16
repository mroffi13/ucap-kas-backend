package invoice

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/artech/ucapkas_backend/internal/modules/auth"
	"github.com/artech/ucapkas_backend/internal/modules/ledger"
)

func extractJpegFromLogoURL(logoURL string) ([]byte, int, int, bool) {
	trimmed := strings.TrimSpace(logoURL)
	if trimmed == "" {
		return nil, 0, 0, false
	}
	if idx := strings.Index(trimmed, "base64,"); idx >= 0 {
		trimmed = trimmed[idx+7:]
	}
	raw, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil || len(raw) < 20 || raw[0] != 0xFF || raw[1] != 0xD8 {
		return nil, 0, 0, false
	}
	// Parse JPEG SOF0/SOF1/SOF2 marker for dimensions
	i := 2
	for i+9 < len(raw) {
		if raw[i] != 0xFF {
			i++
			continue
		}
		marker := raw[i+1]
		if marker == 0xFF {
			i++
			continue
		}
		if marker == 0xD8 || marker == 0xD9 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2
			continue
		}
		segLen := int(raw[i+2])<<8 | int(raw[i+3])
		if segLen < 2 || i+2+segLen > len(raw) {
			break
		}
		if marker == 0xC0 || marker == 0xC1 || marker == 0xC2 {
			h := int(raw[i+5])<<8 | int(raw[i+6])
			w := int(raw[i+7])<<8 | int(raw[i+8])
			if w > 0 && h > 0 {
				return raw, w, h, true
			}
			break
		}
		i += 2 + segLen
	}
	return raw, 120, 120, true
}

func formatRupiahPDF(amount int64) string {
	neg := amount < 0
	if neg {
		amount = -amount
	}
	s := fmt.Sprintf("%d", amount)
	var b strings.Builder
	for i, ch := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(ch)
	}
	if neg {
		return "-Rp " + b.String()
	}
	return "Rp " + b.String()
}

func pdfEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '(':
			b.WriteString(`\(`)
		case ')':
			b.WriteString(`\)`)
		case '—', '–':
			b.WriteByte('-')
		case '•', '·':
			b.WriteString(" / ")
		default:
			if r >= 32 && r <= 126 {
				b.WriteRune(r)
			} else if r >= 160 && r <= 255 {
				b.WriteByte(byte(r))
			} else {
				b.WriteByte(' ')
			}
		}
	}
	return b.String()
}

func truncateRunes(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max-3]) + "..."
}

func wrapTextByWidth(font string, size float64, text string, maxWidth float64, maxLines int) []string {
	words := strings.Fields(strings.TrimSpace(text))
	if len(words) == 0 {
		return []string{"-"}
	}
	var lines []string
	current := words[0]
	for _, w := range words[1:] {
		candidate := current + " " + w
		if measureTextWidth(font, size, candidate) <= maxWidth {
			current = candidate
		} else {
			lines = append(lines, current)
			current = w
			if len(lines) == maxLines-1 {
				break
			}
		}
	}
	lines = append(lines, current)
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return lines
}

// Exact Adobe Type1 AFM glyph widths (in 1/1000 em) for Helvetica (F1) and Helvetica-Bold (F2).
func glyphWidthPermille(font string, r rune) int {
	bold := font == "F2"
	switch r {
	case ' ', '.', ',', ':', ';', '!', '|', 'i', 'l', 'I', '\'':
		if bold && (r == '!' || r == ':' || r == ';') {
			return 333
		}
		if r == 'i' || r == 'l' {
			if bold {
				return 278
			}
			return 222
		}
		return 278
	case '-', '(', ')', '[', ']', '`':
		return 333
	case '/', '\\':
		return 278
	case 'f', 'j', 'r', 't':
		if bold {
			if r == 'r' {
				return 389
			}
			if r == 't' {
				return 333
			}
			return 333
		}
		if r == 'r' {
			return 333
		}
		return 278
	case 's', 'z', 'c', 'k', 'v', 'x', 'y', 'J':
		if bold {
			return 556
		}
		return 500
	case 'a', 'b', 'd', 'e', 'g', 'h', 'n', 'o', 'p', 'q', 'u', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '$', '#', '+', '=', '~', '^', '?', '_', '*':
		if bold && (r == 'b' || r == 'd' || r == 'g' || r == 'h' || r == 'n' || r == 'o' || r == 'p' || r == 'q' || r == 'u') {
			return 611
		}
		return 556
	case 'F', 'L', 'T', 'Z', 'E', 'P', 'S':
		if bold {
			if r == 'L' {
				return 611
			}
			return 667
		}
		if r == 'L' {
			return 556
		}
		if r == 'T' || r == 'Z' {
			return 611
		}
		return 667
	case 'A', 'B', 'C', 'D', 'G', 'H', 'K', 'N', 'O', 'Q', 'R', 'U', 'V', 'X', 'Y', 'w':
		if r == 'G' || r == 'O' || r == 'Q' {
			return 778
		}
		if bold && (r == 'C' || r == 'D' || r == 'H' || r == 'N' || r == 'R' || r == 'U') {
			return 722
		}
		return 685
	case 'M', 'm':
		if bold {
			return 889
		}
		return 833
	case 'W':
		return 944
	case '@', '%':
		return 889
	default:
		return 556
	}
}

func measureTextWidth(font string, size float64, text string) float64 {
	escaped := pdfEscape(text)
	total := 0
	for _, r := range escaped {
		total += glyphWidthPermille(font, r)
	}
	return float64(total) * size / 1000.0
}

func sanitizeFilename(num string) string {
	var b strings.Builder
	for _, r := range num {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "UcapKas_Invoice.pdf"
	}
	return "UcapKas_" + out + ".pdf"
}

// RenderInvoicePDF generates a Swiss-editorial commercial A4 PDF 1.4 invoice.
func RenderInvoicePDF(inv InvoiceDTO, ws auth.WorkspaceDTO) ([]byte, string) {
	var stream bytes.Buffer

	drawRect := func(x, y, w, h, r, g, b float64) {
		fmt.Fprintf(&stream, "%.3f %.3f %.3f rg\n%.2f %.2f %.2f %.2f re f\n", r, g, b, x, y, w, h)
	}
	strokeRect := func(x, y, w, h, lineW, r, g, b float64) {
		fmt.Fprintf(&stream, "%.2f w %.3f %.3f %.3f RG\n%.2f %.2f %.2f %.2f re S\n", lineW, r, g, b, x, y, w, h)
	}
	drawLine := func(x1, y1, x2, y2, lineW, r, g, b float64) {
		fmt.Fprintf(&stream, "%.2f w %.3f %.3f %.3f RG\n%.2f %.2f m %.2f %.2f l S\n", lineW, r, g, b, x1, y1, x2, y2)
	}
	drawText := func(font string, size, x, y, r, g, b float64, text string) {
		fmt.Fprintf(&stream, "BT /%s %.2f Tf %.3f %.3f %.3f rg 1 0 0 1 %.2f %.2f Tm (%s) Tj ET\n",
			font, size, r, g, b, x, y, pdfEscape(text))
	}
	drawTextRight := func(font string, size, rightX, y, r, g, b float64, text string) {
		w := measureTextWidth(font, size, text)
		x := rightX - w
		if x < 48 {
			x = 48
		}
		drawText(font, size, x, y, r, g, b, text)
	}

	// Color tokens (Clean Swiss Financial Ledger — pure white sheet, deep ink, single structural accent)
	const (
		inkR, inkG, inkB       = 0.035, 0.051, 0.086 // #090D16 (Primary Ink)
		bodyR, bodyG, bodyB    = 0.200, 0.255, 0.333 // #334155 (Secondary Slate)
		mutedR, mutedG, mutedB = 0.392, 0.455, 0.545 // #64748B (Muted Label)
		ruleR, ruleG, ruleB    = 0.886, 0.910, 0.941 // #E2E8F0 (Hairline Rule)
		softR, softG, softB    = 0.973, 0.980, 0.988 // #F8FAFC (Subtle Surface)
	)

	// 1. Pure white A4 sheet (595 x 842 pt) + top 4pt structural ink rule
	drawRect(0, 0, 595, 842, 1.0, 1.0, 1.0)
	drawRect(0, 838, 595, 4, inkR, inkG, inkB)

	wsName := strings.TrimSpace(ws.Name)
	if wsName == "" {
		wsName = "UcapKas UMKM"
	}
	shortMark := strings.TrimSpace(ws.ShortName)
	if shortMark == "" {
		runes := []rune(wsName)
		if len(runes) >= 2 {
			shortMark = strings.ToUpper(string(runes[:2]))
		} else {
			shortMark = "UK"
		}
	} else {
		runes := []rune(shortMark)
		if len(runes) > 3 {
			shortMark = strings.ToUpper(string(runes[:3]))
		} else {
			shortMark = strings.ToUpper(shortMark)
		}
	}

	// 2. Editorial Header (Asymmetric Split: Brand Identity Left, Document Identity Right)
	var (
		customJpegBytes []byte
		customJpegW     int
		customJpegH     int
		hasCustomLogo   bool
	)
	if ws.IsPro {
		customJpegBytes, customJpegW, customJpegH, hasCustomLogo = extractJpegFromLogoURL(ws.LogoURL)
	}

	if !ws.IsPro {
		// Starter / Free Tier: Render Official UcapKas Vector Brand Logo (28x28pt)
		drawRect(48, 760, 28, 28, 0.051, 0.580, 0.533)        // Primary Teal #0D9488
		drawRect(54.16, 779.18, 3.5, 3.5, 0.961, 0.620, 0.043) // Spark Gold #F59E0B
		drawRect(54.16, 767.00, 3.5, 8.68, 1.0, 1.0, 1.0)      // Bar 1
		drawRect(60.32, 767.00, 3.5, 12.32, 1.0, 1.0, 1.0)     // Bar 2
		drawRect(66.48, 767.00, 3.5, 16.24, 1.0, 1.0, 1.0)     // Bar 3
		drawRect(54.16, 767.00, 15.82, 3.5, 1.0, 1.0, 1.0)     // Base bar
	} else if hasCustomLogo {
		// Pro Tier with Custom Uploaded Logo Image (JPEG XObject /Im1)
		fmt.Fprintf(&stream, "q 28.00 0 0 28.00 48.00 760.00 cm /Im1 Do Q\n")
		strokeRect(48, 760, 28, 28, 0.5, ruleR, ruleG, ruleB)
	} else {
		// Pro Tier Custom Monogram Box (28x28pt)
		drawRect(48, 760, 28, 28, inkR, inkG, inkB)
		markW := measureTextWidth("F2", 10, shortMark)
		drawText("F2", 10, 48+(28-markW)/2, 770.5, 1.0, 1.0, 1.0, shortMark)
	}

	drawText("F2", 16, 86, 775, inkR, inkG, inkB, truncateRunes(wsName, 32))
	if !ws.IsPro {
		drawText("F1", 9, 86, 762, mutedR, mutedG, mutedB, "Dokumen Tagihan / Powered by UcapKas")
	} else {
		drawText("F1", 9, 86, 762, mutedR, mutedG, mutedB, "Dokumen Tagihan & Faktur Komersial")
	}

	// Right side: INVOICE title + Reference Number
	drawTextRight("F2", 22, 547, 772, inkR, inkG, inkB, "INVOICE")
	drawTextRight("F1", 9.5, 547, 757, bodyR, bodyG, bodyB, "#"+inv.Number)

	// Status Pill (Top Right below Invoice Number)
	var (
		statusText           string
		stFgR, stFgG, stFgB  float64
		stBgR, stBgG, stBgB  float64
		stBdR, stBdG, stBdB  float64
	)
	switch inv.Status {
	case "lunas":
		statusText = "LUNAS"
		stFgR, stFgG, stFgB = 0.024, 0.373, 0.275 // #065F46
		stBgR, stBgG, stBgB = 0.925, 0.992, 0.961 // #ECFDF5
		stBdR, stBdG, stBdB = 0.655, 0.953, 0.816 // #A7F3D0
	case "jatuh_tempo":
		statusText = "JATUH TEMPO"
		stFgR, stFgG, stFgB = 0.600, 0.106, 0.106 // #991B1B
		stBgR, stBgG, stBgB = 0.996, 0.949, 0.949 // #FEF2F2
		stBdR, stBdG, stBdB = 0.996, 0.792, 0.792 // #FECACA
	default:
		statusText = "MENUNGGU PEMBAYARAN"
		stFgR, stFgG, stFgB = 0.573, 0.251, 0.055 // #92400E
		stBgR, stBgG, stBgB = 1.000, 0.984, 0.922 // #FFFBEB
		stBdR, stBdG, stBdB = 0.992, 0.902, 0.541 // #FDE68A
	}

	pillW := measureTextWidth("F2", 7.5, statusText) + 18
	pillX := 547 - pillW
	drawRect(pillX, 735, pillW, 16, stBgR, stBgG, stBgB)
	strokeRect(pillX, 735, pillW, 16, 0.6, stBdR, stBdG, stBdB)
	drawText("F2", 7.5, pillX+9, 740.5, stFgR, stFgG, stFgB, statusText)

	// 3. Structured 3-Column Metadata Grid (y = 640 .. 718)
	drawLine(48, 718, 547, 718, 0.8, inkR, inkG, inkB)
	drawLine(48, 640, 547, 640, 0.6, ruleR, ruleG, ruleB)
	drawLine(218, 640, 218, 718, 0.5, ruleR, ruleG, ruleB)
	drawLine(378, 640, 378, 718, 0.5, ruleR, ruleG, ruleB)

	// Column 1: DITAGIHKAN KEPADA
	drawText("F2", 7.5, 48, 703, mutedR, mutedG, mutedB, "DITAGIHKAN KEPADA")
	clientTitle := inv.ClientName
	if idx := strings.Index(clientTitle, "—"); idx > 0 {
		clientTitle = strings.TrimSpace(clientTitle[:idx])
	}
	drawText("F2", 11, 48, 686, inkR, inkG, inkB, truncateRunes(clientTitle, 26))
	contact := strings.TrimSpace(inv.ClientContact)
	if contact == "" {
		contact = "-"
	}
	drawText("F1", 9, 48, 671, bodyR, bodyG, bodyB, "WA / Telp: "+truncateRunes(contact, 22))
	drawText("F1", 8.5, 48, 656, mutedR, mutedG, mutedB, "Pelanggan Terdaftar")

	// Column 2: DITERBITKAN OLEH
	drawText("F2", 7.5, 230, 703, mutedR, mutedG, mutedB, "DITERBITKAN OLEH")
	drawText("F2", 10.5, 230, 686, inkR, inkG, inkB, truncateRunes(wsName, 23))
	bankMeta := "Pembayaran Transfer / Tunai"
	if strings.TrimSpace(ws.BankName) != "" {
		bankMeta = "Bank " + strings.TrimSpace(ws.BankName)
	}
	drawText("F1", 9, 230, 671, bodyR, bodyG, bodyB, truncateRunes(bankMeta, 25))
	drawText("F1", 8.5, 230, 656, mutedR, mutedG, mutedB, "Mata Uang: IDR (Rupiah)")

	// Column 3: RINCIAN TANGGAL & REFERENSI
	drawText("F2", 7.5, 390, 703, mutedR, mutedG, mutedB, "RINCIAN DOKUMEN")
	drawText("F1", 8.5, 390, 686, mutedR, mutedG, mutedB, "Tgl. Terbit")
	drawTextRight("F2", 8.5, 547, 686, inkR, inkG, inkB, inv.DateIssued)

	drawText("F1", 8.5, 390, 671, mutedR, mutedG, mutedB, "Jatuh Tempo")
	dueClean := strings.TrimPrefix(inv.DueDateLabel, "Tempo: ")
	dueR, dueG, dueB := inkR, inkG, inkB
	if inv.Status == "jatuh_tempo" {
		dueR, dueG, dueB = stFgR, stFgG, stFgB
	}
	drawTextRight("F2", 8.5, 547, 671, dueR, dueG, dueB, truncateRunes(dueClean, 20))

	drawText("F1", 8.5, 390, 656, mutedR, mutedG, mutedB, "No. Dokumen")
	drawTextRight("F1", 8.5, 547, 656, bodyR, bodyG, bodyB, truncateRunes(inv.Number, 20))

	// 4. Line Items Table (Swiss Financial Grid)
	tableTop := 614.0
	drawLine(48, tableTop, 547, tableTop, 0.8, inkR, inkG, inkB)
	drawLine(48, tableTop-22, 547, tableTop-22, 0.6, ruleR, ruleG, ruleB)

	drawText("F2", 7.5, 48, tableTop-14, mutedR, mutedG, mutedB, "NO")
	drawText("F2", 7.5, 76, tableTop-14, mutedR, mutedG, mutedB, "DESKRIPSI BARANG / JASA")
	drawTextRight("F2", 7.5, 336, tableTop-14, mutedR, mutedG, mutedB, "QTY")
	drawTextRight("F2", 7.5, 438, tableTop-14, mutedR, mutedG, mutedB, "HARGA SATUAN")
	drawTextRight("F2", 7.5, 547, tableTop-14, mutedR, mutedG, mutedB, "JUMLAH")

	items := inv.Items
	if len(items) == 0 {
		items = []ledger.LineItemDTO{
			{
				Name:      clientTitle,
				Quantity:  1,
				UnitPrice: inv.Amount,
				Subtotal:  inv.Amount,
			},
		}
	}

	y := tableTop - 40.0
	maxRows := len(items)
	if maxRows > 10 {
		maxRows = 10
	}

	var computedSubtotal int64
	for i := 0; i < maxRows; i++ {
		it := items[i]
		sub := it.Subtotal
		if sub == 0 {
			sub = int64(it.Quantity) * it.UnitPrice
		}
		computedSubtotal += sub

		descLines := wrapTextByWidth("F1", 9.5, it.Name, 224.0, 2)
		rowHeight := 26.0
		if len(descLines) > 1 {
			rowHeight = 36.0
		}

		drawText("F1", 9, 48, y, mutedR, mutedG, mutedB, fmt.Sprintf("%02d", i+1))
		drawText("F2", 9.5, 76, y, inkR, inkG, inkB, descLines[0])
		if len(descLines) > 1 {
			drawText("F1", 8.5, 76, y-12, bodyR, bodyG, bodyB, descLines[1])
		}

		drawTextRight("F1", 9.5, 336, y, bodyR, bodyG, bodyB, fmt.Sprintf("%d", it.Quantity))
		drawTextRight("F1", 9.5, 438, y, bodyR, bodyG, bodyB, formatRupiahPDF(it.UnitPrice))
		drawTextRight("F2", 9.5, 547, y, inkR, inkG, inkB, formatRupiahPDF(sub))

		lineY := y - (rowHeight - 16.0)
		drawLine(48, lineY, 547, lineY, 0.4, ruleR, ruleG, ruleB)
		y -= rowHeight
	}

	if computedSubtotal == 0 {
		computedSubtotal = inv.Amount
	}

	// 5. Right-Aligned Financial Summary Block
	sumTop := y - 6.0
	drawText("F1", 9, 350, sumTop, bodyR, bodyG, bodyB, "Subtotal")
	drawTextRight("F1", 9, 547, sumTop, inkR, inkG, inkB, formatRupiahPDF(computedSubtotal))

	drawText("F1", 9, 350, sumTop-16, bodyR, bodyG, bodyB, "Pajak / Biaya Tambahan")
	drawTextRight("F1", 9, 547, sumTop-16, mutedR, mutedG, mutedB, "Rp 0")

	drawLine(350, sumTop-26, 547, sumTop-26, 1.0, inkR, inkG, inkB)

	totalRowY := sumTop - 44
	drawRect(342, totalRowY-8, 205, 26, softR, softG, softB)
	drawText("F2", 10, 350, totalRowY, inkR, inkG, inkB, "Total Tagihan")
	totalR, totalG, totalB := inkR, inkG, inkB
	if inv.Status == "lunas" {
		totalR, totalG, totalB = stFgR, stFgG, stFgB
	}
	drawTextRight("F2", 12.5, 547, totalRowY-1, totalR, totalG, totalB, formatRupiahPDF(inv.Amount))
	drawLine(350, totalRowY-10, 547, totalRowY-10, 0.8, inkR, inkG, inkB)

	// 6. Notes (Left Side Aligned with Summary if Present)
	if strings.TrimSpace(inv.Notes) != "" {
		drawText("F2", 7.5, 48, sumTop, mutedR, mutedG, mutedB, "CATATAN TAGIHAN")
		noteLines := wrapTextByWidth("F1", 8.5, inv.Notes, 260.0, 3)
		ny := sumTop - 13.0
		for _, nl := range noteLines {
			drawText("F1", 8.5, 48, ny, bodyR, bodyG, bodyB, nl)
			ny -= 12.0
		}
	}

	// 7. Settlement & Verification Section (Anchored above footer at y = 96 .. 168)
	settleTop := 168.0
	drawLine(48, settleTop, 547, settleTop, 0.5, ruleR, ruleG, ruleB)

	// Left Accent Bar for Payment Details
	drawRect(48, 102, 2.5, 52, inkR, inkG, inkB)
	drawText("F2", 7.5, 58, 146, mutedR, mutedG, mutedB, "INSTRUKSI PEMBAYARAN")

	bankTitle := "Transfer Bank / QRIS Resmi " + wsName
	bankSub := "Cantumkan nomor referensi #" + inv.Number + " pada berita pembayaran."
	if strings.TrimSpace(ws.BankName) != "" || strings.TrimSpace(ws.BankAccountNumber) != "" {
		bankTitle = strings.TrimSpace(fmt.Sprintf("%s  -  %s", ws.BankName, ws.BankAccountNumber))
		if strings.TrimSpace(ws.BankAccountHolder) != "" {
			bankSub = "a.n. " + strings.TrimSpace(ws.BankAccountHolder) + "  |  Ref: #" + inv.Number
		}
	}
	drawText("F2", 10, 58, 130, inkR, inkG, inkB, truncateRunes(bankTitle, 52))
	drawText("F1", 8.5, 58, 116, bodyR, bodyG, bodyB, truncateRunes(bankSub, 64))
	drawText("F1", 8, 58, 103, mutedR, mutedG, mutedB, "Harap simpan bukti transfer sebagai referensi pelunasan yang sah.")

	// Right Verification Block
	drawTextRight("F2", 7.5, 547, 146, mutedR, mutedG, mutedB, "VERIFIKASI DIGITAL")
	drawTextRight("F2", 9.5, 547, 130, inkR, inkG, inkB, truncateRunes(wsName, 30))
	drawTextRight("F1", 8, 547, 116, bodyR, bodyG, bodyB, "Diterbitkan: "+inv.DateIssued)
	drawTextRight("F1", 8, 547, 103, mutedR, mutedG, mutedB, "Sah tanpa tanda tangan basah")

	// 8. Footer (Starter Watermark vs Pro White-Label)
	drawLine(48, 68, 547, 68, 0.5, ruleR, ruleG, ruleB)
	if !ws.IsPro {
		drawRect(48, 40, 499, 22, softR, softG, softB)
		strokeRect(48, 40, 499, 22, 0.5, ruleR, ruleG, ruleB)
		// Mini UcapKas Logo Mark (12x12pt) inside footer watermark
		drawRect(55, 45, 12, 12, 0.051, 0.580, 0.533)
		drawRect(57.6, 53.2, 1.5, 1.5, 0.961, 0.620, 0.043)
		drawRect(57.6, 48.0, 1.5, 3.7, 1.0, 1.0, 1.0)
		drawRect(60.3, 48.0, 1.5, 5.3, 1.0, 1.0, 1.0)
		drawRect(63.0, 48.0, 1.5, 7.0, 1.0, 1.0, 1.0)
		drawRect(57.6, 48.0, 6.9, 1.5, 1.0, 1.0, 1.0)

		drawText("F2", 8.5, 73, 48.5, inkR, inkG, inkB, "Invoice dibuat oleh aplikasi UcapKas, tersedia di Play Store gratis")
		drawTextRight("F1", 8, 539, 48.5, mutedR, mutedG, mutedB, "Dokumen #"+inv.Number)
	} else {
		drawText("F1", 8, 48, 53, mutedR, mutedG, mutedB, truncateRunes(wsName, 36)+"  /  Dokumen #"+inv.Number)
		drawTextRight("F1", 8, 547, 53, mutedR, mutedG, mutedB, "Halaman 1 dari 1  -  Faktur Resmi "+truncateRunes(wsName, 24))
	}

	// Assemble PDF 1.4 Objects with exact byte offsets
	contentBytes := stream.Bytes()
	pageResources := "<< /Font << /F1 4 0 R /F2 5 0 R >> >>"
	if hasCustomLogo {
		pageResources = "<< /Font << /F1 4 0 R /F2 5 0 R >> /XObject << /Im1 7 0 R >> >>"
	}
	objs := []string{
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n",
		fmt.Sprintf("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources %s /Contents 6 0 R >>\nendobj\n", pageResources),
		"4 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>\nendobj\n",
		"5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>\nendobj\n",
		fmt.Sprintf("6 0 obj\n<< /Length %d >>\nstream\n%sendstream\nendobj\n", len(contentBytes), string(contentBytes)),
	}
	if hasCustomLogo {
		var imgObj bytes.Buffer
		fmt.Fprintf(
			&imgObj,
			"7 0 obj\n<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>\nstream\n",
			customJpegW,
			customJpegH,
			len(customJpegBytes),
		)
		imgObj.Write(customJpegBytes)
		imgObj.WriteString("\nendstream\nendobj\n")
		objs = append(objs, imgObj.String())
	}

	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n%\xE2\xE3\xCF\xD3\n")
	offsets := make([]int, len(objs)+1)
	for i, obj := range objs {
		offsets[i+1] = pdf.Len()
		pdf.WriteString(obj)
	}
	xrefOffset := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xrefOffset)

	return pdf.Bytes(), sanitizeFilename(inv.Number)
}
