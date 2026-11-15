# UcapKas Backend (`ucapkas_backend`) — Go 1.27 + PostgreSQL 16

Backend finansial & AI proxy untuk aplikasi mobile **UcapKas** (`ucapkas_app`), dirancang ringan (<250 MB RAM total) dan berstandar keamanan tinggi (UUIDv7, SHA-256 Refresh Token Rotation + Reuse Attack Detection, verifikasi kriptografi Google `id_token`).

## Versi & Stack
- **Go:** `go 1.27.2` (`Dockerfile` menggunakan `golang:1.27-alpine`, dilengkapi stage `dev` dengan **Air Hot-Reload** & stage `prod` image ~20 MB)
- **HTTP Router:** `go-chi/chi/v5` + Security Middleware (CORS, Rate-Limit IP, Security Headers, Body Max 1MB)
- **Database:** PostgreSQL 16 Alpine + `jackc/pgx/v5` + `sqlc` + migrasi otomatis `pressly/goose/v3`

## Cara Menjalankan Lokal (Mode Dev dengan Docker Hot-Reload)

1. **Jalankan PostgreSQL + Go API (Hot-Reload via `air`):**
   ```bash
   cd /Users/apple/Documents/project/prb/artech/ucapkas_backend
   docker compose -f docker-compose.dev.yml up --build
   ```
   - Setiap kali Anda menyimpan perubahan file `.go` atau `.sql` di laptop, container `ucapkas_api_dev` akan otomatis *rebuild & restart* dalam hitungan milidetik.
   - Port `8080` terbuka untuk iOS Simulator (`http://127.0.0.1:8080`), Android Emulator (`http://10.0.2.2:8080`), maupun HP fisik di Wi-Fi yang sama (`http://<IP_MAC_ANDA>:8080`).
   - Cek status kesehatan di `http://127.0.0.1:8080/healthz`.

2. **Alternatif: Jalankan Go Langsung di Host Mac (Tanpa Container API):**
   ```bash
   docker compose up -d postgres
   go run ./cmd/api
   ```

3. **Menjalankan Aplikasi Flutter (`ucapkas_app`) Terhubung ke Backend Lokal:**
   - **Mode Uji Coba Cepat End-to-End di Simulator (Tanpa Setup SHA-1 Play Console):**
     ```bash
     cd ../ucapkas_app
     /Users/apple/Dev/flutter/bin/flutter run --dart-define=USE_DEV_MOCK_GOOGLE=true
     ```
   - **Dengan Akun Google Asli (Simulator/Device):**
     Isi `GOOGLE_CLIENT_IDS` di `.env` backend, lalu jalankan:
     ```bash
     cd ../ucapkas_app
     /Users/apple/Dev/flutter/bin/flutter run --dart-define=GOOGLE_SERVER_CLIENT_ID=<WEB_CLIENT_ID>.apps.googleusercontent.com
     ```

## Mode Production (VPS 1 GB RAM)
```bash
docker compose -f docker-compose.yml up -d --build
```

## Menjalankan Unit & Security Tests
```bash
go test -v -count=1 ./...
```
