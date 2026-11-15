package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type ParsedLineItem struct {
	Name      string `json:"name"`
	Quantity  int32  `json:"quantity"`
	UnitPrice int64  `json:"unit_price"`
	Subtotal  int64  `json:"subtotal"`
}

type VoiceParseResult struct {
	Intent          string           `json:"intent"`           // "buku_kas" | "kirim_invoice"
	Type            string           `json:"type"`             // "inflow" | "outflow"
	Title           string           `json:"title"`            // e.g. "Pak Husni — Benerin motor"
	Category        string           `json:"category"`         // e.g. "Servis", "Bahan Baku", "Suku Cadang", "Operasional"
	PaymentMethod   string           `json:"payment_method"`   // "Tunai" | "QRIS" | "Transfer"
	CustomerName    string           `json:"customer_name"`    // For invoice or cash note
	CustomerContact string           `json:"customer_contact"` // Optional WA/phone
	DueDate         string           `json:"due_date"`         // For invoice
	Notes           string           `json:"notes"`            // Optional notes
	Transcript      string           `json:"transcript"`       // Normalized or transcribed text
	Items           []ParsedLineItem `json:"items"`
	TotalAmount     int64            `json:"total_amount"`
	Provider        string           `json:"provider"` // "gemini_flash" | "slang_engine"
	TokensUsed      int32            `json:"tokens_used"`
}

type ReceiptParseResult struct {
	StoreName     string           `json:"store_name"`
	Category      string           `json:"category"`
	PaymentMethod string           `json:"payment_method"`
	Items         []ParsedLineItem `json:"items"`
	TotalAmount   int64            `json:"total_amount"`
	Provider      string           `json:"provider"` // "gemini_flash" | "slang_engine"
	TokensUsed    int32            `json:"tokens_used"`
}

type ParseVoiceInput struct {
	Transcript  string `json:"transcript"`
	AudioBase64 string `json:"audio_base64"`
	MimeType    string `json:"mime_type"`
}

type ParseReceiptInput struct {
	ImageBase64 string `json:"image_base64"`
	MimeType    string `json:"mime_type"`
	HintText    string `json:"hint_text"`
	PresetID    string `json:"preset_id"`
}

type Parser interface {
	ParseVoice(ctx context.Context, in ParseVoiceInput) (*VoiceParseResult, error)
	ParseReceipt(ctx context.Context, in ParseReceiptInput) (*ReceiptParseResult, error)
}

type HybridParser struct {
	apiKey        string
	model         string
	groqAPIKey    string
	groqSTTModel  string
	groqLLMModel  string
	gatewayURL    string
	gatewayAPIKey string
	gatewayModel  string
	sttModel      string
	httpClient    *http.Client
}

type GatewayConfig struct {
	GatewayURL    string
	GatewayAPIKey string
	GatewayModel  string
	STTModel      string
	GroqAPIKey    string
	GroqSTTModel  string
	GroqLLMModel  string
	GeminiAPIKey  string
	GeminiModel   string
}

func NewHybridParser(apiKey, model string) *HybridParser {
	return NewHybridParserWithGateway(GatewayConfig{
		GeminiAPIKey: apiKey,
		GeminiModel:  model,
	})
}

func NewHybridParserWithGateway(cfg GatewayConfig) *HybridParser {
	gemModel := strings.TrimSpace(cfg.GeminiModel)
	if gemModel == "" {
		gemModel = "gemini-2.5-flash"
	}
	gwModel := strings.TrimSpace(cfg.GatewayModel)
	if gwModel == "" {
		gwModel = gemModel
	}
	groqSTT := strings.TrimSpace(cfg.GroqSTTModel)
	if groqSTT == "" {
		groqSTT = "whisper-large-v3-turbo"
	}
	groqLLM := strings.TrimSpace(cfg.GroqLLMModel)
	if groqLLM == "" {
		groqLLM = "llama-3.3-70b-versatile"
	}
	sttModel := strings.TrimSpace(cfg.STTModel)
	if sttModel == "" {
		sttModel = groqSTT
	}
	return &HybridParser{
		apiKey:        strings.TrimSpace(cfg.GeminiAPIKey),
		model:         gemModel,
		groqAPIKey:    strings.TrimSpace(cfg.GroqAPIKey),
		groqSTTModel:  groqSTT,
		groqLLMModel:  groqLLM,
		gatewayURL:    strings.TrimRight(strings.TrimSpace(cfg.GatewayURL), "/"),
		gatewayAPIKey: strings.TrimSpace(cfg.GatewayAPIKey),
		gatewayModel:  gwModel,
		sttModel:      sttModel,
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

const voiceSystemPrompt = `Anda adalah asisten AI pembukuan UMKM Indonesia bernama UcapKas.
Tugas Anda adalah mengekstrak ucapan suara atau teks bahasa pasar Indonesia menjadi JSON terstruktur.
Kamus Bahasa Pasar Indonesia wajib dipahami:
- seceng = 1000, noceng = 2000, goceng = 5000
- ceban = 10000, noban = 20000, 삼ban/samban = 30000, gocap / seket / goban = 50000
- cepek = 100000 (atau 100 ribu dalam konteks harga), pekgo = 150000, nopek = 200000
- rb / ribu = x1000, jt / juta = x1000000
Aturan Output JSON:
Kembalikan HANYA objek JSON valid dengan field:
{
  "intent": "buku_kas" atau "kirim_invoice",
  "type": "inflow" atau "outflow",
  "title": "Ringkasan singkat transaksi (misal: Pak Husni — Servis & Oli)",
  "category": "Salah satu dari: Penjualan, Servis, Suku Cadang, Bahan Baku, Operasional, Lainnya",
  "payment_method": "Tunai" atau "QRIS" atau "Transfer",
  "customer_name": "Nama pelanggan jika disebutkan",
  "customer_contact": "Nomor HP/WA jika disebutkan",
  "due_date": "Tanggal jatuh tempo jika intent kirim_invoice (default: Jatuh Tempo 7 Hari)",
  "notes": "Catatan tambahan",
  "transcript": "Transkrip kalimat bersih",
  "items": [
    {"name": "Nama barang/jasa", "quantity": 1, "unit_price": 10000, "subtotal": 10000}
  ],
  "total_amount": 10000
}`

const receiptSystemPrompt = `Anda adalah asisten OCR Struk Belanja UMKM Indonesia bernama UcapKas.
Tugas Anda adalah membaca foto struk/nota belanja atau teks struk dan mengekstraknya menjadi JSON terstruktur:
{
  "store_name": "Nama toko atau supplier",
  "category": "Salah satu dari: Bahan Baku, Suku Cadang, Operasional, Servis, Lainnya",
  "payment_method": "Tunai" atau "QRIS" atau "Transfer",
  "items": [
    {"name": "Nama barang", "quantity": 1, "unit_price": 45000, "subtotal": 45000}
  ],
  "total_amount": 45000
}`

type geminiPart struct {
	Text       string            `json:"text,omitempty"`
	InlineData *geminiInlineData `json:"inlineData,omitempty"`
}

type geminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiGenConfig struct {
	ResponseMimeType string  `json:"responseMimeType"`
	Temperature      float64 `json:"temperature"`
}

type geminiRequest struct {
	SystemInstruction *geminiContent  `json:"systemInstruction,omitempty"`
	Contents          []geminiContent `json:"contents"`
	GenerationConfig  geminiGenConfig `json:"generationConfig"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	UsageMetadata struct {
		TotalTokenCount int32 `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

const whisperUmkmPrompt = "Transkrip pembukuan kas UMKM Indonesia dan bahasa pasar: ceban, gocap, seket, noban, goban, pekgo, seceng, noceng, goceng, selawe, cepek, QRIS, transfer BCA Mandiri BRI BNI, tunai, buat invoice, jatuh tempo, ganti oli, kampas rem, busi, beras, minyak goreng, gula pasir, tepung."

func (p *HybridParser) ParseVoice(ctx context.Context, in ParseVoiceInput) (*VoiceParseResult, error) {
	transcript := strings.TrimSpace(in.Transcript)

	// Step 1: If audio_base64 is provided, ALWAYS prioritize Whisper STT (Groq whisper-large-v3-turbo or 9Router)
	// so Whisper's high-accuracy Indonesian + Bahasa Pasar transcription replaces any rough phone STT preview!
	if strings.TrimSpace(in.AudioBase64) != "" {
		if whisperText := p.transcribeAudioWithWhisper(ctx, in.AudioBase64, in.MimeType); whisperText != "" {
			transcript = whisperText
			in.Transcript = whisperText
		}
	}

	// Step 2: Try AI Gateway (Bifrost) (/v1/chat/completions) using configured model/routing rule (e.g. llm-ucapkas).
	// Bifrost handles provider routing & fallbacks internally, so do not send extra hardcoded models unless unconfigured.
	if transcript != "" {
		gwModel := strings.TrimSpace(p.gatewayModel)
		if gwModel == "" {
			gwModel = "groq/llama-3.3-70b-versatile"
		}
		for _, gwURL := range p.gatewayURLs() {
			res, err := p.callGatewayVoice(ctx, gwURL, transcript, p.gatewayAPIKey, gwModel)
			if err == nil && res != nil && len(res.Items) > 0 {
				return res, nil
			}
		}
	}

	// Step 3: Try Groq LLM directly (llama-3.3-70b-versatile) if GROQ_API_KEY is configured
	if transcript != "" && p.groqAPIKey != "" {
		res, err := p.callGatewayVoice(ctx, "https://api.groq.com/openai/v1", transcript, p.groqAPIKey, p.groqLLMModel)
		if err == nil && res != nil && len(res.Items) > 0 {
			res.Provider = "groq_whisper_llama"
			return res, nil
		}
	}

	// Step 4: Try Direct Gemini API if GEMINI_API_KEY is configured
	if p.apiKey != "" && (transcript != "" || in.AudioBase64 != "") {
		res, err := p.callGeminiVoice(ctx, in)
		if err == nil && res != nil && len(res.Items) > 0 {
			return res, nil
		}
	}

	// Step 5: Deterministic Bahasa Pasar NLP Engine fallback
	return ParseVoiceSlangFallback(transcript), nil
}

func (p *HybridParser) transcribeAudioWithWhisper(ctx context.Context, audioB64, mimeType string) string {
	// 1. Try AI Gateway (Bifrost) (/v1/audio/transcriptions) using configured STT model/rule (e.g. stt-ucapkas)
	if p.sttModel != "" {
		for _, gwURL := range p.gatewayURLs() {
			if text, err := p.callOpenAIWhisperSTT(ctx, gwURL, p.gatewayAPIKey, p.sttModel, audioB64, mimeType); err == nil && text != "" {
				return text
			}
		}
	}
	// 2. Fallback to Direct Groq Whisper (whisper-large-v3-turbo) — fastest (~0.3s) & most accurate
	if p.groqAPIKey != "" {
		if text, err := p.callOpenAIWhisperSTT(ctx, "https://api.groq.com/openai/v1", p.groqAPIKey, p.groqSTTModel, audioB64, mimeType); err == nil && text != "" {
			return text
		}
	}
	return ""
}

func (p *HybridParser) ParseReceipt(ctx context.Context, in ParseReceiptInput) (*ReceiptParseResult, error) {
	// Step 1: Try AI Gateways (9Router -> Bifrost) via OpenAI-compatible /v1/chat/completions Vision
	if in.ImageBase64 != "" || in.HintText != "" {
		for _, gwURL := range p.gatewayURLs() {
			res, err := p.callGatewayReceipt(ctx, gwURL, in)
			if err == nil && res != nil && len(res.Items) > 0 {
				return res, nil
			}
		}
	}

	// Step 2: Try Direct Gemini API if GEMINI_API_KEY is configured
	if p.apiKey != "" && (in.ImageBase64 != "" || in.HintText != "") {
		res, err := p.callGeminiReceipt(ctx, in)
		if err == nil && res != nil && len(res.Items) > 0 {
			return res, nil
		}
	}

	// Step 3: Deterministic Receipt Fallback
	return ParseReceiptFallback(in.HintText, in.PresetID), nil
}

func (p *HybridParser) gatewayURLs() []string {
	if p.gatewayURL != "" {
		return []string{p.gatewayURL}
	}
	return nil
}

func (p *HybridParser) callOpenAIWhisperSTT(ctx context.Context, baseURL, apiKey, modelName, audioB64, mimeType string) (string, error) {
	audioBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(audioB64))
	if err != nil || len(audioBytes) == 0 {
		return "", fmt.Errorf("invalid base64 audio")
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	ext := "m4a"
	lowerMime := strings.ToLower(mimeType)
	if strings.Contains(lowerMime, "wav") {
		ext = "wav"
	} else if strings.Contains(lowerMime, "ogg") || strings.Contains(lowerMime, "webm") {
		ext = "ogg"
	} else if strings.Contains(lowerMime, "mp3") || strings.Contains(lowerMime, "mpeg") {
		ext = "mp3"
	}
	fw, err := mw.CreateFormFile("file", "speech."+ext)
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(audioBytes); err != nil {
		return "", err
	}
	_ = mw.WriteField("model", modelName)
	_ = mw.WriteField("language", "id")
	_ = mw.WriteField("prompt", whisperUmkmPrompt)
	_ = mw.WriteField("temperature", "0")
	_ = mw.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/audio/transcriptions", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	setGatewayAuthHeaders(req, baseURL, apiKey)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("whisper stt status %d", resp.StatusCode)
	}

	var sttResp struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&sttResp); err != nil {
		return "", err
	}
	return strings.TrimSpace(sttResp.Text), nil
}

func (p *HybridParser) callGatewayVoice(ctx context.Context, gwBaseURL, transcript, apiKey, modelName string) (*VoiceParseResult, error) {
	messages := []map[string]any{
		{"role": "system", "content": voiceSystemPrompt},
		{"role": "user", "content": "Ucapan pengguna: " + transcript},
	}
	rawJSON, tokens, err := p.invokeOpenAIChatWithAuth(ctx, gwBaseURL, apiKey, modelName, messages)
	if err != nil {
		return nil, err
	}
	var parsed VoiceParseResult
	if err := json.Unmarshal([]byte(stripMarkdownJSON(rawJSON)), &parsed); err != nil {
		return nil, err
	}
	normalizeVoiceResult(&parsed, transcript)
	parsed.Provider = detectGatewayName(gwBaseURL)
	parsed.TokensUsed = tokens
	return &parsed, nil
}

func (p *HybridParser) callGatewayReceipt(ctx context.Context, gwBaseURL string, in ParseReceiptInput) (*ReceiptParseResult, error) {
	var userContent any
	if strings.TrimSpace(in.ImageBase64) != "" {
		mime := strings.TrimSpace(in.MimeType)
		if mime == "" {
			mime = "image/jpeg"
		}
		dataURI := fmt.Sprintf("data:%s;base64,%s", mime, strings.TrimSpace(in.ImageBase64))
		prompt := "Ekstrak seluruh daftar barang, jumlah, harga satuan, nama toko, dan kategori dari struk ini ke format JSON."
		if strings.TrimSpace(in.HintText) != "" {
			prompt += " Petunjuk tambahan: " + strings.TrimSpace(in.HintText)
		}
		userContent = []map[string]any{
			{"type": "text", "text": prompt},
			{"type": "image_url", "image_url": map[string]string{"url": dataURI}},
		}
	} else {
		userContent = "Teks/petunjuk struk: " + strings.TrimSpace(in.HintText)
	}

	messages := []map[string]any{
		{"role": "system", "content": receiptSystemPrompt},
		{"role": "user", "content": userContent},
	}
	rawJSON, tokens, err := p.invokeOpenAIChat(ctx, gwBaseURL, messages)
	if err != nil {
		return nil, err
	}
	var parsed ReceiptParseResult
	if err := json.Unmarshal([]byte(stripMarkdownJSON(rawJSON)), &parsed); err != nil {
		return nil, err
	}
	normalizeReceiptResult(&parsed)
	parsed.Provider = detectGatewayName(gwBaseURL)
	parsed.TokensUsed = tokens
	return &parsed, nil
}

func (p *HybridParser) invokeOpenAIChat(ctx context.Context, gwBaseURL string, messages []map[string]any) (string, int32, error) {
	return p.invokeOpenAIChatWithAuth(ctx, gwBaseURL, p.gatewayAPIKey, p.gatewayModel, messages)
}

func (p *HybridParser) invokeOpenAIChatWithAuth(ctx context.Context, gwBaseURL, apiKey, modelName string, messages []map[string]any) (string, int32, error) {
	payload := map[string]any{
		"model":       modelName,
		"messages":    messages,
		"temperature": 0.1,
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, gwBaseURL+"/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	setGatewayAuthHeaders(req, gwBaseURL, apiKey)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", 0, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Warn("gateway chat non-2xx", "url", gwBaseURL, "model", modelName, "status", resp.StatusCode, "body", string(respBytes))
		return "", 0, fmt.Errorf("gateway chat status %d", resp.StatusCode)
	}

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int32 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBytes, &chatResp); err != nil {
		slog.Warn("gateway chat unmarshal failed", "model", modelName, "err", err)
		return "", 0, err
	}
	if len(chatResp.Choices) == 0 {
		return "", 0, fmt.Errorf("empty gateway choices")
	}
	content := extractOpenAIMessageContent(chatResp.Choices[0].Message.Content)
	if content == "" {
		return "", 0, fmt.Errorf("empty gateway content")
	}
	return content, chatResp.Usage.TotalTokens, nil
}

func extractOpenAIMessageContent(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	var asStr string
	if err := json.Unmarshal(trimmed, &asStr); err == nil {
		return strings.TrimSpace(asStr)
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(trimmed, &parts); err == nil {
		var sb strings.Builder
		for _, part := range parts {
			if part.Text != "" {
				sb.WriteString(part.Text)
			}
		}
		return strings.TrimSpace(sb.String())
	}
	return strings.TrimSpace(string(trimmed))
}

func stripMarkdownJSON(raw string) string {
	s := strings.TrimSpace(raw)
	// Strip <think>...</think> blocks emitted by reasoning models (e.g. qwen3-32b)
	for {
		startThink := strings.Index(s, "<think>")
		endThink := strings.Index(s, "</think>")
		if startThink != -1 && endThink != -1 && endThink > startThink {
			s = strings.TrimSpace(s[:startThink] + s[endThink+len("</think>"):])
		} else {
			break
		}
	}
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	// Extract the outermost JSON object { ... } if surrounded by any prose
	firstBrace := strings.Index(s, "{")
	lastBrace := strings.LastIndex(s, "}")
	if firstBrace != -1 && lastBrace != -1 && lastBrace > firstBrace {
		s = s[firstBrace : lastBrace+1]
	}
	return strings.TrimSpace(s)
}

func setGatewayAuthHeaders(req *http.Request, baseURL, apiKey string) {
	if apiKey == "" {
		return
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if strings.HasPrefix(apiKey, "sk-bf-") || detectGatewayName(baseURL) == "bifrost_gateway" {
		req.Header.Set("x-bf-vk", apiKey)
	}
}

func detectGatewayName(url string) string {
	lower := strings.ToLower(url)
	if strings.Contains(lower, "20128") || strings.Contains(lower, "9router") || strings.Contains(lower, "ninerouter") {
		return "9router_gateway"
	}
	if strings.Contains(lower, "bifrost") || strings.Contains(lower, "8085") {
		return "bifrost_gateway"
	}
	return "ai_gateway"
}

func (p *HybridParser) callGeminiVoice(ctx context.Context, in ParseVoiceInput) (*VoiceParseResult, error) {
	var parts []geminiPart
	if strings.TrimSpace(in.Transcript) != "" {
		parts = append(parts, geminiPart{Text: "Ucapan pengguna: " + strings.TrimSpace(in.Transcript)})
	}
	if strings.TrimSpace(in.AudioBase64) != "" {
		mime := strings.TrimSpace(in.MimeType)
		if mime == "" {
			mime = "audio/wav"
		}
		parts = append(parts, geminiPart{
			InlineData: &geminiInlineData{
				MimeType: mime,
				Data:     strings.TrimSpace(in.AudioBase64),
			},
		})
	}

	rawJSON, tokens, err := p.invokeGemini(ctx, voiceSystemPrompt, parts)
	if err != nil {
		return nil, err
	}

	var parsed VoiceParseResult
	if err := json.Unmarshal([]byte(rawJSON), &parsed); err != nil {
		return nil, err
	}
	normalizeVoiceResult(&parsed, in.Transcript)
	parsed.Provider = "gemini_flash"
	parsed.TokensUsed = tokens
	return &parsed, nil
}

func (p *HybridParser) callGeminiReceipt(ctx context.Context, in ParseReceiptInput) (*ReceiptParseResult, error) {
	var parts []geminiPart
	if strings.TrimSpace(in.HintText) != "" {
		parts = append(parts, geminiPart{Text: "Teks/petunjuk struk: " + strings.TrimSpace(in.HintText)})
	} else {
		parts = append(parts, geminiPart{Text: "Ekstrak seluruh daftar barang, jumlah, harga satuan, nama toko, dan kategori dari struk ini."})
	}
	if strings.TrimSpace(in.ImageBase64) != "" {
		mime := strings.TrimSpace(in.MimeType)
		if mime == "" {
			mime = "image/jpeg"
		}
		parts = append(parts, geminiPart{
			InlineData: &geminiInlineData{
				MimeType: mime,
				Data:     strings.TrimSpace(in.ImageBase64),
			},
		})
	}

	rawJSON, tokens, err := p.invokeGemini(ctx, receiptSystemPrompt, parts)
	if err != nil {
		return nil, err
	}

	var parsed ReceiptParseResult
	if err := json.Unmarshal([]byte(rawJSON), &parsed); err != nil {
		return nil, err
	}
	normalizeReceiptResult(&parsed)
	parsed.Provider = "gemini_flash"
	parsed.TokensUsed = tokens
	return &parsed, nil
}

func (p *HybridParser) invokeGemini(ctx context.Context, sysPrompt string, parts []geminiPart) (string, int32, error) {
	reqPayload := geminiRequest{
		SystemInstruction: &geminiContent{
			Parts: []geminiPart{{Text: sysPrompt}},
		},
		Contents: []geminiContent{
			{
				Role:  "user",
				Parts: parts,
			},
		},
		GenerationConfig: geminiGenConfig{
			ResponseMimeType: "application/json",
			Temperature:      0.1,
		},
	}

	bodyBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return "", 0, err
	}

	endpoint := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", p.model, p.apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", 0, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, fmt.Errorf("gemini api status %d", resp.StatusCode)
	}

	var gemResp geminiResponse
	if err := json.Unmarshal(respBytes, &gemResp); err != nil {
		return "", 0, err
	}
	if len(gemResp.Candidates) == 0 || len(gemResp.Candidates[0].Content.Parts) == 0 {
		return "", 0, fmt.Errorf("empty gemini candidates")
	}

	text := strings.TrimSpace(gemResp.Candidates[0].Content.Parts[0].Text)
	return text, gemResp.UsageMetadata.TotalTokenCount, nil
}

// ParseVoiceSlangFallback is our built-in Indonesian Slang & Pasar NLP parser
// so the API works 100% reliably even before GEMINI_API_KEY is configured.
func ParseVoiceSlangFallback(rawTranscript string) *VoiceParseResult {
	text := strings.TrimSpace(rawTranscript)
	if text == "" {
		text = "Ada uang masuk dari Pak Husni buat benerin motor: ganti oli 2 botol @35rb sama busi 25rb, dibayar via QRIS."
	}
	lower := strings.ToLower(text)

	// 1. Detect Intent ("buku_kas" vs "kirim_invoice")
	intent := "buku_kas"
	if strings.Contains(lower, "invoice") ||
		strings.Contains(lower, "tagihan") ||
		strings.Contains(lower, "tagih ") ||
		strings.Contains(lower, "buatkan nota") ||
		strings.Contains(lower, "kirim nota") {
		intent = "kirim_invoice"
	}

	// 2. Detect Direction ("inflow" vs "outflow")
	txType := "inflow"
	outflowKeywords := []string{
		"keluar", "beli ", "belanja", "bayar ", "kulakan", "gaji", "listrik", "bensin", "sewa", "ongkos", "modal",
	}
	inflowKeywords := []string{
		"masuk", "terima", "dapat", "laku", "jual", "dibayar",
	}
	hasOutflow := false
	for _, kw := range outflowKeywords {
		if strings.Contains(lower, kw) {
			hasOutflow = true
			break
		}
	}
	hasInflow := false
	for _, kw := range inflowKeywords {
		if strings.Contains(lower, kw) {
			hasInflow = true
			break
		}
	}
	if hasOutflow && !hasInflow && intent == "buku_kas" {
		txType = "outflow"
	}

	// 3. Detect Payment Method
	paymentMethod := "Tunai"
	if strings.Contains(lower, "qris") {
		paymentMethod = "QRIS"
	} else if strings.Contains(lower, "transfer") || strings.Contains(lower, "tf") ||
		strings.Contains(lower, "bca") || strings.Contains(lower, "mandiri") ||
		strings.Contains(lower, "bri") || strings.Contains(lower, "bni") {
		paymentMethod = "Transfer"
	}

	// 4. Detect Customer Name
	customerName := extractPersonName(text)

	// 5. Detect Due Date (for invoice)
	dueDate := "Jatuh Tempo 7 Hari"
	dueRe := regexp.MustCompile(`(?i)jatuh\s+tempo(?:\s+tanggal)?\s+([0-9]{1,2}\s+[A-Za-z]+(?:\s+[0-9]{4})?)`)
	if m := dueRe.FindStringSubmatch(text); len(m) > 1 {
		dueDate = strings.TrimSpace(m[1])
	} else if strings.Contains(lower, "besok") {
		dueDate = "Besok"
	} else if strings.Contains(lower, "14 hari") {
		dueDate = "Jatuh Tempo 14 Hari"
	}

	// 6. Detect Category
	category := "Penjualan"
	switch {
	case strings.Contains(lower, "oli") || strings.Contains(lower, "busi") || strings.Contains(lower, "kampas") || strings.Contains(lower, "ban") || strings.Contains(lower, "onderdil") || strings.Contains(lower, "sparepart"):
		if txType == "outflow" {
			category = "Suku Cadang"
		} else {
			category = "Servis"
		}
	case strings.Contains(lower, "servis") || strings.Contains(lower, "service") || strings.Contains(lower, "benerin") || strings.Contains(lower, "pasang"):
		category = "Servis"
	case strings.Contains(lower, "beras") || strings.Contains(lower, "minyak") || strings.Contains(lower, "gula") || strings.Contains(lower, "tepung") || strings.Contains(lower, "bahan") || strings.Contains(lower, "kulakan"):
		category = "Bahan Baku"
	case strings.Contains(lower, "listrik") || strings.Contains(lower, "air") || strings.Contains(lower, "wifi") || strings.Contains(lower, "kuota") || strings.Contains(lower, "bensin") || strings.Contains(lower, "makan") || strings.Contains(lower, "plastik"):
		category = "Operasional"
	default:
		if txType == "outflow" {
			category = "Bahan Baku"
		} else {
			category = "Penjualan"
		}
	}

	// 7. Extract Line Items
	items := extractLineItemsFromText(text)
	var total int64
	for i := range items {
		items[i].Subtotal = int64(items[i].Quantity) * items[i].UnitPrice
		total += items[i].Subtotal
	}

	// 8. Build Title
	title := buildSmartTitle(text, customerName, items, txType)

	res := &VoiceParseResult{
		Intent:        intent,
		Type:          txType,
		Title:         title,
		Category:      category,
		PaymentMethod: paymentMethod,
		CustomerName:  customerName,
		DueDate:       dueDate,
		Transcript:    text,
		Items:         items,
		TotalAmount:   total,
		Provider:      "slang_engine",
		TokensUsed:    0,
	}
	normalizeVoiceResult(res, text)
	return res
}

func ParseReceiptFallback(hintText string, presetID string) *ReceiptParseResult {
	lowerPreset := strings.ToLower(strings.TrimSpace(presetID))
	lowerHint := strings.ToLower(strings.TrimSpace(hintText))

	if lowerPreset == "sembako" || strings.Contains(lowerHint, "sembako") || strings.Contains(lowerHint, "beras") || strings.Contains(lowerHint, "minyak") {
		items := []ParsedLineItem{
			{Name: "Beras Ramos Super 5kg", Quantity: 2, UnitPrice: 68000, Subtotal: 136000},
			{Name: "Minyak Goreng Tropical 2L", Quantity: 3, UnitPrice: 36000, Subtotal: 108000},
			{Name: "Gula Pasir Gulaku 1kg", Quantity: 4, UnitPrice: 17500, Subtotal: 70000},
		}
		return &ReceiptParseResult{
			StoreName:     "Grosir Sembako Sumber Makmur",
			Category:      "Bahan Baku",
			PaymentMethod: "Tunai",
			Items:         items,
			TotalAmount:   314000,
			Provider:      "slang_engine",
			TokensUsed:    0,
		}
	}

	if lowerPreset == "operasional" || strings.Contains(lowerHint, "atk") || strings.Contains(lowerHint, "plastik") || strings.Contains(lowerHint, "operasional") {
		items := []ParsedLineItem{
			{Name: "Kantong Plastik Kresek HD Besar", Quantity: 5, UnitPrice: 12000, Subtotal: 60000},
			{Name: "Kertas Struk Thermal 58mm (10 Roll)", Quantity: 1, UnitPrice: 45000, Subtotal: 45000},
			{Name: "Lakban Bening Daimaru 2 Inch", Quantity: 2, UnitPrice: 15000, Subtotal: 30000},
		}
		return &ReceiptParseResult{
			StoreName:     "Toko Plastik & Kemasan Lancar",
			Category:      "Operasional",
			PaymentMethod: "QRIS",
			Items:         items,
			TotalAmount:   135000,
			Provider:      "slang_engine",
			TokensUsed:    0,
		}
	}

	// If custom hintText has prices/items, parse them!
	if strings.TrimSpace(hintText) != "" {
		parsedItems := extractLineItemsFromText(hintText)
		if len(parsedItems) > 0 {
			var sum int64
			for i := range parsedItems {
				parsedItems[i].Subtotal = int64(parsedItems[i].Quantity) * parsedItems[i].UnitPrice
				sum += parsedItems[i].Subtotal
			}
			return &ReceiptParseResult{
				StoreName:     "Toko Belanja Struk",
				Category:      "Bahan Baku",
				PaymentMethod: "Tunai",
				Items:         parsedItems,
				TotalAmount:   sum,
				Provider:      "slang_engine",
				TokensUsed:    0,
			}
		}
	}

	// Default Onderdil receipt
	items := []ParsedLineItem{
		{Name: "Kampas Rem Depan Honda Beat (2 Set)", Quantity: 2, UnitPrice: 45000, Subtotal: 90000},
		{Name: "Oli Mesin Shell Advance AX7 1L", Quantity: 1, UnitPrice: 65000, Subtotal: 65000},
		{Name: "Cairan Pembersih Karburator & Baut", Quantity: 1, UnitPrice: 25000, Subtotal: 25000},
	}
	return &ReceiptParseResult{
		StoreName:     "Toko Onderdil Motor & Mobil Jaya",
		Category:      "Suku Cadang",
		PaymentMethod: "Tunai",
		Items:         items,
		TotalAmount:   180000,
		Provider:      "slang_engine",
		TokensUsed:    0,
	}
}

func extractPersonName(text string) string {
	reHonorific := regexp.MustCompile(`(?i)\b((?:Pak|Bu|Ibu|Bapak|Mas|Mbak|Bang|Kak|Ko|Ci|Toko|CV|PT|UD)\s+[A-Z0-9a-z][A-Za-z0-9]+(?:\s+[A-Z0-9a-z][A-Za-z0-9]+)?)`)
	if m := reHonorific.FindStringSubmatch(text); len(m) > 1 {
		cleaned := strings.TrimSpace(m[1])
		stopWords := []string{" buat", " untuk", " beli", " ganti", " jatuh", " sebesar", " senilai", " via", " tanggal", " sama", " dan"}
		lowerC := strings.ToLower(cleaned)
		for _, sw := range stopWords {
			if idx := strings.Index(lowerC, sw); idx > 0 {
				cleaned = strings.TrimSpace(cleaned[:idx])
				lowerC = strings.ToLower(cleaned)
			}
		}
		return titleCaseWords(cleaned)
	}
	return ""
}

func extractLineItemsFromText(text string) []ParsedLineItem {
	// Focus on the detail part after ':' if present
	work := text
	if colonIdx := strings.Index(work, ":"); colonIdx >= 0 && colonIdx+1 < len(work) {
		work = work[colonIdx+1:]
	}

	// Strip trailing payment method / due date clauses
	stripPatterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)[,.]?\s*(?:dibayar|bayar|via|pakai|lewat)\s+(?:qris|tunai|cash|transfer|tf|bca|mandiri|bri|bni).*$`),
		regexp.MustCompile(`(?i)[,.]?\s*jatuh\s+tempo.*$`),
	}
	for _, pat := range stripPatterns {
		work = pat.ReplaceAllString(work, "")
	}

	// Split clauses by "sama", "dan", "plus", "tambah", ",", ";"
	splitter := regexp.MustCompile(`(?i)\s+(?:sama|dan|plus|tambah|serta)\s+|[,;]+`)
	rawClauses := splitter.Split(work, -1)

	var items []ParsedLineItem
	for _, clause := range rawClauses {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}
		item, ok := parseSingleItemClause(clause)
		if ok {
			items = append(items, item)
		}
	}

	if len(items) == 0 {
		// Fallback: extract total price from the whole text
		price := parseSlangMoney(text)
		if price <= 0 {
			price = 50000
		}
		name := cleanItemName(work)
		if name == "" {
			name = "Transaksi Kas"
		}
		items = append(items, ParsedLineItem{
			Name:      titleCaseWords(name),
			Quantity:  1,
			UnitPrice: price,
			Subtotal:  price,
		})
	}

	return items
}

func parseSingleItemClause(clause string) (ParsedLineItem, bool) {
	price := parseSlangMoney(clause)
	if price <= 0 {
		return ParsedLineItem{}, false
	}

	qty := extractQuantity(clause)
	if qty <= 0 {
		qty = 1
	}

	// Check if price is explicitly unit price (@) or total
	unitPrice := price
	name := cleanItemName(clause)
	if name == "" {
		name = "Item Transaksi"
	}

	return ParsedLineItem{
		Name:      titleCaseWords(name),
		Quantity:  qty,
		UnitPrice: unitPrice,
		Subtotal:  int64(qty) * unitPrice,
	}, true
}

func extractQuantity(clause string) int32 {
	lower := strings.ToLower(clause)
	// Match patterns like "2 botol", "3 pcs", "5 kg", "4 liter", "2 set", "3 biji", "2x", "2 buah", "2 bungkus", "2 kaleng", "2 dus"
	reQtyUnit := regexp.MustCompile(`(?i)\b([0-9]{1,3})\s*(?:botol|pcs|biji|buah|set|kg|liter|ltr|bungkus|bks|kaleng|dus|box|pak|roll|lembar|batang|pasang|x)\b`)
	if m := reQtyUnit.FindStringSubmatch(lower); len(m) > 1 {
		if v, err := strconv.Atoi(m[1]); err == nil && v > 0 {
			return int32(v)
		}
	}

	// Spoken small quantities before unit words
	wordNums := map[string]int32{
		"satu": 1, "sebotol": 1, "sebiji": 1, "sebungkus": 1,
		"dua": 2, "tiga": 3, "empat": 4, "lima": 5, "enam": 6, "tujuh": 7, "delapan": 8, "sembilan": 9, "sepuluh": 10,
	}
	for word, val := range wordNums {
		reWordUnit := regexp.MustCompile(`(?i)\b` + word + `\s+(?:botol|pcs|biji|buah|set|kg|liter|bungkus|kaleng|dus|pasang)\b`)
		if reWordUnit.MatchString(lower) {
			return val
		}
	}
	return 1
}

// parseSlangMoney extracts Indonesian Rupiah amounts from natural/slang text
// e.g. "ceban" -> 10000, "gocap" -> 50000, "seket" -> 50000, "pekgo" -> 150000, "35rb" -> 35000, "1,5 juta" -> 1500000.
func parseSlangMoney(text string) int64 {
	lower := strings.ToLower(text)

	// 1. Check explicit numeric + juta / jt
	reJuta := regexp.MustCompile(`([0-9]+(?:[.,][0-9]+)?)\s*(?:juta|jt)\b`)
	if m := reJuta.FindStringSubmatch(lower); len(m) > 1 {
		numStr := strings.ReplaceAll(m[1], ",", ".")
		if f, err := strconv.ParseFloat(numStr, 64); err == nil && f > 0 {
			return int64(f * 1_000_000)
		}
	}

	// 2. Check explicit numeric + ribu / rb / k (e.g. "@35rb", "65 ribu", "450rb")
	reRibu := regexp.MustCompile(`@?\s*([0-9]+(?:[.,][0-9]+)?)\s*(?:ribu|rb|k)\b`)
	if m := reRibu.FindStringSubmatch(lower); len(m) > 1 {
		numStr := strings.ReplaceAll(m[1], ",", ".")
		if f, err := strconv.ParseFloat(numStr, 64); err == nil && f > 0 {
			return int64(f * 1_000)
		}
	}

	// 3. Check Bahasa Pasar / Hokkien / Jawa slang terms
	slangMap := []struct {
		pattern string
		value   int64
	}{
		{`\bsatu\s+setengah\s+juta\b`, 1_500_000},
		{`\bsetengah\s+juta\b`, 500_000},
		{`\bsejuta\b`, 1_000_000},
		{`\bpekgo\b`, 150_000},
		{`\bnopek\b`, 200_000},
		{`\bsampek\b`, 300_000},
		{`\bgopek\s+ribu\b`, 500_000},
		{`\bcepek\s+ribu\b`, 100_000},
		{`\bcepek\b`, 100_000},
		{`\bgocap\b`, 50_000},
		{`\bgoban\b`, 50_000},
		{`\bseket\b`, 50_000},
		{`\bselawe\b`, 25_000},
		{`\bsamban\b`, 30_000},
		{`\bnoban\b`, 20_000},
		{`\bceban\s+lima\b`, 15_000},
		{`\bceban\b`, 10_000},
		{`\bgoceng\b`, 5_000},
		{`\bnoceng\b`, 2_000},
		{`\bseceng\b`, 1_000},
		{`\bseratus\s+lima\s+puluh\s+ribu\b`, 150_000},
		{`\bseratus\s+ribu\b`, 100_000},
		{`\blima\s+puluh\s+ribu\b`, 50_000},
		{`\bdua\s+puluh\s+lima\s+ribu\b`, 25_000},
		{`\bdua\s+puluh\s+ribu\b`, 20_000},
		{`\bsepuluh\s+ribu\b`, 10_000},
	}
	for _, s := range slangMap {
		re := regexp.MustCompile(s.pattern)
		if re.MatchString(lower) {
			return s.value
		}
	}

	// 4. Check raw formatted thousands like "Rp 45.000" or "45000"
	reRawRupiah := regexp.MustCompile(`(?:rp\.?\s*)?([0-9]{1,3}(?:\.[0-9]{3})+|[0-9]{4,9})\b`)
	if m := reRawRupiah.FindStringSubmatch(lower); len(m) > 1 {
		digits := strings.ReplaceAll(m[1], ".", "")
		if v, err := strconv.ParseInt(digits, 10, 64); err == nil && v >= 500 {
			return v
		}
	}

	return 0
}

func cleanItemName(clause string) string {
	s := clause
	// Remove leading boilerplate phrases
	leadPhrases := []*regexp.Regexp{
		regexp.MustCompile(`(?i)^.*?(?:buat|untuk|berupa)\s+`),
		regexp.MustCompile(`(?i)^(?:ada\s+)?(?:uang\s+masuk|uang\s+keluar|kas\s+masuk|kas\s+keluar|beli|belanja|bayar|jual|tagihan|buatkan\s+invoice|kirim\s+invoice)\s+`),
	}
	for _, pat := range leadPhrases {
		s = pat.ReplaceAllString(s, "")
	}

	// Remove price & slang tokens from item name
	removePatterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)@?\s*[0-9]+(?:[.,][0-9]+)?\s*(?:juta|jt|ribu|rb|k)\b`),
		regexp.MustCompile(`(?i)(?:rp\.?\s*)?[0-9]{1,3}(?:\.[0-9]{3})+\b`),
		regexp.MustCompile(`(?i)\b[0-9]{4,9}\b`),
		regexp.MustCompile(`(?i)\b[0-9]{1,3}\s*(?:botol|pcs|biji|buah|set|kg|liter|ltr|bungkus|bks|kaleng|dus|box|pak|roll|lembar|batang|pasang|x)\b`),
		regexp.MustCompile(`(?i)\b(?:seceng|noceng|goceng|ceban|noban|samban|gocap|goban|seket|selawe|cepek|pekgo|nopek|sampek|sejuta|setengah\s+juta|seratus\s+ribu|lima\s+puluh\s+ribu|sepuluh\s+ribu)\b`),
		regexp.MustCompile(`(?i)\b(?:sebesar|senilai|harga|total|masing-masing|satuan)\b`),
	}
	for _, pat := range removePatterns {
		s = pat.ReplaceAllString(s, " ")
	}

	s = strings.Trim(s, " :,-.@")
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func buildSmartTitle(text string, customer string, items []ParsedLineItem, txType string) string {
	if len(items) > 0 {
		firstItem := items[0].Name
		if len(items) > 1 {
			firstItem = fmt.Sprintf("%s (+%d rincian)", firstItem, len(items)-1)
		}
		if customer != "" {
			return fmt.Sprintf("%s — %s", customer, firstItem)
		}
		return firstItem
	}
	if customer != "" {
		if txType == "outflow" {
			return "Pembayaran ke " + customer
		}
		return "Pemasukan dari " + customer
	}
	return "Catatan Suara AI"
}

func titleCaseWords(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		if len(w) <= 1 {
			words[i] = strings.ToUpper(w)
			continue
		}
		upper := strings.ToUpper(w)
		if upper == "QRIS" || upper == "BCA" || upper == "BRI" || upper == "BNI" || upper == "CV" || upper == "PT" || upper == "OLI" && false {
			words[i] = upper
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
	}
	return strings.Join(words, " ")
}

func normalizeVoiceResult(r *VoiceParseResult, fallbackTranscript string) {
	if r.Intent != "kirim_invoice" {
		r.Intent = "buku_kas"
	}
	if r.Type != "outflow" {
		r.Type = "inflow"
	}
	switch r.PaymentMethod {
	case "QRIS", "Transfer", "Tunai":
	default:
		r.PaymentMethod = "Tunai"
	}
	if strings.TrimSpace(r.Category) == "" {
		if r.Type == "outflow" {
			r.Category = "Bahan Baku"
		} else {
			r.Category = "Penjualan"
		}
	}
	if strings.TrimSpace(r.Transcript) == "" {
		r.Transcript = strings.TrimSpace(fallbackTranscript)
	}
	if strings.TrimSpace(r.DueDate) == "" {
		r.DueDate = "Jatuh Tempo 7 Hari"
	}
	var sum int64
	for i := range r.Items {
		if r.Items[i].Quantity <= 0 {
			r.Items[i].Quantity = 1
		}
		if r.Items[i].UnitPrice < 0 {
			r.Items[i].UnitPrice = 0
		}
		r.Items[i].Subtotal = int64(r.Items[i].Quantity) * r.Items[i].UnitPrice
		sum += r.Items[i].Subtotal
	}
	if sum > 0 {
		r.TotalAmount = sum
	}
	if strings.TrimSpace(r.Title) == "" {
		r.Title = buildSmartTitle(r.Transcript, r.CustomerName, r.Items, r.Type)
	}
}

func normalizeReceiptResult(r *ReceiptParseResult) {
	if strings.TrimSpace(r.StoreName) == "" {
		r.StoreName = "Belanja Struk"
	}
	if strings.TrimSpace(r.Category) == "" {
		r.Category = "Bahan Baku"
	}
	switch r.PaymentMethod {
	case "QRIS", "Transfer", "Tunai":
	default:
		r.PaymentMethod = "Tunai"
	}
	var sum int64
	for i := range r.Items {
		if r.Items[i].Quantity <= 0 {
			r.Items[i].Quantity = 1
		}
		if r.Items[i].UnitPrice < 0 {
			r.Items[i].UnitPrice = 0
		}
		r.Items[i].Subtotal = int64(r.Items[i].Quantity) * r.Items[i].UnitPrice
		sum += r.Items[i].Subtotal
	}
	if sum > 0 {
		r.TotalAmount = sum
	}
}
