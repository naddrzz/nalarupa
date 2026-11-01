//! The person's Claude Code CLI as the agent's account: whether it is signed in, and its own sign-in and sign-out.
//! Nalarupa only starts the CLI's flows (`claude auth login` opens the browser); it never reads or stores the
//! credentials, which stay with Claude Code.

use serde_json::{json, Value};
use std::process::{Child, Command, Stdio};
use std::sync::Mutex;

/// The running `claude auth login`, so a second click does not start another and Cancel can stop it.
static LOGIN: Mutex<Option<Child>> = Mutex::new(None);

/// `{installed, signedIn, method, account, plan}` from `claude auth status` (no model call).
pub fn status() -> Value {
    status_of("claude")
}

fn status_of(program: &str) -> Value {
    match Command::new(program).args(["auth", "status"]).stdin(Stdio::null()).output() {
        Ok(out) => parse_status(&String::from_utf8_lossy(&out.stdout), out.status.success()),
        Err(_) => json!({"installed": false, "signedIn": false, "method": null, "account": null, "plan": null}),
    }
}

/// The CLI prints JSON (`loggedIn`, `authMethod`, `email`, `subscriptionType`) and exits 1 when signed out.
fn parse_status(stdout: &str, success: bool) -> Value {
    let v: Value = serde_json::from_str(stdout.trim()).unwrap_or(Value::Null);
    let method = v["authMethod"].as_str().filter(|m| *m != "none");
    let signed_in = success && v["loggedIn"] != false && method.is_some();
    json!({
        "installed": true,
        "signedIn": signed_in,
        "method": if signed_in { json!(method) } else { Value::Null },
        "account": if signed_in { v["email"].clone() } else { Value::Null },
        "plan": if signed_in { v["subscriptionType"].clone() } else { Value::Null },
    })
}

/// Start the CLI's own sign-in in the background (it opens the browser). When it exits, the engine emits a
/// `claude_auth` event with the new status. `console` signs in to an Anthropic Console account (API billing).
pub fn login(console: bool) -> Result<(), String> {
    let mut running = LOGIN.lock().unwrap_or_else(|e| e.into_inner());
    if running.as_mut().is_some_and(|c| matches!(c.try_wait(), Ok(None))) {
        return Ok(());
    }
    let mut cmd = Command::new("claude");
    cmd.args(["auth", "login"]).stdin(Stdio::null()).stdout(Stdio::null()).stderr(Stdio::null());
    if console {
        cmd.arg("--console");
    }
    let child = cmd.spawn().map_err(|e| {
        if e.kind() == std::io::ErrorKind::NotFound {
            "Claude Code is not installed: install it (code.claude.com), then sign in here.".to_string()
        } else {
            format!("could not start `claude auth login`: {e}")
        }
    })?;
    *running = Some(child);
    std::thread::spawn(|| {
        loop {
            std::thread::sleep(std::time::Duration::from_millis(250));
            let mut running = LOGIN.lock().unwrap_or_else(|e| e.into_inner());
            match running.as_mut().map(Child::try_wait) {
                Some(Ok(None)) => continue,
                // Exited, failed to wait, or cancelled (taken): report where things stand now.
                _ => {
                    running.take();
                    break;
                }
            }
        }
        crate::engine::engine().emit(json!({"type": "claude_auth", "state": status()}));
    });
    Ok(())
}

/// Stop a sign-in that is still waiting for the browser.
pub fn cancel_login() {
    if let Some(mut c) = LOGIN.lock().unwrap_or_else(|e| e.into_inner()).take() {
        let _ = c.kill();
        let _ = c.wait();
    }
}

/// `claude auth logout`, then the new status.
pub fn logout() -> Result<Value, String> {
    let out = Command::new("claude").args(["auth", "logout"]).stdin(Stdio::null()).output().map_err(|e| format!("could not run `claude auth logout`: {e}"))?;
    if !out.status.success() {
        return Err(format!("`claude auth logout` failed: {}", String::from_utf8_lossy(&out.stderr).trim()));
    }
    Ok(status())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_cli_status_says_how_claude_code_is_signed_in() {
        let signed_in = r#"{"loggedIn": true, "authMethod": "claude.ai", "email": "a@b.co", "subscriptionType": "max"}"#;
        assert_eq!(parse_status(signed_in, true), json!({"installed": true, "signedIn": true, "method": "claude.ai", "account": "a@b.co", "plan": "max"}));
        let key = r#"{"loggedIn": true, "authMethod": "api_key"}"#;
        assert_eq!(parse_status(key, true)["method"], "api_key");
        let out = r#"{"loggedIn": false, "authMethod": "none"}"#;
        assert_eq!(parse_status(out, false), json!({"installed": true, "signedIn": false, "method": null, "account": null, "plan": null}));
        assert_eq!(parse_status("Not logged in", false)["signedIn"], false, "a CLI that prints text is read by its exit code");
        assert_eq!(status_of("nalarupa-no-such-cli")["installed"], false);
    }
}
