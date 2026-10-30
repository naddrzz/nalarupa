# Design guidance

Deeper notes for the design direction in `designing`.

## Type

- Pairings (display + text, all from Google Fonts); the default sans-serif
  (Inter) is one option, not the default:
- Fraunces + Work Sans: warm editorial, food, craft
- Barlow Condensed + Barlow: signage, timetables, transport
- IBM Plex Mono + IBM Plex Sans: ledgers, data, developer tools
- Instrument Serif + Public Sans: galleries, culture, quiet luxury
- Bricolage Grotesque + Manrope: friendly products, communities
- Space Grotesk + Space Mono: technical, science, hardware
- Playfair Display + Source Sans 3: classic print, finance, law
- Archivo Black + Archivo: posters, sport, loud campaigns
- Baloo 2 + Nunito: children, play, learning
- Inter + Inter: neutral product UI where the content is the design
- Scale (desktop): 12 · 14 · 16 · 20 · 24 · 32 · 48 · 64 · 96. Mobile tops out
  around 34 for titles, 16–17 body.
- Line height: 1.1–1.2 for headlines, 1.4–1.6 for body (`lineHeight`).
- Tighten big headlines slightly (`letterSpacing: -1` to `-3`), loosen small caps (+1 to +4).

## Spacing

- Use a 4/8px rhythm: 4, 8, 12, 16, 24, 32, 48, 64, 96, 128.
- Generous outer padding on screens: 64–120px desktop, 20–24px mobile.

## Color

- Gradients, shadows and big radii each earn their place in the direction;
  use them on purpose, not as decoration.
- Dark themes: avoid pure black/white; use #0E0F12-ish backgrounds and
  #F2F2F2-ish text.
- Light/dark: define themed variables
  (`value: [{value, theme: {mode: "light"}}, {value, theme: {mode: "dark"}}]`)
  and set `theme: {mode: "dark"}` on a frame to preview.

## Layout

- Desktop screens 1440 wide; tablets 834; phones 393×852.
- Use a content column (e.g. 1200 max) inside the screen with
  `alignItems: "center"` on the screen frame.
- Make each section distinct: vary alignment, density and scale between
  sections instead of repeating the same grid of cards.

## Mobile

- Status bar area (≈54px) at the top, home indicator (≈34px) at the bottom.
- Tap targets ≥ 44px. Bottom tab bars hold 3–5 items with icon + label.
- One primary action per screen.

## Web apps / dashboards

- Sidebar 240–280px, top bar 56–64px, content padding 24–32px.
- Tables: header row + rows as horizontal frames with `fill_container`
  columns; right-align numbers.
- Metrics: big number, label, delta with color; keep 3–4 per row.
