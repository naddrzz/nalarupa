//! Canvas performance check: `cargo run --release --example bench -- <doc.pen> [w] [h] [png-prefix]`.
//! Times document open/sync, cold viewport paints, pans (cached-view copies), live zoom
//! (scaled previews), sharp zoom repaints and a small edit. BENCH_LAYERS=1 lists the slowest
//! top-level layers painted alone at 200%.

use nalarupa_core::engine::engine;
use std::path::Path;
use std::time::Instant;

const BG: [u8; 3] = [245, 245, 245];

fn main() {
    let args: Vec<String> = std::env::args().collect();
    let path = args.get(1).expect("usage: bench <doc.pen> [w] [h] [png-prefix]");
    let w: u32 = args.get(2).and_then(|s| s.parse().ok()).unwrap_or(1600);
    let h: u32 = args.get(3).and_then(|s| s.parse().ok()).unwrap_or(1000);
    let mut e = engine();
    let t = Instant::now();
    e.open(Path::new(path)).expect("open");
    e.sync();
    println!("open+sync        {:>8.1} ms   nodes {}", ms(t), e.renderer.flat().count());
    let (bx, by, bw, bh) = e.renderer.bounds().unwrap_or((0.0, 0.0, 1000.0, 1000.0));
    let fit = (w as f64 / bw).min(h as f64 / bh);
    for (label, zoom) in [("fit", fit), ("100%", 1.0), ("200%", 2.0)] {
        let (cx, cy) = (bx + bw / 2.0 - w as f64 / 2.0 / zoom, by + bh / 2.0 - h as f64 / 2.0 / zoom);
        let t = Instant::now();
        e.renderer.render(w, h, cx, cy, zoom, BG, None, true);
        let cold = ms(t);
        if let Some(out) = args.get(4) {
            let px = e.renderer.render(w, h, cx, cy, zoom, BG, None, true).to_vec();
            let pm = resvg::tiny_skia::Pixmap::from_vec(px, resvg::tiny_skia::IntSize::from_wh(w, h).unwrap()).unwrap();
            pm.save_png(format!("{out}-{label}.png").replace('%', "pct")).unwrap();
        }
        let t = Instant::now();
        for i in 0..10 {
            e.renderer.render(w, h, cx + i as f64 * 8.0 / zoom, cy, zoom, BG, None, false);
        }
        let pan = ms(t) / 10.0;
        let t = Instant::now();
        for i in 0..10 {
            e.renderer.render(w, h, cx, cy, zoom * (1.0 + i as f64 * 0.03), BG, None, false);
        }
        let live = ms(t) / 10.0;
        println!("{label:<5} cold {cold:>8.1} ms   pan {pan:>6.1} ms/frame   live zoom {live:>6.1} ms/frame");
    }
    let t = Instant::now();
    for i in 0..5 {
        e.renderer.render(w, h, bx, by, fit * (1.0 + i as f64 * 0.2), BG, None, true);
    }
    println!("sharp zoom       {:>8.1} ms/frame", ms(t) / 5.0);
    let text = e.renderer.flat().find(|f| f.ty == "text").map(|f| f.id.clone());
    if let Some(id) = text {
        let f = e.renderer.find(&id).map(|f| (f.x, f.y)).unwrap();
        e.renderer.render(w, h, f.0 - 100.0, f.1 - 100.0, 1.0, BG, None, true);
        let t = Instant::now();
        let _ = e.tool("execute", &serde_json::json!({"input": format!("Update(\"{id}\", {{content: \"Edited\"}})")}));
        e.sync();
        let edit_sync = ms(t);
        let t = Instant::now();
        e.renderer.render(w, h, f.0 - 100.0, f.1 - 100.0, 1.0, BG, None, true);
        println!("edit text        sync {edit_sync:>6.1} ms   repaint {:>6.1} ms", ms(t));
    }
    if std::env::var("BENCH_LAYERS").is_ok() {
        let mut slow = Vec::new();
        for id in e.renderer.order.clone() {
            let Some(f) = e.renderer.find(&id).map(|f| (f.x, f.y, f.w, f.h, f.name.clone())) else { continue };
            let t = Instant::now();
            e.renderer.render(w, h, f.0, f.1, 2.0, BG, Some(&id), true);
            slow.push((ms(t), id, f.4, f.2, f.3));
        }
        slow.sort_by(|a, b| b.0.total_cmp(&a.0));
        for s in slow.iter().take(6) {
            println!("  layer {:>8.1} ms  {} {:?} {}x{}", s.0, s.1, s.2, s.3, s.4);
        }
    }
}

fn ms(t: Instant) -> f64 {
    t.elapsed().as_secs_f64() * 1000.0
}
