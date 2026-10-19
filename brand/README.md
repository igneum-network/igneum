# Igneum brand assets

## The rule (the founder, 4 October 2026)

One global logo for apps, profile pictures, favicons, everything. The mark sits in a black square. Never in a circle.
Never on another colour. Minimum clear space = 20% of the square. The Mac app icon is the model: a full square, all
black, the mark in the middle.

- Square: `#0C0C0E` (the site's obsidian token, `--obsidian` in `site/index.html`).
- Mark: `#F2541B` ember, the flame's centre cut in obsidian. Geometry = the two polygons in the site header
  (`site/index.html`, 100 x 100 viewBox). Do not redraw it.
- Mark height = 62% of the square (so 19% clear on top and bottom, 30% on each side).
- No ring, no border, no rounded corners baked in, apart from the one rounded variant below.

## Masters (`brand/master/`)

| File | Use |
|---|---|
| `igneum-mark-square.svg` | The master. 1024 x 1024, full-bleed obsidian, mark centred. Every derivative comes from this. |
| `igneum-mark-square-rounded.svg` | Apple's icon grid (824 x 824 shape on the 1024 canvas, continuous corner as a superellipse). Only for the DMG volume icon. Tahoe masks the plain square itself, so the app bundle ships the plain square. |

## Generator

`python3 brand/icons/make-icons.py` rebuilds every file below from the masters. It uses `rsvg-convert` when installed
(`brew install librsvg`), otherwise Pillow draws the master's polygons at 4x to 16x supersampling (exact, the mark is two
polygons). The Windows `.ico` and the favicons render each size from the vector, never by shrinking a bitmap.

## File map

| File | What | Where it is used |
|---|---|---|
| `brand/icons/igneum.icns` | plain square, 16 to 1024 | `Igneum Miner.app/Contents/Resources` (`packaging/mac/build-dmg.sh`, `CFBundleIconFile` = `igneum`) |
| `brand/icons/igneum-volume.icns` | rounded variant | the DMG volume icon (`packaging/mac/dmg/settings.py`) |
| `brand/icons/igneum.ico` | 16 24 32 48 64 128 256, BMP entries | every Windows exe through the `.rc` files (`packaging/windows/resources/*.rc`, `proto-cuda/nvrtc/*.rc`, `app/igneum-app/resources/igneum-app.rc`, `app/windows/host.rc`), Inno Setup (`Igneum-Miner.iss`) |
| `brand/icons/igneum-256.png` | the square at 256 | reference |
| `brand/icons/inno-*.bmp` | installer wizard art | Inno Setup |
| `brand/icons/dmg-background*.png/.tiff` | DMG window background | `packaging/mac/dmg/settings.py` |
| `site/favicon.ico` (16 32 48), `favicon-32.png`, `apple-touch-icon.png` (180, square: iOS rounds it), `icon-192.png`, `icon-512.png`, `maskable-512.png` (mark at 50%) | site favicons | every `site/*.html`, the header template in `site/build.mjs`, `site/site.webmanifest` |
| `relay/favicon.ico`, `favicon-32.png`, `apple-touch-icon.png` | relay favicons | `relay/index.html`, `relay/ui.html` (static files at the relay root) |
| `brand/profile/profile-400.png`, `profile-512.png`, `profile-1024.png` | profile pictures | X, Discord, Telegram, LinkedIn |
| `brand/profile/github-org-512.png` | organisation avatar | github.com/igneum-network (uploaded by hand from the Igneum Chrome profile) |
| `brand/profile/x-banner-1500x500.png` | X header | the mark centred on obsidian |
| `brand/igneum-icon-512.png`, `igneum-icon-1024.png` | the square at 512 and 1024 | the same as the profile files; kept for links that already point here |
| `site/og/<card>.png` (1200 x 630) and `site/og/<card>-square.png` (1200 x 1200), one per row of `CARDS` in `site/og/pages.mjs`: the mark over a soft ember glow, a kicker, one line, a quiet sub line, the route at the foot | share images (WhatsApp, iMessage, Slack, X, LinkedIn); the metas of every page come from `site/og/pages.mjs` through `site/build.mjs`, `?v=` from `OG_VERSION` | every served page; rendered by `node site/og/render.mjs` on a build box from `site/og/card.html` (8 October 2026) |
| `site/og.png`, `og-coin.png`, `og-square.png`, `og-small.png` | the earlier share images, kept only for links already cached by readers; nothing in the site points at them | retired 8 October 2026 |
| `brand/profile/github-social-1280x640.png` | repository social preview | github.com/igneum-network/igneum settings (by hand) |
| `brand/profile/vercel-avatar-512.png` | Vercel team or project avatar | by hand |

Inline SVG favicons (the `data:` link in every page head) carry the same square and mark, so a page shows the right icon
before the `.ico` loads.

## Not regenerated

- `brand/igneum-coin-1024.png`: the old ringed coin. Retired as a source; nothing reads it now.
- `brand/igneum-mark-transparent-1024.png`, the wordmark PNGs and `Unbounded.ttf`: unchanged.

## Before and after

`docs/brand/before/` holds the icons as they were (ringed coin everywhere except the GitHub avatar), `docs/brand/after-*.png`
the new set.
