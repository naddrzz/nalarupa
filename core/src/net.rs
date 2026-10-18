//! Blocking HTTP helpers with short timeouts (fonts, icons, placeholder images).

use std::time::Duration;

fn agent() -> ureq::Agent {
    ureq::Agent::config_builder()
        .timeout_global(Some(Duration::from_secs(8)))
        .user_agent("nalarupa/0.1")
        .build()
        .into()
}

pub fn get_bytes(url: &str) -> Option<Vec<u8>> {
    let mut res = agent().get(url).call().ok()?;
    res.body_mut().with_config().limit(32 * 1024 * 1024).read_to_vec().ok()
}

pub fn get_text(url: &str) -> Option<String> {
    String::from_utf8(get_bytes(url)?).ok()
}
