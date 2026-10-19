#!/usr/bin/env python3
"""Builds Ember's icons from the Ember mark, brand/master/ember-mark-square.svg (12 October 2026: the founder's choice,
candidate A, "Underlit", of site/brand/ember on lead/site-rebuild: the coal lit along a jagged burn line from the base).

The form is the Igneum pipeline's (brand/icons/make-icons.py at the repository root, which keeps serving the site, the relay
and the profiles with the Igneum mark; this script serves only the app): the mark sits in a black square (#0C0C0E, the
obsidian token), centred, 62% of the square, no ring, no border, never on another colour. The app bundle ships the plain
square (Tahoe masks it). The DMG volume icon bakes Apple's rounded shape (824 on 1024, a superellipse of exponent 5) into
the file; it is drawn here from the same master instead of from a second one.

Outputs, in this folder, under the names packaging/mac/build-dmg.sh, packaging/mac/dmg/settings.py, app/igneum-app/build.rs,
packaging/windows/embed-resources.sh and packaging/windows/Ember.iss read:
  igneum.icns                        the app bundle icon (plain square, 16 to 1024, iconutil)
  igneum-volume.icns                 the DMG volume icon (the rounded shape)
  igneum.ico                         Windows icon, 16 24 32 48 64 128 256 px, uncompressed BMP entries, each from the vector
  dmg-background.png / @2x / .tiff   660x400 DMG window background (the rounded icon under the app slot, the drag arrow);
                                     the tiff holds both scales when tiffutil is present
  inno-wizard-164x314.bmp            Inno Setup WizardImageFile (the mark on obsidian)
  inno-wizard-small-55x58.bmp        Inno Setup WizardSmallImageFile

Rendering: Pillow draws the master's rect and polygons itself at 4x to 16x supersampling (the mark is polygons, so this
is exact); no rsvg, no cairo. Needs Pillow and, on a Mac, iconutil and tiffutil (both ship with macOS; elsewhere the
icns files are skipped and said so).
    python3 brand/icons/make-icons.py [source.svg]      the source defaults to brand/master/ember-mark-square.svg
"""
import math, os, re, subprocess, sys, shutil, tempfile
import xml.etree.ElementTree as ET
from PIL import Image, ImageDraw, ImageFont

HERE = os.path.dirname(os.path.abspath(__file__))
BRAND = os.path.dirname(HERE)
BASE = os.path.dirname(BRAND)
MASTER = os.path.join(BRAND, 'master', 'ember-mark-square.svg')
SOURCE = os.path.abspath(sys.argv[1]) if len(sys.argv) > 1 else MASTER
OBSIDIAN = (12, 12, 14)
EMBER = (242, 84, 27)
MOLTEN = (255, 179, 92)
BONE = (244, 241, 236)
ARTBOARD = 1024
SHAPE = 824          # Apple's icon grid: the rounded shape's width on the 1024 canvas
SVG = '{http://www.w3.org/2000/svg}'
# The faces (0.2.8, the founder's word of 14 October 2026: the installers in the site's design): the display face
# Unbounded (Google Fonts, the variable family, OFL 1.1) and the label face IBM Plex Mono (OFL 1.1), as the site serves
# them in site/design/fonts.css; the render reads TTF copies named by IGNEUM_DISPLAY_TTF and IGNEUM_MONO_TTF (the site
# lands woff2 alone), else a checkout's brand/Unbounded.ttf, else the system's Helvetica (named in the record then).
FONTS = [os.environ.get('IGNEUM_DISPLAY_TTF', ''), os.path.join(os.path.dirname(os.path.dirname(BASE)), 'brand', 'Unbounded.ttf'),
         '/System/Library/Fonts/Helvetica.ttc', '/System/Library/Fonts/Supplemental/Arial.ttf']
MONO_FONTS = [os.environ.get('IGNEUM_MONO_TTF', ''), '/System/Library/Fonts/Menlo.ttc', '/System/Library/Fonts/Supplemental/Courier New.ttf']

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

def superellipse(n=1024, exponent=5):
    """Apple's rounded shape on the artboard: 824 wide, centred, the continuous corner as a superellipse."""
    c = ARTBOARD / 2
    r = SHAPE / 2
    pts = []
    for i in range(n):
        t = 2 * math.pi * i / n
        x, y = math.cos(t), math.sin(t)
        pts.append((c + r * math.copysign(abs(x) ** (2 / exponent), x), c + r * math.copysign(abs(y) ** (2 / exponent), y)))
    return pts

_shapes = None
_cache = {}
def render(size, rounded=False, tile=True):
    """The mark at `size` px on transparency: the plain square, the rounded shape (the mark kept at 62% of the shape),
    or with tile=False the mark alone."""
    global _shapes
    key = (size, rounded, tile)
    if key in _cache:
        return _cache[key].copy()
    if _shapes is None:
        _shapes = load_master(SOURCE)
    ss = max(2, min(16, 4096 // size))
    big = size * ss
    k = big / ARTBOARD
    im = Image.new('RGBA', (big, big), (0, 0, 0, 0))
    d = ImageDraw.Draw(im)
    c = ARTBOARD / 2
    scale = SHAPE / ARTBOARD if rounded else 1.0
    for sh in _shapes:
        if sh[0] == 'rect':
            if not tile:
                continue
            if rounded:
                d.polygon([(x * k, y * k) for x, y in superellipse()], fill=sh[1] + (255,))
            else:
                d.rectangle((0, 0, big, big), fill=sh[1] + (255,))
        else:
            pts = [((c + (x - c) * scale) * k, (c + (y - c) * scale) * k) for x, y in sh[1]]
            d.polygon(pts, fill=sh[2] + (255,))
    _cache[key] = im.resize((size, size), Image.LANCZOS)
    return _cache[key].copy()

# ---- outputs ----------------------------------------------------------------------------------------------------

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
    base, rest = frames[-1], frames[:-1]
    try:
        base.save(path, format='ICO', sizes=[(s, s) for s in sizes], append_images=rest, bitmap_format='bmp')
    except TypeError:
        base.save(path, format='ICO', sizes=[(s, s) for s in sizes], append_images=rest)
    return path

def font(size, weight='SemiBold', faces=None):
    for p in (faces or FONTS):
        if p and os.path.exists(p):
            f = ImageFont.truetype(p, size)
            try:
                f.set_variation_by_name(weight)
            except Exception:
                pass
            return f
    return ImageFont.load_default()

def wordmark(text, size, colour=BONE, weight='SemiBold', faces=None, tracking=0.0):
    """The words as one image; `tracking` is the letter spacing as a fraction of the size (the site's .08em on labels)."""
    f = font(size, weight, faces)
    if tracking:
        gap = int(round(size * tracking))
        glyphs = [(ch, f.getbbox(ch)) for ch in text]
        width = sum((b[2] - b[0]) for _, b in glyphs) + gap * (len(text) - 1) + 4
        top = min(b[1] for _, b in glyphs); bottom = max(b[3] for _, b in glyphs)
        im = Image.new('RGBA', (max(1, width), bottom - top + 4), (0, 0, 0, 0))
        d = ImageDraw.Draw(im); x = 2
        for ch, b in glyphs:
            d.text((x - b[0], 2 - top), ch, font=f, fill=colour); x += (b[2] - b[0]) + gap
        return im
    l, t, r, b = f.getbbox(text)
    im = Image.new('RGBA', (r - l + 4, b - t + 4), (0, 0, 0, 0))
    ImageDraw.Draw(im).text((2 - l, 2 - t), text, font=f, fill=colour)
    return im

def lockup(size_display, colour_word=BONE, colour_by=(150, 148, 145)):
    """The wordmark: "Ember" in the display face (Unbounded 700) over "BY IGNEUM" in the label face (Plex Mono 500,
    uppercase, .08em tracking), as the app's rail sets it."""
    word = wordmark('Ember', size_display, colour_word, 'Bold')
    by = wordmark('BY IGNEUM', max(8, int(size_display * 0.34)), colour_by, 'Medium', faces=MONO_FONTS, tracking=0.08)
    gap = max(2, size_display // 8)
    im = Image.new('RGBA', (max(word.width, by.width), word.height + gap + by.height), (0, 0, 0, 0))
    im.alpha_composite(word, ((im.width - word.width) // 2, 0))
    im.alpha_composite(by, ((im.width - by.width) // 2, word.height + gap))
    return im

def paste_centre(dst, src, cx, cy):
    dst.alpha_composite(src, (int(cx - src.width / 2), int(cy - src.height / 2)))

def bmp(im, path):
    im.convert('RGB').save(path, format='BMP')
    return path

def make_inno():
    """The installer pages are obsidian already, so the mark reads centred on them, no tile edge: the mark alone on obsidian,
    on the tall image's upper third and filling the small one."""
    big = Image.new('RGBA', (164, 314), OBSIDIAN + (255,))
    paste_centre(big, render(96, tile=False), 82, 96)
    paste_centre(big, lockup(22), 82, 176)
    small = Image.new('RGBA', (55, 58), OBSIDIAN + (255,))
    paste_centre(small, render(40, tile=False), 27.5, 29)
    return [bmp(big, os.path.join(HERE, 'inno-wizard-164x314.bmp')), bmp(small, os.path.join(HERE, 'inno-wizard-small-55x58.bmp'))]

def make_inno_lockup():
    """One strip for every wizard page (the project's standard of 11:0x UK, 14 October 2026: the lockup and the mark at the same
    place and size on every page): the mark beside the "Ember BY IGNEUM" lockup on obsidian, 220 x 58 at 1x and 440 x 116
    at 2x; the wizard loads the one its scale asks for."""
    out = []
    for S in (1, 2):
        im = Image.new('RGBA', (220 * S, 58 * S), OBSIDIAN + (255,))
        paste_centre(im, render(44 * S, tile=False), 30 * S, 29 * S)
        lk = lockup(20 * S)
        im.alpha_composite(lk, (int(62 * S), int(29 * S - lk.height / 2)))
        out.append(bmp(im, os.path.join(HERE, f'inno-lockup-{220 * S}x{58 * S}.bmp')))
    return out

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
    paste_centre(im, lockup(26 * S), 330 * S, 48 * S)
    paste_centre(im, wordmark('Drag Ember to Applications', 15 * S, BONE, 'Bold'), 330 * S, 236 * S)
    paste_centre(im, wordmark('THEN OPEN IT FROM APPLICATIONS. NOBODY FROM IGNEUM WILL EVER ASK FOR YOUR SEED.', 7 * S, (150, 148, 145), 'Medium', faces=MONO_FONTS, tracking=0.08), 330 * S, 262 * S)
    out = []
    im2x = im.convert('RGB')
    p2 = os.path.join(HERE, 'dmg-background@2x.png')
    p1 = os.path.join(HERE, 'dmg-background.png')
    im2x.save(p2)
    im2x.resize((660, 400), Image.LANCZOS).save(p1)
    out += [p1, p2]
    if shutil.which('tiffutil'):
        tiff = os.path.join(HERE, 'dmg-background.tiff')
        subprocess.run(['tiffutil', '-cathidpicheck', p1, p2, '-out', tiff], check=True)
        out.append(tiff)
    return out

def main():
    made = []
    if sys.platform == 'darwin' and shutil.which('iconutil'):
        made.append(make_icns('igneum.icns', rounded=False))
        made.append(make_icns('igneum-volume.icns', rounded=True))
    else:
        print('icns skipped: iconutil is a macOS tool')
    made.append(make_ico(os.path.join(HERE, 'igneum.ico'), (16, 24, 32, 48, 64, 128, 256)))
    made += make_inno()
    made += make_inno_lockup()
    made += make_dmg_background()
    print('source:', os.path.relpath(SOURCE, BASE))
    for p in made:
        print(f'{os.path.getsize(p):>8}  {os.path.relpath(p, BASE)}')

if __name__ == '__main__':
    main()
