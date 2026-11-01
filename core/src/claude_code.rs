//! The person's Claude Code CLI as the agent's account: whether it is signed in, and its own sign-in and sign-out.
//! Nalarupa only starts the CLI's flows (`claude auth login` opens the browser); it never reads or stores the
//! credentials, which stay with Claude Code.

use serde_json::{json, Value};
use std::process::{Child, Command, Stdio};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Mutex;

/// The running `claude auth login` and its generation, so a second click does not start another, Cancel can stop
/// it, and only the watcher of the current one reports.
static LOGIN: Mutex<Option<(u64, Child)>> = Mutex::new(None);
static GENERATION: AtomicU64 = AtomicU64::new(0);

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
    login_with("claude", console, |state| crate::engine::engine().emit(json!({"type": "claude_auth", "state": state})))
}

/// `login` with the program and the report injected (tests use a stand-in CLI and a channel).
fn login_with(program: &str, console: bool, report: impl FnOnce(Value) + Send + 'static) -> Result<(), String> {
    let mut running = LOGIN.lock().unwrap_or_else(|e| e.into_inner());
    if running.as_mut().is_some_and(|(_, c)| matches!(c.try_wait(), Ok(None))) {
        return Ok(());
    }
    let mut cmd = Command::new(program);
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
    let gen = GENERATION.fetch_add(1, Ordering::SeqCst) + 1;
    *running = Some((gen, child));
    let program = program.to_string();
    std::thread::spawn(move || {
        loop {
            std::thread::sleep(std::time::Duration::from_millis(250));
            let mut running = LOGIN.lock().unwrap_or_else(|e| e.into_inner());
            match running.as_mut() {
                Some((g, c)) if *g == gen => {
                    if matches!(c.try_wait(), Ok(None)) {
                        continue;
                    }
                    running.take();
                    break;
                }
                // Cancelled or replaced by a newer sign-in: that one reports.
                _ => return,
            }
        }
        // Read outside every lock: a slow CLI must not hold anything else up.
        report(status_of(&program));
    });
    Ok(())
}

/// Stop a sign-in that is still waiting for the browser (also on app shutdown).
pub fn cancel_login() {
    if let Some((_, mut c)) = LOGIN.lock().unwrap_or_else(|e| e.into_inner()).take() {
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

    /// A stand-in CLI whose `auth status` reads a flag file, whose `auth login` sets it after a moment and records
    /// its arguments, and whose `auth logout` clears it. A shell script, or a .cmd file on Windows.
    fn stand_in(dir: &std::path::Path) -> String {
        let flag = dir.join("signed-in");
        let args = dir.join("login-args");
        #[cfg(windows)]
        let (path, body) = (
            dir.join("claude.cmd"),
            format!(
                "@echo off\r\nif \"%2\"==\"status\" (if exist \"{f}\" (echo {{\"loggedIn\": true, \"authMethod\": \"claude.ai\"}}) else (echo {{\"loggedIn\": false}}& exit /b 1))\r\nif \"%2\"==\"login\" (echo %* > \"{a}\"& ping -n 2 127.0.0.1 > nul & type nul > \"{f}\")\r\n",
                f = flag.display(),
                a = args.display()
            ),
        );
        #[cfg(not(windows))]
        let (path, body) = (
            dir.join("claude"),
            format!(
                "#!/bin/sh\ncase \"$2\" in\n  status) [ -f '{f}' ] || {{ echo '{{\"loggedIn\": false}}'; exit 1; }}; echo '{{\"loggedIn\": true, \"authMethod\": \"claude.ai\"}}' ;;\n  login) echo \"$@\" > '{a}'; sleep 1; touch '{f}' ;;\nesac\n",
                f = flag.display(),
                a = args.display()
            ),
        );
        std::fs::write(&path, body).unwrap();
        #[cfg(unix)]
        std::fs::set_permissions(&path, std::os::unix::fs::PermissionsExt::from_mode(0o755)).unwrap();
        path.display().to_string()
    }

    #[test]
    fn a_sign_in_runs_the_cli_flow_reports_once_and_can_be_cancelled() {
        let dir = std::env::temp_dir().join(format!("nalarupa-auth-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let cli = stand_in(&dir);
        assert_eq!(status_of(&cli)["signedIn"], false);
        // A cancelled sign-in reports nothing; the next one (Console) completes and reports exactly once.
        let (tx, rx) = std::sync::mpsc::channel();
        let first = tx.clone();
        login_with(&cli, false, move |s| first.send(s).unwrap()).unwrap();
        cancel_login();
        login_with(&cli, true, move |s| tx.send(s).unwrap()).unwrap();
        let state = rx.recv_timeout(std::time::Duration::from_secs(10)).expect("the sign-in reports when the CLI flow ends");
        assert_eq!(state["signedIn"], true, "{state}");
        assert!(rx.recv_timeout(std::time::Duration::from_millis(800)).is_err(), "the cancelled sign-in does not report");
        assert!(std::fs::read_to_string(dir.join("login-args")).unwrap().contains("--console"), "Console sign-in passes --console");
        let _ = std::fs::remove_dir_all(&dir);
    }
}
