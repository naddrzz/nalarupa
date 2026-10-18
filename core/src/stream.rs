//! Helpers for executing an agent's `execute` call while the model is still
//! streaming it: pull the `input` string out of a partial JSON object and cut
//! the JavaScript at the last complete top-level statement.

/// Decode the (possibly unterminated) string value of `key` from a partial JSON object,
/// e.g. `{"input": "a = Insert(do` → `a = Insert(do`. Incomplete escapes are dropped.
pub fn partial_string(json: &str, key: &str) -> Option<String> {
    let pat = format!("\"{key}\"");
    let start = json.find(&pat)? + pat.len();
    let rest = json[start..].trim_start();
    let rest = rest.strip_prefix(':')?.trim_start();
    let mut chars = rest.strip_prefix('"')?.chars().peekable();
    let mut out = String::new();
    while let Some(c) = chars.next() {
        match c {
            '"' => return Some(out),
            '\\' => {
                let Some(e) = chars.next() else { return Some(out) };
                match e {
                    'n' => out.push('\n'),
                    't' => out.push('\t'),
                    'r' => out.push('\r'),
                    'b' => out.push('\u{8}'),
                    'f' => out.push('\u{c}'),
                    'u' => {
                        let hex: String = chars.by_ref().take(4).collect();
                        if hex.len() < 4 {
                            return Some(out);
                        }
                        let Ok(code) = u32::from_str_radix(&hex, 16) else { return Some(out) };
                        // Surrogate pairs arrive as two \u escapes.
                        if (0xD800..0xDC00).contains(&code) {
                            let lookahead: String = chars.clone().take(6).collect();
                            if lookahead.len() < 6 {
                                return Some(out);
                            }
                            if let Some(low) = lookahead.strip_prefix("\\u").and_then(|h| u32::from_str_radix(h, 16).ok()) {
                                for _ in 0..6 {
                                    chars.next();
                                }
                                if let Some(ch) = char::from_u32(0x10000 + ((code - 0xD800) << 10) + (low - 0xDC00)) {
                                    out.push(ch);
                                }
                            }
                        } else if let Some(ch) = char::from_u32(code) {
                            out.push(ch);
                        }
                    }
                    other => out.push(other),
                }
            }
            c => out.push(c),
        }
    }
    Some(out)
}

/// Byte length of the longest prefix of `src` that ends at a complete top-level
/// statement: a newline or `;` with all brackets closed and outside strings,
/// template literals and comments. A statement is held
/// until the next token shows it does not continue (`else`, `.map(...)`, `+ x`),
/// because each released piece runs on its own. 0 when no statement is complete yet.
pub fn complete_prefix(src: &str) -> usize {
    let b = src.as_bytes();
    let mut depth: i32 = 0;
    let mut last = 0;
    // End of the previous top-level statement, waiting for the next token to confirm it.
    let mut pending: Option<usize> = None;
    let mut i = 0;
    // Template literal nesting: each entry is the bracket depth at which a `${` opened.
    let mut tmpl: Vec<i32> = Vec::new();
    let mut in_tmpl = false;
    while i < b.len() {
        let c = b[i];
        if in_tmpl {
            match c {
                b'\\' => i += 1,
                b'`' => in_tmpl = false,
                b'$' if b.get(i + 1) == Some(&b'{') => {
                    tmpl.push(depth);
                    depth += 1;
                    in_tmpl = false;
                    i += 1;
                }
                _ => {}
            }
            i += 1;
            continue;
        }
        if let Some(end) = pending {
            let comment = c == b'/' && matches!(b.get(i + 1), Some(b'/' | b'*'));
            if !c.is_ascii_whitespace() && !comment {
                match continues(&src[i..]) {
                    None => return last,
                    Some(true) => {}
                    Some(false) => last = end,
                }
                pending = None;
            }
        }
        match c {
            b'"' | b'\'' => {
                i += 1;
                while i < b.len() && b[i] != c && b[i] != b'\n' {
                    if b[i] == b'\\' {
                        i += 1;
                    }
                    i += 1;
                }
                if i >= b.len() {
                    return last;
                }
            }
            b'`' => in_tmpl = true,
            b'/' if b.get(i + 1) == Some(&b'/') => {
                while i < b.len() && b[i] != b'\n' {
                    i += 1;
                }
                continue;
            }
            b'/' if b.get(i + 1) == Some(&b'*') => match src[i + 2..].find("*/") {
                Some(end) => i += end + 3,
                None => return last,
            },
            b'(' | b'[' | b'{' => depth += 1,
            b')' | b']' | b'}' => {
                depth -= 1;
                if c == b'}' && tmpl.last() == Some(&depth) {
                    tmpl.pop();
                    in_tmpl = true;
                }
            }
            b';' | b'\n' if depth == 0 => pending = Some(i + 1),
            _ => {}
        }
        i += 1;
    }
    last
}

/// Whether the code starting `rest` continues the statement before it; None while the
/// word there may still be growing.
fn continues(rest: &str) -> Option<bool> {
    let end = rest.find(|c: char| !(c.is_alphanumeric() || c == '_' || c == '$'))?;
    if end > 0 {
        return Some(matches!(&rest[..end], "else" | "catch" | "finally"));
    }
    Some(".,?:+-*/%&|^=<>([`".contains(rest.as_bytes()[0] as char))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn partial_json_strings() {
        assert_eq!(partial_string(r#"{"input": "a = 1;\nb = \"x"#, "input").unwrap(), "a = 1;\nb = \"x");
        assert_eq!(partial_string(r#"{"input":"done"}"#, "input").unwrap(), "done");
        assert_eq!(partial_string(r#"{"input":"trail\"#, "input").unwrap(), "trail");
        assert_eq!(partial_string(r#"{"input":"é\u00"#, "input").unwrap(), "é");
        assert_eq!(partial_string(r#"{"editId":"e1"}"#, "input"), None);
    }

    #[test]
    fn statement_boundaries() {
        let s = "a = Insert(document, {\n  type: \"frame\"\n})\nb = Insert(a, {type: \"te";
        assert_eq!(&s[..complete_prefix(s)], "a = Insert(document, {\n  type: \"frame\"\n})\n");
        let t = "x = `a ${f({y: 1})}\n` ; y = 2\n";
        assert_eq!(&t[..complete_prefix(t)], "x = `a ${f({y: 1})}\n` ;");
        let u = "for (const k of [1,2]) {\n Insert(a, {})\n";
        assert_eq!(complete_prefix(u), 0);
        assert_eq!(complete_prefix("s = \"semi; colon\"\n// c; (\nz = 1"), "s = \"semi; colon\"\n// c; (\n".len());
        // Held until the next token shows the statement ended.
        assert_eq!(complete_prefix("a = [1]\n  .map(f)\nb = 2\n"), "a = [1]\n  .map(f)\n".len());
        assert_eq!(complete_prefix("if (x) f()\nelse g()\n"), 0);
        assert_eq!(complete_prefix("if (x) f(); else g()\nh()"), "if (x) f(); else g()\n".len());
        assert_eq!(complete_prefix("a = 1\nel"), 0);
    }
}
