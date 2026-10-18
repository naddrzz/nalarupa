<p align="center">
  <img src="app/assets/brand/icon-256.png" width="128" alt="Logo Nalarupa">
</p>

<h1 align="center">Nalarupa</h1>

<p align="center">
  Editor desain kanvas tak-terbatas dengan agen AI.<br>
  Core <b>Rust</b>, UI <b>GPU-native</b> (GPUIX: React di atas GPUI) — tanpa Electron, tanpa webview.
</p>

<p align="center">
  <a href="https://github.com/naddrzz/nalarupa/releases/latest">Unduh</a> ·
  <a href="#fitur">Fitur</a> ·
  <a href="#instalasi">Instalasi</a> ·
  <a href="#pengembangan">Pengembangan</a>
</p>

![Dua agen mendesain hero landing page berdampingan](docs/screenshots/agent.png)

*Nalarupa* berasal dari *nalar* (pikiran) dan *rupa* (wujud): ide yang diwujudkan. Logonya memadukan gunungan
wayang yang sekaligus mata pena (pen tool), bunga kawung, dan tekstur parang batik.

## Fitur

### Agen desain AI

![Kursor agen bekerja di kanvas](docs/screenshots/agent-live.png)

- **Chat desain di panel kanan**: tulis apa yang diinginkan, agen menyusun desainnya langsung di kanvas.
- **Desain muncul selagi agen menulis**: setiap langkah tampil seketika, lalu di-commit sebagai satu langkah undo.
- **Kursor agen** bernama dan berwarna terbang ke node yang disentuh, dengan kilatan sorot dan efek
  *Generating* pada frame yang sedang dikerjakan.
- **Agen paralel 1–4×**: bagi pekerjaan (*Split*) atau buat beberapa varian berdampingan (*Side by side*).
- **Let it cook**: setelah satu desain selesai, agen terus membuat variasi (layout atau gaya) sendiri.
- **Pilih model** (Opus 5.5, Sonnet 5.5, Haiku 5.5, Fable 5.1) dan **mode Fast** bila modelnya mendukung.
- Dua backend: **Claude Code** (memakai login Claude Code yang sudah ada) atau **Claude API** (API key).
- Riwayat chat tersimpan, thinking yang bisa dibuka, screenshot inline, rencana kerja (TODO), antrean pesan,
  kartu izin sebelum agen menjalankan tool di luar kanvas.
- **AI Pen**: ilustrasi SVG digambar bertahap ke dalam frame.

### Kanvas dan editor

![Inspector Design pada desain buatan agen](docs/screenshots/design.png)

- Kanvas tak-terbatas: pan (Space, tombol tengah, Hand), zoom (Ctrl+scroll, Shift+1 fit, Shift+2 seleksi).
- Frame, Rectangle, Ellipse, Text, **Pen tool** (klik = sudut, seret = kurva, edit titik dengan dobel-klik).
- Seleksi: klik → anak frame, dobel-klik → masuk, Ctrl+klik → terdalam; marquee; Shift untuk menambah.
- **Snapping** 5 px ke induk dan sibling dengan garis bantu, Alt+hover mengukur jarak.
- **Auto layout** (flexbox: Hug/Fill, gap, padding, align); seret di dalam layout untuk mengurutkan ulang,
  lepas di frame lain untuk memindahkan.
- Group (Ctrl+G), duplikat (Ctrl+D, Alt-drag), align (Alt+W/A/S/D/H/V), resize dengan rasio/pusat, rotasi,
  radius sudut langsung di kanvas, undo/redo, salin/tempel (termasuk teks dan SVG).
- **Tempel dari Figma**: frame, auto layout, teks, vektor, komponen, dan variabel ikut terbawa.
- **Layers**: rename, drag untuk mengurutkan/memindahkan, sembunyikan, hover menyorot kanvas.
- **Inspector**: posisi, ukuran, layout, teks, fill, stroke, radius, efek (bayangan, blur), komponen;
  geser label untuk mengubah angka, ↑/↓ untuk langkah kecil.
- Fill: warna, gradien, **mesh gradient**, gambar, **shader** (GLSL), ikon (Lucide, Feather, Material Symbols,
  Phosphor) dan Google Fonts yang diunduh sekali lalu di-cache.
- Komponen dan instance, variabel dan tema, serta node **script** yang membangkitkan desain dari JavaScript.

### Slides dan presentasi

![Template slide deck di tab Slides](docs/screenshots/slides.png)

- Setiap frame tingkat atas adalah slide; tab **Slides** menampilkan thumbnail dan urutannya.
- **Present mode** layar penuh dengan navigasi keyboard.
- Ekspor seluruh deck ke **satu PDF** multi-halaman (teks tetap bisa dipilih).

![Present mode](docs/screenshots/present.png)

### Ekspor dan integrasi

- Ekspor node ke **PNG, JPEG, WebP, SVG, HTML+CSS**, dan deck ke **PDF**.
- **MCP server** bawaan (`http://127.0.0.1:47700/mcp`) untuk agen eksternal: Claude Code, Cursor, Codex.
  Tombol *Connect* menulis konfigurasinya hanya saat diklik.
- CLI headless `nalarupa-cli` untuk render/ekspor tanpa UI.
- Format dokumen `.nala` (JSON yang mudah dibaca); dokumen `.pen` juga bisa dibuka.

### Aksesibilitas

- Semua kontrol bisa dicapai dengan Tab dan dipakai dengan Enter/Space, dengan cincin fokus yang jelas.
- Setiap kontrol punya nama untuk pembaca layar (Windows UI Automation / Narrator).
- Kontras teks memenuhi WCAG AA; animasi dimatikan saat OS meminta *reduced motion*.

![Layar Home](docs/screenshots/home.png)

## Instalasi

**Windows 10/11 x64**

1. Unduh `nalarupa-windows-x64.zip` dari [Releases](https://github.com/naddrzz/nalarupa/releases/latest).
2. Ekstrak ke folder mana saja (misalnya `%LOCALAPPDATA%\Programs\Nalarupa`). Keempat file harus tetap
   bersama: `nalarupa.exe`, `nalarupa_core.dll`, `gpuix-native.win32-x64-msvc.node`, `nalarupa.ico`.
3. Jalankan `nalarupa.exe`.

Untuk agen AI, pasang dan login [Claude Code](https://claude.com/claude-code) (`claude` ada di PATH), atau isi
Anthropic API key di pengaturan agen. Data aplikasi (pengaturan, riwayat chat, cache font) ada di
`%LOCALAPPDATA%\Nalarupa`.

macOS dan Linux bisa dijalankan dari source (lihat di bawah); binary rilis saat ini hanya untuk Windows.

## Pengembangan

Prasyarat: [Rust](https://rustup.rs) stable dan [Bun](https://bun.sh) ≥ 1.3.

```bash
cd core && cargo build --release
```

```bash
cd app && bun install && bun run dev
```

`bun run dev` membuka jendela dengan hot reload (`bun --hot app.tsx`); `bun run start` tanpa hot reload.

### Build binary

```bash
cd app && bun run build
```

Hasilnya di `app/dist/`: `nalarupa.exe` ber-ikon Nalarupa, `nalarupa_core.dll`, addon GPUI, dan
`nalarupa.ico`. Untuk rilis, zip keempat file itu.

### Arsitektur

```
app/  (Bun + @gpuix/react)                core/  (Rust, nalarupa_core.dll)
┌──────────────────────────────┐  bun:ffi  ┌───────────────────────────────────────────────┐
│ Home · Editor · Present      │──────────▶│ nr_call(json) → json   nr_render(...) → RGBA  │
│ Layers/Slides · Inspector    │           │ doc.rs      model dokumen (lossless)          │
│ Agent chat · Canvas (<img>)  │◀── RGBA ──│ resolve.rs  komponen/ref, variabel + tema     │
│ setImagePixels per frame     │           │ layout.rs   flexbox auto layout               │
└──────────────────────────────┘           │ text.rs     rustybuzz + UAX #14, Google Fonts │
                                           │ svg.rs → usvg 0.47 → raster.rs (vello_cpu)    │
 Claude Code / Cursor / Codex              │ render.rs   view cache + dirty rect + culling │
   ── HTTP MCP 127.0.0.1:47700/mcp ───────▶│ script.rs   `execute` di QuickJS-NG (rquickjs)│
                                           │ editor.rs   snap, marquee, layout, pen tool   │
                                           │ mcp.rs      MCP streamable-HTTP + installer   │
                                           │ agent.rs    Claude API loop │ Claude Code CLI │
                                           │ html.rs     ekspor HTML+CSS, PDF (krilla 0.8) │
                                           └───────────────────────────────────────────────┘
```

Panduan untuk agen ada di `core/guide/*.md` (skema dokumen, API `execute`, komponen, slides, desain, kode).

### Pengecekan

```bash
cd core && cargo test
```

```bash
cd app && bun run typecheck && bun run smoke && bun run e2e
```

```bash
cd app && bun run editor-check && bun run editor-e2e
```

```bash
cd app && bun run a11y-check && bun run motion-check && bun run window-check
```

- `e2e` membuka jendela GPU di latar belakang: Home → template slides → menggambar → agen eksternal lewat MCP
  menambah slide → Present.
- `editor-e2e` menguji interaksi editor (snapping, marquee, group, auto layout, rename, pen tool).
- `a11y-check` membaca pohon Windows UI Automation dan gagal bila ada kontrol tanpa nama; `motion-check`
  memastikan animasi berhenti saat *reduced motion*; `window-check` memastikan jendela muat di layar dan
  frame-nya membawa ikon Nalarupa (`bun run window-check dist/nalarupa.exe` untuk exe hasil build).
- `exe-check` menjalankan binary hasil build; `model-check` menjalankan dua giliran agen sungguhan.
- `bun run shots` membuat ulang screenshot README di `docs/screenshots/`.

Kinerja kanvas:

```bash
cd core && cargo run --release --example bench -- file.nala 1600 1000
```

Logo di `app/assets/brand/` (`nalarupa.svg`, versi 16–48 px `nalarupa-small.svg`). PNG dan `nalarupa.ico`
dibuat ulang dengan:

```bash
cd core && cargo run --release --example icon -- ../app/assets/brand/nalarupa.svg ../app/assets/brand/nalarupa-small.svg ../app/assets/brand
```

## Menghubungkan agen eksternal

Saat aplikasi berjalan, MCP tersedia di `http://127.0.0.1:47700/mcp` (port berikutnya bila terpakai).
Tool-nya: `get_editor_state`, `read_guide`, `get_style`, `execute`, `open_document`. Contoh Claude Code:

```bash
claude mcp add --transport http --scope user nalarupa http://127.0.0.1:47700/mcp
```

## Batasan yang diketahui

- Background blur saat ekspor (lewat resvg) hanya mengaburkan isi frame tingkat atas yang sama.
- `execute` dibatasi 30 s, 64 MB memori, dan 512 KB stack.
- Clipboard sistem hanya di Windows; di macOS/Linux salin/tempel hanya di dalam aplikasi.
- Generasi gambar AI/stock memasang foto placeholder; belum ada backend generasi gambar.
- PDF memakai krilla 0.8: 1 px = 1 pt, filter (blur, bayangan) masuk sebagai bitmap.
- WebP selalu lossless; JPEG tanpa alpha (area transparan menjadi putih).
- API key Claude disimpan apa adanya di `%LOCALAPPDATA%\Nalarupa\settings.json`.

`core/vendor/resvg` adalah resvg 0.47.0 (Apache-2.0 OR MIT) dengan dua perbaikan *layer clamp*; ikon UI dari
[Lucide](https://lucide.dev) (ISC, `app/assets/icons/LICENSE`).
