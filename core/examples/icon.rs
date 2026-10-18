//! App icon assets from the brand SVGs (sizes ≤ 48 px use the simplified drawing):
//! `cargo run --release --example icon -- <nalarupa.svg> <nalarupa-small.svg> <out dir>`
//! Writes icon-<size>.png and nalarupa.ico (PNG-compressed entries, read by Windows Vista and later).
use std::{fs, path::Path};

fn raster(svg: &[u8], size: u32) -> Vec<u8> {
    let tree = resvg::usvg::Tree::from_data(svg, &resvg::usvg::Options::default()).expect("brand SVG");
    let mut pix = resvg::tiny_skia::Pixmap::new(size, size).expect("icon size");
    let s = size as f32 / tree.size().width();
    resvg::render(&tree, resvg::tiny_skia::Transform::from_scale(s, s), &mut pix.as_mut());
    pix.encode_png().expect("png")
}

fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let [full, small, out] = &args[..] else { panic!("usage: icon <nalarupa.svg> <nalarupa-small.svg> <out dir>") };
    let (full, small, out) = (fs::read(full).expect("full svg"), fs::read(small).expect("small svg"), Path::new(out));
    fs::create_dir_all(out).expect("out dir");
    let mut ico = Vec::new();
    for size in [16, 20, 24, 32, 40, 48, 64, 128, 256, 512, 1024] {
        let png = raster(if size <= 48 { &small } else { &full }, size);
        fs::write(out.join(format!("icon-{size}.png")), &png).expect("write png");
        if size <= 256 {
            ico.push((size, png));
        }
    }
    // ICONDIR, one ICONDIRENTRY per image (0 means 256), then the PNG blobs.
    let mut bytes = vec![0, 0, 1, 0];
    bytes.extend((ico.len() as u16).to_le_bytes());
    let mut offset = 6 + 16 * ico.len() as u32;
    for (size, png) in &ico {
        let d = if *size >= 256 { 0 } else { *size as u8 };
        bytes.extend([d, d, 0, 0]);
        bytes.extend(1u16.to_le_bytes());
        bytes.extend(32u16.to_le_bytes());
        bytes.extend((png.len() as u32).to_le_bytes());
        bytes.extend(offset.to_le_bytes());
        offset += png.len() as u32;
    }
    for (_, png) in &ico {
        bytes.extend(png);
    }
    fs::write(out.join("nalarupa.ico"), bytes).expect("write ico");
}
