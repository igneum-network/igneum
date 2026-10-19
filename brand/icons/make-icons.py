#!/usr/bin/env python3
"""Builds every Igneum icon from the master mark, brand/master/igneum-mark-square.svg (4 October 2026).

The founder's rule (4 October 2026): one global logo for apps, profile pictures, favicons, everything. The mark sits in a black
square (#0C0C0E, the site's obsidian token), centred, no circle, no ring, no border, never on another colour. The Mac app
is the model. The rounded variant (brand/master/igneum-mark-square-rounded.svg, Apple's 824-on-1024 icon grid) is used
only where the shape has to be baked into the file: the DMG volume icon. Tahoe masks the plain square itself.

Outputs:
  brand/icons/
    igneum.icns                 the app bundle icon (plain square, 16 to 1024, iconutil)
    igneum-volume.icns          the DMG volume icon (rounded variant)
    igneum.ico                  Windows icon, 16 24 32 48 64 128 256 px, uncompressed BMP entries, each rendered from the vector
    igneum-256.png              the square at 256 px
    inno-wizard-164x314.bmp     Inno Setup WizardImageFile
    inno-wizard-small-55x58.bmp Inno Setup WizardSmallImageFile
    inno-banner-150x57.bmp      header banner (NSIS size)
    dmg-background.png / @2x    660x400 DMG window background; dmg-background.tiff holds both when tiffutil is present
  site/   favicon.ico (16 32 48), favicon-32.png, apple-touch-icon.png (180, square: iOS rounds it), icon-192.png,
          icon-512.png, maskable-512.png (the mark at 50% of the square, inside the maskable safe zone)
  relay/  favicon.ico, favicon-32.png, apple-touch-icon.png (the relay serves static files from its root)
  brand/profile/  profile-400.png, profile-512.png, profile-1024.png (X, Discord, Telegram, LinkedIn), github-org-512.png,
          x-banner-1500x500.png (the mark centred on obsidian)
  brand/igneum-icon-512.png and igneum-icon-1024.png are refreshed from the master as well.
  site/og-small.png (256), og-square.png (1024), og-coin.png and og.png (1200x630 card), brand/profile/github-social-1280x640.png,
          vercel-avatar-512.png: the share images (make_social), the mark on obsidian, never the old ringed coin.

Rendering: rsvg-convert when it is installed (brew install librsvg), else Pillow draws the master's rect and polygons
itself at 4x to 16x supersampling (the mark is two polygons, so this is exact). cairosvg is not used: the conda build on
this Mac has no libcairo. Needs Pillow and, on a Mac, iconutil and tiffutil (both ship with macOS).
    python3 brand/icons/make-icons.py
"""
import os, re, subprocess, sys, shutil, tempfile
import xml.etree.ElementTree as ET
from PIL import Image, ImageDraw, ImageFont

HERE = os.path.dirname(os.path.abspath(__file__))
BRAND = os.path.dirname(HERE)
ROOT = os.path.dirname(BRAND)
MASTER = os.path.join(BRAND, 'master', 'igneum-mark-square.svg')
MASTER_ROUNDED = os.path.join(BRAND, 'master', 'igneum-mark-square-rounded.svg')
SITE = os.path.join(ROOT, 'site')
RELAY = os.path.join(ROOT, 'relay')
PROFILE = os.path.join(BRAND, 'profile')
FONT = os.path.join(BRAND, 'Unbounded.ttf')
OBSIDIAN = (12, 12, 14)
EMBER = (242, 84, 27)
MOLTEN = (255, 179, 92)
BONE = (244, 241, 236)
ARTBOARD = 1024
SVG = '{http://www.w3.org/2000/svg}'

# ---- the vector -------------------------------------------------------------------------------------------------

def hex_rgb(s):
    s = s.strip().lstrip('#')
    return tuple(int(s[i:i + 2], 16) for i in (0, 2, 4))

def parse_transform(t):
    """translate(tx ty) scale(s) -> (tx, ty, s); only the two forms the masters use."""
    tx = ty = 0.0
    s = 1.0
    for name, args in re.findall(r'(\w+)\(([^)]*)\)', t or ''):
        v = [float(x) for x in re.split(r'[\s,]+', args.strip()) if x]
        if name == 'translate':
            tx, ty = v[0], (v[1] if len(v) > 1 else 0.0)
        elif name == 'scale':
            s = v[0]
    return tx, ty, s

def load_master(path):
    """The master as a list of shapes: ('rect', fill) or ('poly', [(x, y), ...], fill), in document order, on the 1024 artboard."""
    root = ET.parse(path).getroot()
    shapes = []
    def walk(el, tx, ty, s):
        tag = el.tag.replace(SVG, '')
        if tag == 'g':
            gx, gy, gs = parse_transform(el.get('transform'))
            for c in el:
                walk(c, tx + gx * s, ty + gy * s, s * gs)
        elif tag == 'rect':
            shapes.append(('rect', hex_rgb(el.get('fill'))))
        elif tag == 'polygon':
            pts = [float(v) for v in re.split(r'[\s,]+', el.get('points').strip()) if v]
            poly = [(tx + pts[i] * s, ty + pts[i + 1] * s) for i in range(0, len(pts), 2)]
            shapes.append(('poly', poly, hex_rgb(el.get('fill'))))
        else:
            for c in el:
                walk(c, tx, ty, s)
    walk(root, 0.0, 0.0, 1.0)
    return shapes

def render_pillow(shapes, size, mark_scale=1.0):
    """The shapes at `size` px, supersampled, on transparency. mark_scale shrinks the polygons about the centre
    (the maskable icon wants the mark at 50% of the square instead of 62%)."""
    ss = max(2, min(16, 4096 // size))
    big = size * ss
    k = big / ARTBOARD
    im = Image.new('RGBA', (big, big), (0, 0, 0, 0))
    d = ImageDraw.Draw(im)
    c = ARTBOARD / 2
    for sh in shapes:
        if sh[0] == 'rect':
            d.rectangle((0, 0, big, big), fill=sh[1] + (255,))
        else:
            pts = [((c + (x - c) * mark_scale) * k, (c + (y - c) * mark_scale) * k) for x, y in sh[1]]
            d.polygon(pts, fill=sh[2] + (255,))
    return im.resize((size, size), Image.LANCZOS)

def render_rsvg(path, size):
    out = subprocess.run(['rsvg-convert', '-w', str(size), '-h', str(size), path], capture_output=True, check=True).stdout
    import io
    return Image.open(io.BytesIO(out)).convert('RGBA')

_cache = {}
def render(size, rounded=False, mark_scale=1.0):
    key = (size, rounded, mark_scale)
    if key not in _cache:
        path = MASTER_ROUNDED if rounded else MASTER
        if mark_scale == 1.0 and shutil.which('rsvg-convert'):
            im = render_rsvg(path, size)
        else:
            im = render_pillow(load_master(path), size, mark_scale)
        _cache[key] = im
    return _cache[key].copy()

# ---- outputs ----------------------------------------------------------------------------------------------------

def save_png(im, path):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    im.save(path, optimize=True)
    return path

def make_icns(name, rounded):
    tmp = tempfile.mkdtemp()
    iconset = os.path.join(tmp, 'igneum.iconset')
    os.mkdir(iconset)
    for s in (16, 32, 128, 256, 512):
        render(s, rounded).save(os.path.join(iconset, f'icon_{s}x{s}.png'))
        render(s * 2, rounded).save(os.path.join(iconset, f'icon_{s}x{s}@2x.png'))
    out = os.path.join(HERE, name)
    subprocess.run(['iconutil', '-c', 'icns', iconset, '-o', out], check=True)
    shutil.rmtree(tmp)
    return out

def make_ico(path, sizes):
    frames = [render(s).convert('RGBA') for s in sizes]
    os.makedirs(os.path.dirname(path), exist_ok=True)
    base, rest = frames[-1], frames[:-1]
    try:
        base.save(path, format='ICO', sizes=[(s, s) for s in sizes], append_images=rest, bitmap_format='bmp')
    except TypeError:
        base.save(path, format='ICO', sizes=[(s, s) for s in sizes], append_images=rest)
    return path

def font(size, weight='SemiBold'):
    f = ImageFont.truetype(FONT, size)
    try:
        f.set_variation_by_name(weight)
    except Exception:
        pass
    return f

def wordmark(text, size, colour=BONE, weight='SemiBold'):
    f = font(size, weight)
    l, t, r, b = f.getbbox(text)
    im = Image.new('RGBA', (r - l + 4, b - t + 4), (0, 0, 0, 0))
    ImageDraw.Draw(im).text((2 - l, 2 - t), text, font=f, fill=colour)
    return im

def paste_centre(dst, src, cx, cy):
    dst.alpha_composite(src, (int(cx - src.width / 2), int(cy - src.height / 2)))

def bmp(im, path):
    im.convert('RGB').save(path, format='BMP')

def make_inno():
    # The installer pages are obsidian already, so the square tile merges with the page: the mark reads centred, no edge.
    big = Image.new('RGBA', (164, 314), OBSIDIAN + (255,))
    paste_centre(big, render(124), 82, 110)
    paste_centre(big, wordmark('IGNEUM', 19, BONE, 'Bold'), 82, 205)
    paste_centre(big, wordmark('MINER', 13, MOLTEN, 'Medium'), 82, 232)
    bmp(big, os.path.join(HERE, 'inno-wizard-164x314.bmp'))
    small = Image.new('RGBA', (55, 58), OBSIDIAN + (255,))
    paste_centre(small, render(55), 27.5, 29)
    bmp(small, os.path.join(HERE, 'inno-wizard-small-55x58.bmp'))
    banner = Image.new('RGBA', (150, 57), OBSIDIAN + (255,))
    paste_centre(banner, render(50), 28, 28.5)
    w1 = wordmark('IGNEUM', 15, BONE, 'Bold')
    w2 = wordmark('MINER', 10, MOLTEN, 'Medium')
    banner.alpha_composite(w1, (56, 14))
    banner.alpha_composite(w2, (57, 34))
    bmp(banner, os.path.join(HERE, 'inno-banner-150x57.bmp'))

def arrow(draw, x0, x1, y, colour, width):
    draw.line((x0, y, x1 - width * 2, y), fill=colour, width=width)
    h = width * 3
    draw.polygon([(x1, y), (x1 - h * 1.6, y - h), (x1 - h * 1.6, y + h)], fill=colour)

# Finder icon slots on the DMG (1x pixels, icon size 96): the app at APP_SLOT, the Applications link at APPS_SLOT,
# README.txt and the Stop command in the row below. packaging/mac/dmg/settings.py uses the same numbers.
ICON = 96
APP_SLOT = (165, 130)
APPS_SLOT = (495, 130)
ROW2_Y = 330

def dashed_box(im, cx, cy, r, colour, width=3, dash=16, gap=10, radius=18):
    """A dashed rounded square: the outline drawn once, then masked by diagonal bars."""
    W, H = im.size
    layer = Image.new('RGBA', (W, H), (0, 0, 0, 0))
    ImageDraw.Draw(layer).rounded_rectangle((cx - r, cy - r, cx + r, cy + r), radius=radius, outline=colour, width=width)
    bars = Image.new('L', (W, H), 0)
    bd = ImageDraw.Draw(bars)
    k = -H
    while k < W + H:
        bd.line((k, 0, k + H, H), fill=255, width=dash)
        k += dash + gap
    alpha = layer.split()[3]
    alpha = Image.composite(alpha, Image.new('L', (W, H), 0), bars)
    layer.putalpha(alpha)
    im.alpha_composite(layer)

def make_dmg_background():
    """660x400 at 1x; drawn at 2x and downsampled. The rounded icon is painted under the app slot, so the slot reads as
    the product even before Finder draws the app icon on top of it."""
    S = 2
    W, H = 660 * S, 400 * S
    im = Image.new('RGBA', (W, H), OBSIDIAN + (255,))
    d = ImageDraw.Draw(im)
    ax, ay = APP_SLOT[0] * S, APP_SLOT[1] * S
    bx, by = APPS_SLOT[0] * S, APPS_SLOT[1] * S
    glow = Image.new('RGBA', (W, H), (0, 0, 0, 0))
    gd = ImageDraw.Draw(glow)
    for i, a in ((76 * S, 10), (66 * S, 12), (58 * S, 14)):
        gd.ellipse((ax - i, ay - i, ax + i, ay + i), fill=EMBER + (a,))
    im.alpha_composite(glow)
    paste_centre(im, render(ICON * S, rounded=True), ax, ay)
    dashed_box(im, bx, by, (ICON // 2 + 14) * S, MOLTEN + (190,), width=2 * S, dash=9 * S, gap=6 * S, radius=9 * S)
    arrow(d, (APP_SLOT[0] + 78) * S, (APPS_SLOT[0] - 78) * S, ay, EMBER + (255,), 4 * S)
    paste_centre(im, wordmark('Drag Igneum Miner to Applications', 16 * S, BONE, 'Medium'), 330 * S, 236 * S)
    paste_centre(im, wordmark('Then open it from Applications. Nobody from Igneum will ever ask for your seed.', 8 * S, (150, 148, 145), 'Regular'), 330 * S, 262 * S)
    im2x = im.convert('RGB')
    im2x.save(os.path.join(HERE, 'dmg-background@2x.png'))
    im1x = im2x.resize((660, 400), Image.LANCZOS)
    im1x.save(os.path.join(HERE, 'dmg-background.png'))
    tiff = os.path.join(HERE, 'dmg-background.tiff')
    if shutil.which('tiffutil'):
        subprocess.run(['tiffutil', '-cathidpicheck', os.path.join(HERE, 'dmg-background.png'),
                        os.path.join(HERE, 'dmg-background@2x.png'), '-out', tiff], check=True)

def make_site(folder, full=True):
    out = []
    out.append(make_ico(os.path.join(folder, 'favicon.ico'), (16, 32, 48)))
    out.append(save_png(render(32).convert('RGB'), os.path.join(folder, 'favicon-32.png')))
    out.append(save_png(render(180).convert('RGB'), os.path.join(folder, 'apple-touch-icon.png')))
    if full:
        out.append(save_png(render(192).convert('RGB'), os.path.join(folder, 'icon-192.png')))
        out.append(save_png(render(512).convert('RGB'), os.path.join(folder, 'icon-512.png')))
        out.append(save_png(render(512, mark_scale=50 / 62).convert('RGB'), os.path.join(folder, 'maskable-512.png')))
    return out

def make_profile():
    out = []
    for s in (400, 512, 1024):
        out.append(save_png(render(s).convert('RGB'), os.path.join(PROFILE, f'profile-{s}.png')))
    out.append(save_png(render(512).convert('RGB'), os.path.join(PROFILE, 'github-org-512.png')))
    banner = Image.new('RGB', (1500, 500), OBSIDIAN)
    tile = render(500).convert('RGB')
    banner.paste(tile, (500, 0))
    out.append(save_png(banner, os.path.join(PROFILE, 'x-banner-1500x500.png')))
    out.append(save_png(render(512).convert('RGB'), os.path.join(BRAND, 'igneum-icon-512.png')))
    out.append(save_png(render(1024).convert('RGB'), os.path.join(BRAND, 'igneum-icon-1024.png')))
    return out

def card(w, h, tile, cx, cy, title_x):
    """The share-card layout: the mark on obsidian at (cx, cy), the IGNEUM wordmark, tagline and subline to the right.
    Same layout as the old og-coin.png; the ringed coin and its glow are replaced by the master mark (the square merges
    with the ground, so nothing but the mark shows: no ring, no edge)."""
    im = Image.new('RGBA', (w, h), OBSIDIAN + (255,))
    paste_centre(im, render(tile), cx, cy)
    k = h / 630
    im.alpha_composite(wordmark('IGNEUM', int(84 * k), BONE, 'Bold'), (title_x, int(cy - 92 * k)))
    im.alpha_composite(wordmark('Mined by GPUs. Proven by fire.', int(31 * k), MOLTEN, 'SemiBold'), (title_x, int(cy + 20 * k)))
    im.alpha_composite(wordmark('The GPU-mined layer 1 \u00b7 igneum.network', int(19 * k), (150, 148, 145), 'Regular'), (title_x, int(cy + 72 * k)))
    return im.convert('RGB')

def make_social():
    """Share images. og-small.png stays a 256 square (the compact card chat apps show; its pages declare 256x256 and
    twitter:card summary), og-square.png 1024 square, og-coin.png and og.png the 1200x630 card (og.png is what bench.html,
    evidence.html and build.mjs reference), github-social-1280x640.png the repository social preview."""
    out = []
    out.append(save_png(render(256).convert('RGB'), os.path.join(SITE, 'og-small.png')))
    out.append(save_png(render(1024).convert('RGB'), os.path.join(SITE, 'og-square.png')))
    c = card(1200, 630, 300, 276, 315, 532)
    out.append(save_png(c, os.path.join(SITE, 'og-coin.png')))
    out.append(save_png(c, os.path.join(SITE, 'og.png')))
    out.append(save_png(card(1280, 640, 306, 300, 320, 560), os.path.join(PROFILE, 'github-social-1280x640.png')))
    out.append(save_png(render(512).convert('RGB'), os.path.join(PROFILE, 'vercel-avatar-512.png')))
    return out

def main():
    made = []
    made.append(save_png(render(256), os.path.join(HERE, 'igneum-256.png')))
    if sys.platform == 'darwin' and shutil.which('iconutil'):
        made.append(make_icns('igneum.icns', rounded=False))
        made.append(make_icns('igneum-volume.icns', rounded=True))
    else:
        print('icns skipped: iconutil is a macOS tool')
    made.append(make_ico(os.path.join(HERE, 'igneum.ico'), (16, 24, 32, 48, 64, 128, 256)))
    make_inno()
    make_dmg_background()
    made += make_site(SITE)
    made += make_site(RELAY, full=False)
    made += make_profile()
    made += make_social()
    for f in sorted(os.listdir(HERE)):
        p = os.path.join(HERE, f)
        if os.path.isfile(p) and not f.endswith('.py'):
            made.append(p)
    print('renderer:', 'rsvg-convert' if shutil.which('rsvg-convert') else 'Pillow (polygons from the master SVG)')
    for p in sorted(set(made)):
        print(f'{os.path.getsize(p):>8}  {os.path.relpath(p, ROOT)}')

if __name__ == '__main__':
    main()
