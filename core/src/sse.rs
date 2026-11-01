//! Server-sent events from a POST, for the model clients. The blocking socket work (headers and body reads) runs on
//! its own thread, so a silent connection cannot hold the caller: it polls `cancel` and the stall limit between lines.

use serde_json::Value;
use std::io::{BufRead, BufReader};
use std::sync::mpsc::{sync_channel, RecvTimeoutError};
use std::time::{Duration, Instant};

/// How often a silent stream re-checks `cancel`.
const POLL: Duration = Duration::from_millis(200);

/// POST `body` and hand each event's `data` (multi-line data joined) to `on`, until the stream ends or `on` returns
/// `Ok(false)`. An HTTP error status becomes `error(status, message)`, the message taken from `{"error": {"message"}}`
/// when the body has one. `who` names the server in the stall error.
pub fn post(
    req: ureq::RequestBuilder<ureq::typestate::WithBody>,
    body: Value,
    stall: Duration,
    who: &str,
    cancel: &dyn Fn() -> bool,
    error: fn(u16, &str) -> String,
    on: &mut dyn FnMut(&str) -> Result<bool, String>,
) -> Result<(), String> {
    // A thread abandoned on Stop ends at its next line (servers ping) or at the agent's global timeout. The worker
    // sends `Ok(None)` when the stream ends, so a dropped sender without it is an error. Bounded, so a slow `on`
    // backpressures the socket instead of queueing every line.
    let (tx, rx) = sync_channel::<Result<Option<String>, String>>(64);
    // Slightly past the stall limit, so the reader below reports the stall first.
    let hdr = Some(stall + POLL * 2);
    let req = req.config().timeout_connect(hdr).timeout_send_request(hdr).timeout_recv_response(hdr).build();
    std::thread::spawn(move || {
        let res = match req.send_json(&body) {
            Ok(res) => res,
            Err(e) => return drop(tx.send(Err(format!("network error: {e}")))),
        };
        let status = res.status().as_u16();
        let (_, body) = res.into_parts();
        if status >= 400 {
            let text = body.into_with_config().limit(1 << 20).read_to_string().unwrap_or_default();
            let msg = serde_json::from_str::<Value>(&text).ok().and_then(|v| v["error"]["message"].as_str().map(str::to_string)).unwrap_or(text);
            return drop(tx.send(Err(error(status, &msg))));
        }
        for line in BufReader::new(body.into_reader()).lines() {
            let line = line.map(Some).map_err(|e| format!("stream interrupted: {e}"));
            let failed = line.is_err();
            if tx.send(line).is_err() || failed {
                return;
            }
        }
        let _ = tx.send(Ok(None));
    });

    let mut data = String::new();
    let mut last = Instant::now();
    loop {
        if cancel() {
            return Err("stopped".into());
        }
        let line = match rx.recv_timeout(POLL) {
            Ok(Ok(Some(line))) => line,
            Ok(Ok(None)) => break,
            Ok(Err(e)) => return Err(e),
            Err(RecvTimeoutError::Timeout) if last.elapsed() < stall => continue,
            Err(RecvTimeoutError::Timeout) => return Err(format!("connection stalled: no data from {who} for {} s", stall.as_secs())),
            Err(RecvTimeoutError::Disconnected) => return Err("stream interrupted: network worker ended unexpectedly".into()),
        };
        last = Instant::now();
        if let Some(d) = line.strip_prefix("data:") {
            if !data.is_empty() {
                data.push('\n');
            }
            data.push_str(d.strip_prefix(' ').unwrap_or(d));
            continue;
        }
        // Comments (`: ping`) and other fields are ignored; a blank line ends the event.
        if !line.is_empty() || data.is_empty() {
            continue;
        }
        if !on(&std::mem::take(&mut data))? {
            return Ok(());
        }
    }
    // A last event without its blank line still counts.
    if !data.is_empty() {
        on(&data)?;
    }
    Ok(())
}
