package agent

import (
	"fmt"
	"os"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/export"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// PermissionTool is the name of the MCP tool Claude Code asks before running a tool it may not use on its own
// (`--permission-prompt-tool`).
const PermissionTool = "approve"

// AskPermission: Claude Code wants a tool that is not pre-allowed. File tools on paths inside the open document's
// folder (and reading installed skills) are allowed outright; anything else shows a permission card and waits for
// the user. It answers Claude Code's permission JSON.
func AskPermission(e *engine.Engine, agent string, args jsonv.Value) engine.ToolOut {
	tool := jsonv.StrOr(jsonv.Get(args, "tool_name"))
	input := jsonv.Clone(jsonv.Get(args, "input"))
	allow := engine.ToolOut{Text: string(jsonv.Marshal(engine.Object("behavior", "allow", "updatedInput", input)))}
	deny := func(why string) engine.ToolOut {
		return engine.ToolOut{Text: string(jsonv.Marshal(engine.Object("behavior", "deny", "message", why)))}
	}
	var out *engine.ToolOut
	var p engine.Profile
	var id string
	asked := make(chan bool, 1)
	e.Locked(func() {
		skills := ""
		if home, err := os.UserHomeDir(); err == nil {
			skills = doc.JoinPath(doc.JoinPath(home, ".claude"), "skills")
		}
		if f, ok := jsonv.Str(jsonv.Get(input, "file_path")); ok && export.PathAllowed(f, tool, e.Doc.Dir(), e.Doc.Path, skills) {
			out = &allow
			return
		}
		var known bool
		if p, known = e.Agent.Profile(agent); !known {
			d := deny("Unknown agent.")
			out = &d
			return
		}
		if e.Agent.Cancel.IsSet() {
			d := deny("The agent was stopped.")
			out = &d
			return
		}
		id = fmt.Sprintf("pm%x", doc.RandomU64()&0xffff_ffff)
		if e.Agent.Permissions == nil {
			e.Agent.Permissions = map[string]engine.Permission{}
		}
		// An id drawn twice replaces the older request, which is denied (Rust drops its sender, so its wait fails).
		if old, ok := e.Agent.Permissions[id]; ok {
			old.Answer <- false
		}
		e.Agent.Permissions[id] = engine.Permission{Agent: p.ID, Answer: asked}
	})
	if out != nil {
		return *out
	}
	emit(e, p, "permission", engine.Object("id", id, "tool", tool, "input", input))
	allowed := <-asked
	emit(e, p, "permission_done", engine.Object("id", id, "allowed", allowed))
	if allowed {
		return allow
	}
	return deny("The user denied permission for this tool call.")
}

// AnswerPermission is `agent_permission`: the user's answer to a permission card, "allow", "deny" or "auto"
// (allow, and from the next turn on let Claude Code's auto mode decide; kept as a setting).
func AnswerPermission(e *engine.Engine, id, answer string) error {
	if answer != "allow" && answer != "deny" && answer != "auto" {
		return fmt.Errorf("Unknown permission answer \"%s\"; use allow, deny or auto.", answer)
	}
	e.Lock()
	defer e.Unlock()
	w, ok := e.Agent.Permissions[id]
	if !ok {
		return fmt.Errorf("This permission request is no longer waiting.")
	}
	delete(e.Agent.Permissions, id)
	if answer == "auto" {
		e.Settings.Set("agent_auto_permissions", true)
		e.SaveSettings() // still auto for this run when it did not save; never block the answer
	}
	w.Answer <- answer != "deny"
	return nil
}
