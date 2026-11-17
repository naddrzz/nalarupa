package agent

// Saved chats (agent.rs `save_chat`, `chats`, `load_chat`, `delete_chat`, `clear_chats`, `shutdown`): one JSON
// file per conversation in `<data_dir>/sessions`, written atomically by one background writer that keeps only the
// newest snapshot.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// chatKeep: saved chats untouched this long are deleted when the history is listed.
const chatKeep = 20 * 24 * time.Hour

// shutdownWait is how long Shutdown waits for the stopped agents to end (their `claude` children are killed by a
// watcher that polls).
const shutdownWait = 3 * time.Second

// chatOps serializes load, delete and clear: each touches the chat files and the current chat in separate steps,
// so one must not interleave with another. Always taken before the engine lock.
var chatOps sync.Mutex

func sessionsDir(dataDir string) string { return doc.JoinPath(dataDir, "sessions") }

// chatPath is the file of chat id. Ids come back from the UI: only plain alphanumerics may name a file.
func chatPath(dataDir, id string) (string, error) {
	ok := id != ""
	for i := 0; i < len(id) && ok; i++ {
		c := id[i]
		ok = 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
	}
	if !ok {
		return "", fmt.Errorf("invalid chat id: %s", id)
	}
	return doc.JoinPath(sessionsDir(dataDir), id+".json"), nil
}

// chatSave is a chat snapshot on its way to disk: cloned under the engine lock, serialized and written outside
// it. e is where a failed write is reported.
type chatSave struct {
	e             *engine.Engine
	dataDir, path string
	doc           *jsonv.Object
}

// write writes atomically: a temp file, then a rename over the old one.
func (j *chatSave) write() error {
	tmp := j.path + ".tmp"
	err := doc.CreateDirAll(sessionsDir(j.dataDir))
	if err == nil {
		var f *os.File
		if f, err = os.Create(tmp); err == nil {
			_, err = f.Write(jsonv.Marshal(j.doc))
			if err == nil {
				err = f.Sync()
			}
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
	}
	if err == nil {
		err = doc.Rename(tmp, j.path)
	}
	if err != nil {
		return errors.New(doc.IOError(err))
	}
	return nil
}

// snapshotChat clones the current chat for the writer, or nil when there is none. Under the engine lock.
func snapshotChat(e *engine.Engine) (*chatSave, error) {
	a := &e.Agent
	if a.Chat == nil {
		return nil, nil
	}
	path, err := chatPath(e.DataDir, a.Chat.ID)
	if err != nil {
		return nil, err
	}
	var main jsonv.Value
	if p, ok := a.Profile(a.Main); ok && a.Main != "" {
		main = p.JSON()
	}
	var session jsonv.Value
	if a.Session != "" {
		session = a.Session
	}
	// ponytail: rewrites the whole file at every message end; switch to an append-only log if long chats get slow.
	d := engine.Object("version", jsonv.Int(1), "id", a.Chat.ID, "title", a.Chat.Title, "updatedAt", jsonv.Int(int64(engine.NowMs())), "agent", main,
		"events", cloneArray(a.Chat.Events), "messages", cloneArray(a.Messages), "session", session)
	return &chatSave{e: e, dataDir: e.DataDir, path: path, doc: d}, nil
}

func cloneArray(items []jsonv.Value) *jsonv.Array {
	out := make([]jsonv.Value, len(items))
	for i, it := range items {
		out[i] = jsonv.Clone(it)
	}
	return jsonv.NewArray(out...)
}

// writer is the chat writer: the newest snapshot waits in pending (older ones are superseded), busy while one
// is written; failed holds the error of the last write, cleared by the next one that succeeds.
var writer struct {
	sync.Mutex
	changed sync.Cond
	start   sync.Once
	pending *chatSave
	busy    bool
	failed  error
}

func chatWriter() {
	writer.start.Do(func() {
		writer.changed.L = &writer.Mutex
		go func() {
			for {
				writer.Lock()
				for writer.pending == nil {
					writer.changed.Wait()
				}
				job := writer.pending
				writer.pending = nil
				writer.busy = true
				writer.Unlock()
				err := writeGuarded(job)
				writer.Lock()
				writer.busy = false
				writer.failed = err
				writer.Unlock()
				writer.changed.Broadcast()
				if err != nil {
					job.e.Locked(func() { job.e.Emit(engine.Object("type", "toast", "text", "Could not save the chat: "+err.Error())) })
				}
			}
		}()
	})
}

// writeGuarded writes j; a panic is a failed write, so the writer goes on.
func writeGuarded(j *chatSave) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("internal error (panic): %v", r)
		}
	}()
	return j.write()
}

// saveChat saves the current chat in the background. Only the clone happens here, under the engine lock.
func saveChat(e *engine.Engine) error {
	job, err := snapshotChat(e)
	if err != nil || job == nil {
		return err
	}
	chatWriter()
	writer.Lock()
	writer.pending = job
	writer.Unlock()
	writer.changed.Broadcast()
	return nil
}

// flushChatSaves waits until every queued chat save is on disk and returns the error of the last write if it
// failed. Safe with the engine locked: the writer is idle again before it reports a failure.
func flushChatSaves() error {
	chatWriter()
	writer.Lock()
	defer writer.Unlock()
	for writer.pending != nil || writer.busy {
		writer.changed.Wait()
	}
	return writer.failed
}

// Shutdown is `chat_flush`, called as the app exits. It stops the agents first and waits until their `claude`
// children are killed, since nothing outlives the process to do it (no run may go on without its canvas), then
// saves the chat: the writer is a goroutine the process does not wait for.
func Shutdown(e *engine.Engine) error {
	Stop(e)
	CancelLogin()
	deadline := time.Now().Add(shutdownWait)
	// Running outlasts Active across a queued or iteration handoff, until the next run has started.
	for time.Now().Before(deadline) {
		idle := false
		e.Locked(func() { idle = e.Agent.Active == 0 && !e.Agent.Running })
		if idle {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return flushChatSaves()
}

// readText is Rust's `read_to_string`: the file, which must be UTF-8.
func readText(path string) ([]byte, bool) {
	b, err := os.ReadFile(path)
	return b, err == nil && utf8.Valid(b)
}

func readChat(dataDir, id string) (jsonv.Value, error) {
	path, err := chatPath(dataDir, id)
	if err != nil {
		return nil, err
	}
	// Shown as is in the app: say what failed and what to do next.
	b, ok := readText(path)
	if !ok {
		return nil, errors.New("Unable to open this chat: its file was moved or deleted. Pick another chat or start a new one.")
	}
	v, err := jsonv.Parse(b)
	if err != nil {
		return nil, errors.New("Unable to open this chat: its file is damaged. Pick another chat or start a new one.")
	}
	return v, nil
}

// stemExt is Rust's `file_stem` and `extension` of a file name: a leading dot is part of the stem.
func stemExt(name string) (string, string, bool) {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 {
		return name, "", false
	}
	return name[:i], name[i+1:], true
}

// listChats is the saved chats, newest first. Files untouched for chatKeep are deleted.
func listChats(dataDir string) []jsonv.Value {
	cutoff := time.Now().Add(-chatKeep)
	chats := []jsonv.Value{}
	dir := sessionsDir(dataDir)
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		path := doc.JoinPath(dir, entry.Name())
		if info, err := entry.Info(); err == nil && info.ModTime().Before(cutoff) {
			doc.RemoveFile(path)
			continue
		}
		id, ext, ok := stemExt(entry.Name())
		if !ok || ext != "json" {
			continue
		}
		// Only ids chatPath accepts are listed: a row that load and delete reject could never be opened or removed.
		if _, err := chatPath(dataDir, id); err != nil {
			continue
		}
		// ponytail: parses every file for its title; keep an index file if the history gets large.
		b, ok := readText(path)
		if !ok {
			continue
		}
		v, err := jsonv.Parse(b)
		if err != nil {
			continue
		}
		chats = append(chats, engine.Object("id", id, "title", jsonv.Clone(jsonv.Get(v, "title")), "updatedAt", jsonv.Clone(jsonv.Get(v, "updatedAt"))))
	}
	at := func(c jsonv.Value) uint64 {
		n, _ := jsonv.Get(c, "updatedAt").(jsonv.Number)
		u, _ := n.Uint64()
		return u
	}
	sort.SliceStable(chats, func(i, j int) bool { return at(chats[i]) > at(chats[j]) })
	return chats
}

// Chats is `chat_list`: the saved chats plus the id of the current one.
func Chats(e *engine.Engine) jsonv.Value {
	flushChatSaves()
	var dir string
	var current jsonv.Value
	e.Locked(func() {
		dir = e.DataDir
		if e.Agent.Chat != nil {
			current = e.Agent.Chat.ID
		}
	})
	return engine.Object("current", current, "chats", jsonv.NewArray(listChats(dir)...))
}

// LoadChat is `chat_load`: make a saved chat the current conversation; it returns its events for the UI to
// replay.
func LoadChat(e *engine.Engine, id string) (jsonv.Value, error) {
	chatOps.Lock()
	defer chatOps.Unlock()
	flushChatSaves()
	e.Lock()
	defer e.Unlock()
	if e.Agent.Running {
		return nil, errors.New("Stop the agent before opening another chat.")
	}
	saved, err := readChat(e.DataDir, id)
	if err != nil {
		return nil, err
	}
	e.Agent.Restore(id, saved)
	return engine.Object("id", id, "title", jsonv.Clone(jsonv.Get(saved, "title")), "events", jsonv.Clone(jsonv.Get(saved, "events"))), nil
}

// DeleteChat is `chat_delete`: remove a saved chat. Removing the current one starts a new conversation. The file
// goes first: when it cannot be removed the conversation stays as it was.
func DeleteChat(e *engine.Engine, id string) error {
	chatOps.Lock()
	defer chatOps.Unlock()
	var path string
	var err error
	e.Locked(func() {
		if path, err = chatPath(e.DataDir, id); err != nil {
			return
		}
		if e.Agent.Running && e.Agent.Chat != nil && e.Agent.Chat.ID == id {
			err = errors.New("Stop the agent before removing the running chat.")
		}
	})
	if err != nil {
		return err
	}
	// Flushed first: a queued snapshot of this chat would bring the file back after the removal.
	flushChatSaves()
	if err := doc.RemoveFile(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.New(doc.IOError(err))
	}
	// A prompt sent meanwhile keeps the chat: its next save writes the file again.
	e.Locked(func() {
		if !e.Agent.Running && e.Agent.Chat != nil && e.Agent.Chat.ID == id {
			e.Agent.Clear()
		}
	})
	return nil
}

// ClearChats is `chat_clear`: delete every saved chat but a running one. True when the current chat went too. The
// current chat's files go last, so a failed removal leaves it listed and current.
func ClearChats(e *engine.Engine) (bool, error) {
	chatOps.Lock()
	defer chatOps.Unlock()
	var dir string
	var running bool
	// Both the file and the temp file its writer renames over it: removing the temp file fails the next save.
	var current []string
	e.Locked(func() {
		dir, running = e.DataDir, e.Agent.Running
		if c := e.Agent.Chat; c != nil {
			current = []string{c.ID + ".json", c.ID + ".json.tmp"}
		}
	})
	// Flushed before the removal: only a running chat can still queue snapshots, and its file is kept.
	flushChatSaves()
	var last []string
	entries, _ := os.ReadDir(sessionsDir(dir))
	for _, entry := range entries {
		path := doc.JoinPath(sessionsDir(dir), entry.Name())
		if slices.Contains(current, entry.Name()) {
			last = append(last, path)
		} else if err := doc.RemoveFile(path); err != nil {
			return false, errors.New(doc.IOError(err))
		}
	}
	if !running {
		// The temp file before the file: a failure in between still leaves the chat listed.
		sort.SliceStable(last, func(i, j int) bool {
			return strings.HasSuffix(last[i], ".json.tmp") && !strings.HasSuffix(last[j], ".json.tmp")
		})
		for _, path := range last {
			if err := doc.RemoveFile(path); err != nil {
				return false, errors.New(doc.IOError(err))
			}
		}
	}
	cleared := false
	e.Locked(func() {
		cleared = !running && !e.Agent.Running
		if cleared {
			e.Agent.Clear()
		}
	})
	return cleared, nil
}
