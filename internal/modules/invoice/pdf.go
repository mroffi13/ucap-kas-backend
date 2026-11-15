package invoice

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/artech/ucapkas_backend/internal/modules/auth"
)

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
			b.WriteString(" - ")
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

// RenderInvoicePDF generates a valid single-page A4 PDF 1.4 document using standard Type1 fonts.
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
		fmt.Fprintf(&stream, "BT /%s %.1f Tf %.3f %.3f %.3f rg %.2f %.2f Td (%s) Tj ET\n",
			font, size, r, g, b, x, y, pdfEscape(text))
	}

	// Page Background (A4: 595 x 842 pt)
	drawRect(0, 0, 595, 842, 0.976, 0.980, 0.984)

	// Main Invoice Sheet Card
	drawRect(36, 48, 523, 758, 1.0, 1.0, 1.0)
	strokeRect(36, 48, 523, 758, 1.0, 0.886, 0.910, 0.941)

	// Top Header Banner (Nordic Ledger Obsidian #0F172A)
	drawRect(36, 718, 523, 88, 0.059, 0.090, 0.165)

	wsName := ws.Name
	if strings.TrimSpace(wsName) == "" {
		wsName = "UcapKas UMKM"
	}
	drawText("F2", 18, 58, 772, 1.0, 1.0, 1.0, truncateRunes(wsName, 36))
	drawText("F1", 10.5, 58, 752, 0.580, 0.639, 0.722, "INVOICE & BUKTI TAGIHAN RESMI")
	drawText("F3", 11, 58, 734, 0.886, 0.910, 0.941, "No: "+inv.Number)

	// Status Pill on Header Right
	var statusLabel string
	var badgeR, badgeG, badgeB float64
	switch inv.Status {
	case "lunas":
		statusLabel = "LUNAS"
		badgeR, badgeG, badgeB = 0.051, 0.361, 0.227 // Forest Jade
	case "jatuh_tempo":
		statusLabel = "LEWAT JATUH TEMPO"
		badgeR, badgeG, badgeB = 0.863, 0.149, 0.149 // Rose
	default:
		statusLabel = "MENUNGGU PEMBAYARAN"
		badgeR, badgeG, badgeB = 0.851, 0.467, 0.024 // Amber
	}
	drawRect(392, 756, 145, 26, badgeR, badgeG, badgeB)
	drawText("F2", 9.5, 404, 765.5, 1.0, 1.0, 1.0, statusLabel)

	// Recipient & Due Date Section
	drawText("F2", 9.5, 58, 690, 0.392, 0.455, 0.545, "DITAGIHKAN KEPADA")
	clientTitle := inv.ClientName
	if idx := strings.Index(clientTitle, "—"); idx > 0 {
		clientTitle = strings.TrimSpace(clientTitle[:idx])
	}
	drawText("F2", 13.5, 58, 672, 0.059, 0.090, 0.165, truncateRunes(clientTitle, 40))
	drawText("F1", 10.5, 58, 655, 0.278, 0.333, 0.412, "Kontak / WA: "+truncateRunes(inv.ClientContact, 36))

	drawText("F2", 9.5, 355, 690, 0.392, 0.455, 0.545, "INFORMASI TANGGAL")
	drawText("F1", 10.5, 355, 672, 0.278, 0.333, 0.412, "Tanggal Terbit: "+inv.DateIssued)
	drawText("F2", 10.5, 355, 655, 0.059, 0.090, 0.165, truncateRunes(inv.DueDateLabel, 34))

	drawLine(58, 636, 537, 636, 0.8, 0.886, 0.910, 0.941)

	// Table Header
	drawRect(58, 602, 479, 24, 0.945, 0.961, 0.976)
	drawText("F2", 9.5, 68, 610, 0.278, 0.333, 0.412, "NO")
	drawText("F2", 9.5, 98, 610, 0.278, 0.333, 0.412, "RINCIAN BARANG / JASA")
	drawText("F2", 9.5, 318, 610, 0.278, 0.333, 0.412, "QTY")
	drawText("F2", 9.5, 360, 610, 0.278, 0.333, 0.412, "HARGA SATUAN")
	drawText("F2", 9.5, 456, 610, 0.278, 0.333, 0.412, "SUBTOTAL")

	// Table Rows (max 10 rows to fit cleanly on 1 A4 page)
	y := 580.0
	items := inv.Items
	if len(items) == 0 {
		drawText("F1", 10.5, 68, y, 0.059, 0.090, 0.165, "01")
		drawText("F1", 10.5, 98, y, 0.059, 0.090, 0.165, truncateRunes(inv.ClientName, 36))
		drawText("F3", 10.5, 318, y, 0.059, 0.090, 0.165, "1x")
		drawText("F3", 10.5, 360, y, 0.059, 0.090, 0.165, formatRupiahPDF(inv.Amount))
		drawText("F3", 10.5, 456, y, 0.059, 0.090, 0.165, formatRupiahPDF(inv.Amount))
		drawLine(58, y-8, 537, y-8, 0.5, 0.925, 0.941, 0.961)
		y -= 26.0
	} else {
		maxRows := len(items)
		if maxRows > 10 {
			maxRows = 10
		}
		for i := 0; i < maxRows; i++ {
			it := items[i]
			if i%2 == 1 {
				drawRect(58, y-7, 479, 24, 0.984, 0.988, 0.992)
			}
			drawText("F3", 10, 68, y, 0.392, 0.455, 0.545, fmt.Sprintf("%02d", i+1))
			drawText("F1", 10.5, 98, y, 0.059, 0.090, 0.165, truncateRunes(it.Name, 34))
			drawText("F3", 10.5, 318, y, 0.059, 0.090, 0.165, fmt.Sprintf("%dx", it.Quantity))
			drawText("F3", 10, 360, y, 0.278, 0.333, 0.412, formatRupiahPDF(it.UnitPrice))
			drawText("F3", 10.5, 456, y, 0.059, 0.090, 0.165, formatRupiahPDF(it.Subtotal))
			drawLine(58, y-7, 537, y-7, 0.5, 0.925, 0.941, 0.961)
			y -= 24.0
		}
	}

	// Grand Total Summary Box
	totalBoxY := y - 42.0
	drawRect(295, totalBoxY, 242, 40, 0.059, 0.090, 0.165)
	drawText("F2", 9.5, 309, totalBoxY+24, 0.796, 0.835, 0.882, "TOTAL TAGIHAN")
	drawText("F2", 14, 309, totalBoxY+8, 1.0, 1.0, 1.0, formatRupiahPDF(inv.Amount))

	// Payment Instructions Box
	payBoxY := totalBoxY - 82.0
	drawRect(58, payBoxY, 479, 64, 0.961, 0.976, 0.969)
	strokeRect(58, payBoxY, 479, 64, 0.8, 0.75, 0.86, 0.81)
	drawText("F2", 10, 72, payBoxY+44, 0.051, 0.361, 0.227, "METODE PEMBAYARAN / TRANSFER")

	bankLine := "Transfer Bank / QRIS Toko Resmi (" + wsName + ")"
	if strings.TrimSpace(ws.BankName) != "" || strings.TrimSpace(ws.BankAccountNumber) != "" {
		bankLine = strings.TrimSpace(fmt.Sprintf("%s %s a.n %s", ws.BankName, ws.BankAccountNumber, ws.BankAccountHolder))
	}
	drawText("F2", 10.5, 72, payBoxY+27, 0.059, 0.090, 0.165, truncateRunes(bankLine, 65))
	drawText("F1", 9.5, 72, payBoxY+11, 0.392, 0.455, 0.545, "Mohon cantumkan nomor "+inv.Number+" pada berita transfer.")

	// Footer
	drawLine(58, 86, 537, 86, 0.6, 0.886, 0.910, 0.941)
	drawText("F1", 9, 58, 68, 0.392, 0.455, 0.545, "Dokumen invoice digital ini diterbitkan secara sah melalui aplikasi UcapKas.")

	// Assemble PDF 1.4 Objects with exact byte offsets
	contentBytes := stream.Bytes()
	objs := []string{
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n",
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 4 0 R /F2 5 0 R /F3 6 0 R >> >> /Contents 7 0 R >>\nendobj\n",
		"4 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>\nendobj\n",
		"5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>\nendobj\n",
		"6 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Courier-Bold /Encoding /WinAnsiEncoding >>\nendobj\n",
		fmt.Sprintf("7 0 obj\n<< /Length %d >>\nstream\n%sendstream\nendobj\n", len(contentBytes), string(contentBytes)),
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
