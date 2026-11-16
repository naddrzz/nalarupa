package main

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/naddrzz/nalarupa/core-go/agent"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "nalarupa-abi")
	if err != nil {
		panic(err)
	}
	os.Setenv("NALARUPA_DATA", dir)
	engine.Global().Offline()
	engine.Handle("abi_panic", func(*engine.Engine, jsonv.Value) (jsonv.Value, error) { panic("boom") })
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// cstring reads the NUL-terminated C string at p.
func cstring(p unsafe.Pointer) string {
	n := 0
	for *(*byte)(unsafe.Add(p, n)) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(p), n))
}

// raw calls nr_call with req's bytes as the app does and returns the answer's text, freed with nr_free.
func raw(t *testing.T, req []byte) string {
	t.Helper()
	out, ok := tryRaw(req)
	if !ok {
		t.Fatal("nr_call returned null")
	}
	return out
}

// tryRaw is raw for goroutines other than the test's: false when nr_call returned null.
func tryRaw(req []byte) (string, bool) {
	var ptr unsafe.Pointer
	if len(req) > 0 {
		ptr = unsafe.Pointer(&req[0])
	}
	p := nr_call(ptr, uint32(len(req)))
	if p == nil {
		return "", false
	}
	defer nr_free(p)
	return cstring(p), true
}

// call runs a command and returns its data, failing on an error answer.
func call(t *testing.T, req string) jsonv.Value {
	t.Helper()
	out := raw(t, []byte(req))
	v, err := jsonv.Parse([]byte(out))
	if err != nil {
		t.Fatalf("%s: answer is not JSON: %v: %q", req, err, out)
	}
	if !jsonv.IsTrue(jsonv.Get(v, "ok")) {
		t.Fatalf("%s: %s", req, out)
	}
	return jsonv.Get(v, "data")
}

func TestEnvelope(t *testing.T) {
	for _, c := range []struct {
		req  []byte
		want string
	}{
		{[]byte(`{"cmd": "no_such_command"}`), `{"ok":false,"error":"unknown command 'no_such_command'"}`},
		{[]byte(`{"cmd": "node"}`), `{"ok":false,"error":"id required"}`},
		{[]byte(`{"cmd": `), `{"ok":false,"error":"bad request: EOF while parsing a value at line 1 column 8"}`},
		{nil, `{"ok":false,"error":"bad request: EOF while parsing a value at line 1 column 0"}`},
		{[]byte("{\"cmd\": \"st\x00ate\"}"), `{"ok":false,"error":"bad request: control character (\\u0000-\\u001F) found while parsing a string at line 1 column 12"}`},
		{[]byte(`{"cmd": "abi_panic"}`), `{"ok":false,"error":"internal error (panic)"}`},
	} {
		if got := raw(t, c.req); got != c.want {
			t.Errorf("%q:\n got %s\nwant %s", c.req, got, c.want)
		}
	}
	// Nothing is left locked by the panic, and the length counts: bytes past it are not read.
	req := []byte(`{"cmd": "state"}garbage`)
	if !strings.HasPrefix(raw(t, req[:16]), `{"ok":true,"data":{"version":`) {
		t.Error("state after a panic, with a length shorter than the buffer")
	}
	// A NUL in the answer's text is escaped, so the C string is the whole answer.
	call(t, `{"cmd": "new", "template": "empty"}`)
	id := jsonv.StrOr(jsonv.Get(call(t, `{"cmd": "insert", "node": {"type": "rectangle", "name": "a\u0000b", "width": 4, "height": 4}}`), "id"))
	if out := raw(t, []byte(`{"cmd": "layers"}`)); !strings.Contains(out, `"name":"a\u0000b"`) || !strings.HasSuffix(out, "]}") {
		t.Errorf("layers: %s", out)
	}
	call(t, fmt.Sprintf(`{"cmd": "delete", "ids": [%q]}`, id))
}

// init starts the MCP server once, on a live port in its range, and reports it.
func TestInitStartsTheMCPServer(t *testing.T) {
	v := call(t, `{"cmd": "init"}`)
	port, _ := jsonv.F64(jsonv.Get(v, "mcpPort"))
	if jsonv.StrOr(jsonv.Get(v, "dataDir")) != os.Getenv("NALARUPA_DATA") || port < 47700 || port >= 47720 ||
		jsonv.StrOr(jsonv.Get(v, "mcpUrl")) != fmt.Sprintf("http://127.0.0.1:%d/mcp", int(port)) {
		t.Fatalf("init: %s", jsonv.Marshal(v))
	}
	res, err := http.Post(jsonv.StrOr(jsonv.Get(v, "mcpUrl")), "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(b) != `{"jsonrpc":"2.0","id":7,"result":{}}` {
		t.Errorf("ping: %d %s", res.StatusCode, b)
	}
	if again := call(t, `{"cmd": "init"}`); !jsonv.Equal(jsonv.Get(again, "mcpPort"), jsonv.Get(v, "mcpPort")) {
		t.Errorf("a second init moved the server: %s", jsonv.Marshal(again))
	}
	if p, _ := jsonv.F64(jsonv.Get(call(t, `{"cmd": "state"}`), "mcpPort")); p != port {
		t.Errorf("state reports port %v", p)
	}
}

// The agent's commands are dispatched as lib.rs does, with its errors.
func TestAgentCommands(t *testing.T) {
	for _, c := range []struct{ req, want string }{
		{`{"cmd": "agent_stop"}`, `{"ok":true,"data":true}`},
		{`{"cmd": "chat_flush"}`, `{"ok":true,"data":true}`},
		{`{"cmd": "chat_list"}`, `{"ok":true,"data":{"current":null,"chats":[]}}`},
		{`{"cmd": "chat_load"}`, `{"ok":false,"error":"id required"}`},
		{`{"cmd": "chat_delete", "id": "a-b"}`, `{"ok":false,"error":"invalid chat id: a-b"}`},
		{`{"cmd": "agent_permission", "id": "x"}`, `{"ok":false,"error":"answer required: allow, deny or auto"}`},
		{`{"cmd": "agent_permission", "id": "x", "answer": "allow"}`, `{"ok":false,"error":"This permission request is no longer waiting."}`},
		{`{"cmd": "agent_instructions", "backend": "x"}`, `{"ok":false,"error":"unknown backend 'x': api, claude-code or provider"}`},
		{`{"cmd": "mcp_install", "client": "vscode"}`, `{"ok":false,"error":"unknown client 'vscode' (claude-code, cursor, codex)"}`},
		{`{"cmd": "provider_test"}`, `{"ok":false,"error":"id required"}`},
		{`{"cmd": "provider_test", "id": "nope"}`, `{"ok":false,"error":"unknown provider"}`},
		{`{"cmd": "agent_reset"}`, `{"ok":true,"data":true}`},
	} {
		if got := raw(t, []byte(c.req)); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.req, got, c.want)
		}
	}
	if got := jsonv.StrOr(call(t, `{"cmd": "agent_instructions", "backend": "provider"}`)); got != agent.Instructions("api") {
		t.Error("a provider gets the API instructions")
	}
}

// A saved null list stays null for Rust's `set_setting`, which removes the key; it never becomes [].
func TestRemoveProviderKeepsANullList(t *testing.T) {
	e := engine.Global()
	e.Locked(func() { e.Settings.Set("providers", nil) })
	call(t, `{"cmd": "provider_remove", "id": "x"}`)
	e.Locked(func() {
		if v, had := e.Settings.Get("providers"); had {
			t.Errorf("providers is %s, want it removed as Rust does", jsonv.Marshal(v))
		}
	})
}

func TestProviders(t *testing.T) {
	id := jsonv.StrOr(call(t, `{"cmd": "provider_save", "provider": {"name": " Local ", "format": "chat", "base_url": "http://127.0.0.1:1/v1", "api_key": "sk-1", "models": [" a ", "", 3]}}`))
	if len(id) != 9 || id[0] != 'p' {
		t.Fatalf("id %q", id)
	}
	call(t, fmt.Sprintf(`{"cmd": "provider_save", "provider": {"id": %q, "format": "chat", "base_url": "http://127.0.0.1:1/v1", "api_key": %q, "vision": false}}`, id, engine.Mask))
	got := string(jsonv.Marshal(jsonv.Get(call(t, `{"cmd": "settings_get"}`), "providers")))
	want := fmt.Sprintf(`[{"id":%q,"name":"http://127.0.0.1:1/v1","format":"chat","base_url":"http://127.0.0.1:1/v1","api_key":%q,"models":[],"vision":false}]`, id, engine.Mask)
	if got != want {
		t.Errorf("providers:\n got %s\nwant %s", got, want)
	}
	if k := jsonv.Get(jsonv.Items(jsonv.Get(engine.Global().Settings, "providers"))[0], "api_key"); k != "sk-1" {
		t.Errorf("the masked key replaced the saved one: %v", k)
	}
	if out := raw(t, []byte(`{"cmd": "provider_save", "provider": {"format": "chat"}}`)); !strings.Contains(out, "needs a base URL") {
		t.Error(out)
	}
	if out := raw(t, []byte(`{"cmd": "provider_save", "provider": {"base_url": "x", "format": "soap"}}`)); !strings.Contains(out, `Unknown format \"soap\"`) {
		t.Error(out)
	}
	call(t, fmt.Sprintf(`{"cmd": "provider_remove", "id": %q}`, id))
	if n := len(jsonv.Items(jsonv.Get(call(t, `{"cmd": "settings_get"}`), "providers"))); n != 0 {
		t.Errorf("%d providers left", n)
	}
}

// render calls nr_render and returns the pointer and a copy of the w·h·4 bytes.
func render(t *testing.T, w, h uint32, bg uint32, solo string, flags uint32) (unsafe.Pointer, []byte) {
	t.Helper()
	var sp unsafe.Pointer
	if solo != "" {
		sp = unsafe.Pointer(unsafe.StringData(solo))
	}
	p := nr_render(w, h, 0, 0, 1, bg, sp, uint32(len(solo)), flags)
	if p == nil {
		t.Fatal("nr_render returned null")
	}
	return p, append([]byte(nil), unsafe.Slice((*byte)(p), w*h*4)...)
}

func pixel(px []byte, w, x, y int) [4]byte {
	i := (y*w + x) * 4
	return [4]byte(px[i : i+4])
}

func TestRender(t *testing.T) {
	call(t, `{"cmd": "new", "template": "empty"}`)
	id := jsonv.StrOr(jsonv.Get(call(t, `{"cmd": "insert", "node": {"type": "rectangle", "x": 0, "y": 0, "width": 4, "height": 4, "fill": "#FF0000"}}`), "id"))
	bg := [4]byte{0x11, 0x22, 0x33, 0xff}
	red := [4]byte{0xff, 0, 0, 0xff}
	p, px := render(t, 16, 8, 0x112233, "", 1)
	if pixel(px, 16, 1, 1) != red || pixel(px, 16, 10, 5) != bg {
		t.Errorf("16x8: %v %v", pixel(px, 16, 1, 1), pixel(px, 16, 10, 5))
	}
	// A smaller frame reuses the buffer; a bigger one may move it, and it is the right size.
	if q, _ := render(t, 8, 8, 0x112233, "", 0); q != p {
		t.Error("a smaller render moved the buffer")
	}
	_, px = render(t, 64, 48, 0x112233, id, 1)
	if pixel(px, 64, 2, 2) != red || pixel(px, 64, 63, 47)[3] != 0xff {
		t.Errorf("64x48 solo: %v %v", pixel(px, 64, 2, 2), pixel(px, 64, 63, 47))
	}
	// Many renders of one size keep one buffer.
	p, _ = render(t, 64, 48, 0x112233, "", 1)
	for range 300 {
		if q, _ := render(t, 64, 48, 0x112233, "", 0); q != p {
			t.Fatal("a render of the same size moved the buffer")
		}
	}
	if pixels.size != 64*48*4 {
		t.Errorf("buffer of %d bytes", pixels.size)
	}
}

func TestRenderPanicIsNull(t *testing.T) {
	e := engine.Global()
	var d = e.Doc
	// The next render syncs a missing document.
	e.Locked(func() { e.Doc = nil; e.Version++ })
	defer e.Locked(func() { e.Doc = d })
	if p := nr_render(8, 8, 0, 0, 1, 0, nil, 0, 1); p != nil {
		t.Error("a panicking render gave pixels")
	}
	// The lock was released.
	e.Locked(func() {})
}

// Renders and calls from several threads while an execute runs and the document changes: no deadlock, no race
// (run with -race).
func TestConcurrentCallsAndRenders(t *testing.T) {
	concurrently(t, func(int) string { return "" })
}

// The same while image downloads run and land: the engine is online against a local server that answers each
// image after a delay, so the downloads deliver their results (under the engine lock) during the calls.
func TestConcurrentCallsAndRendersWhileDownloading(t *testing.T) {
	var red bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := range img.Pix {
		img.Pix[i] = []byte{255, 0, 0, 255}[i%4]
	}
	png.Encode(&red, img)
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		served.Add(1)
		w.Write(red.Bytes())
	}))
	defer srv.Close()
	e := engine.Global()
	e.Locked(func() { e.Assets.Online = true })
	defer e.Locked(func() { e.Assets.Online = false })
	urls := func(k int) string { return fmt.Sprintf("%s/%d.png", srv.URL, k) }
	concurrently(t, urls)
	// A frame of every inserted image asks for any download not started yet.
	nr_render(700, 48, 0, 0, 1, 0xffffff, nil, 0, 1)
	// Every download lands, and none is left running.
	until := time.Now().Add(30 * time.Second)
	loading := func() (n int) {
		e.Locked(func() {
			for k := range 20 {
				if e.Assets.Loading(urls(k)) {
					n++
				}
			}
		})
		return n
	}
	for loading() > 0 && time.Now().Before(until) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := loading(); n > 0 || served.Load() < 20 {
		t.Errorf("%d downloads still running, %d served", n, served.Load())
	}
}

// concurrently runs renders, executes, inserts (with an image fill from imageURL(k) when it is not "") and polls on
// four goroutines against a fresh empty document, then checks every insert landed.
func concurrently(t *testing.T, imageURL func(k int) string) {
	call(t, `{"cmd": "new", "template": "empty"}`)
	var wg sync.WaitGroup
	errs := make(chan string, 200)
	send := func(req string) {
		out, ok := tryRaw([]byte(req))
		if !ok || !strings.HasPrefix(out, `{"ok":true`) {
			errs <- fmt.Sprintf("%s: %s", req, out)
		}
	}
	for g := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := range 20 {
				switch g {
				case 0:
					if nr_render(64, 48, float64(k), 0, 1, 0xffffff, nil, 0, uint32(k&1)) == nil {
						errs <- "a render gave null"
					}
				case 1:
					send(`{"cmd": "tool", "name": "execute", "args": {"input": "for (let i = 0; i < 3; i++) Insert(document, {type: \"rectangle\", width: 5, height: 5})"}}`)
				case 2:
					fill := `"#00ff00"`
					if u := imageURL(k); u != "" {
						fill = fmt.Sprintf(`{"type": "image", "url": %q, "mode": "fill"}`, u)
					}
					send(fmt.Sprintf(`{"cmd": "insert", "node": {"type": "rectangle", "x": %d, "width": 30, "height": 30, "fill": %s}}`, k*30, fill))
				default:
					send(`{"cmd": "poll"}`)
					send(`{"cmd": "frames"}`)
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if n := len(jsonv.Items(call(t, `{"cmd": "layers"}`))); n != 20*3+20 {
		t.Errorf("%d layers, want 80", n)
	}
}
