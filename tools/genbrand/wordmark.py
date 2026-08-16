"""Makes the brand files that need a typeface.

    docs/site/assets/brand/logo.svg   the horizontal lockup: mark, then GLIDER
    docs/site/assets/brand/og.png     the social card, 1200x630
    docs/site/assets/brand/thumb.png  the 16:9 thumbnail, 1280x720

Run `go run ./tools/genbrand` first. This script reads the dart out of
docs/site/assets/brand/mark.svg, so the geometry of the mark still has exactly
one source and this file never draws it again.

    python tools/genbrand/wordmark.py

It needs fonttools and Pillow, and it needs the network on the first run to get
Archivo and IBM Plex Mono from Google Fonts. Both are cached beside this script
and neither is kept in the repository.

The wordmark goes into logo.svg as <path> outlines and not as a <text> element,
so that the file renders correctly on a machine that does not have Archivo.
"""

import io
import os
import re
import sys
import urllib.request

from fontTools.misc.transform import Transform
from fontTools.pens.boundsPen import BoundsPen
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen
from fontTools.ttLib import TTFont
from fontTools.varLib.instancer import instantiateVariableFont
from PIL import Image, ImageDraw, ImageFont

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, "..", ".."))
BRAND = os.path.join(ROOT, "docs", "site", "assets", "brand")
CACHE = os.path.join(HERE, ".archivo-900.ttf")
MONO_CACHE = os.path.join(HERE, ".plexmono-600.ttf")

ARCHIVO_URL = (
    "https://github.com/google/fonts/raw/main/ofl/archivo/Archivo%5Bwdth,wght%5D.ttf"
)
# The syntax line is set in the same mono the site uses for a command.
MONO_URL = (
    "https://github.com/google/fonts/raw/main/ofl/ibmplexmono/IBMPlexMono-SemiBold.ttf"
)

# The four colour pairs live in tools/genbrand/main.go. Only the two that these
# two files use are repeated here.
INK = "#12100e"
BONE = "#f5f1e8"
CITRON = "#c9f24d"

# Letter-spacing of the wordmark, as a fraction of the type size. The nav
# wordmark on the landing page uses the same figure.
TRACKING = 0.28


def fail(message):
    print(f"wordmark: {message}", file=sys.stderr)
    sys.exit(1)


# ───────────────────────── the mark ─────────────────────────


def read_mark():
    """Returns the two triangles of the dart, and its visible bounding box.

    mark.svg is written by tools/genbrand/main.go and holds two paths of the
    form `M x y L x y L x y Z`. Nothing else is expected in it, so a short
    reader is enough and no XML or SVG library is needed.
    """
    path = os.path.join(BRAND, "mark.svg")
    if not os.path.exists(path):
        fail(f"no {path} — run `go run ./tools/genbrand` first")
    svg = open(path, encoding="utf-8").read()

    triangles = []
    for d in re.findall(r'\sd="([^"]+)"', svg):
        nums = [float(n) for n in re.findall(r"-?\d+(?:\.\d+)?", d)]
        if len(nums) != 6:
            fail(f"mark.svg path is not a triangle: {d}")
        triangles.append([(nums[0], nums[1]), (nums[2], nums[3]), (nums[4], nums[5])])
    if len(triangles) != 2:
        fail(f"mark.svg has {len(triangles)} paths, expected 2")

    xs = [p[0] for t in triangles for p in t]
    ys = [p[1] for t in triangles for p in t]
    box = (min(xs), min(ys), max(xs), max(ys))
    return triangles, box


# ───────────────────────── the typeface ─────────────────────────


def archivo_900():
    """The Archivo variable font, cut to weight 900 and normal width."""
    if not os.path.exists(CACHE):
        print("wordmark: fetching Archivo from Google Fonts")
        try:
            with urllib.request.urlopen(ARCHIVO_URL) as r:
                data = r.read()
        except Exception as e:  # noqa: BLE001 - any network failure is the same failure
            fail(f"cannot fetch Archivo: {e}")
        font = TTFont(io.BytesIO(data))
        font = instantiateVariableFont(font, {"wght": 900, "wdth": 100}, inplace=True)
        font.save(CACHE)
    return TTFont(CACHE)


def plex_mono():
    """IBM Plex Mono SemiBold, on disk. Returns the path, for Pillow."""
    if not os.path.exists(MONO_CACHE):
        print("wordmark: fetching IBM Plex Mono from Google Fonts")
        try:
            with urllib.request.urlopen(MONO_URL) as r:
                data = r.read()
        except Exception as e:  # noqa: BLE001 - any network failure is the same failure
            fail(f"cannot fetch IBM Plex Mono: {e}")
        open(MONO_CACHE, "wb").write(data)
    return MONO_CACHE


def wordmark_paths(font, text, cap_height, x0, baseline):
    """The glyphs of `text` as one SVG path, set to the given cap height.

    Returns the path data and the bounding box of the ink. Tracking is added
    after every glyph except the last one: a trailing space would put the lockup
    out of balance against the gap on its left.

    The box is the ink and not the advance width. A lockup is measured on what
    is drawn, and the side bearings of G and R would otherwise widen the gap and
    the right edge by an amount nobody chose.
    """
    upem = font["head"].unitsPerEm
    cap = font["OS/2"].sCapHeight
    scale = cap_height / cap
    tracking = TRACKING * upem * scale

    glyphs = font.getGlyphSet()
    cmap = font.getBestCmap()
    hmtx = font["hmtx"]

    commands = []
    bounds = BoundsPen(glyphs)
    x = x0
    for i, ch in enumerate(text):
        name = cmap[ord(ch)]
        # The font draws with y upwards and SVG draws with y downwards, so the
        # y axis is negated and the origin is put on the baseline.
        transform = Transform(scale, 0, 0, -scale, x, baseline)
        pen = SVGPathPen(glyphs, ntos=lambda v: f"{v:.2f}".rstrip("0").rstrip("."))
        glyphs[name].draw(TransformPen(pen, transform))
        glyphs[name].draw(TransformPen(bounds, transform))
        commands.append(pen.getCommands())
        x += hmtx[name][0] * scale
        if i != len(text) - 1:
            x += tracking
    return " ".join(commands), bounds.bounds


# ───────────────────────── logo.svg ─────────────────────────


def build_logo(font, triangles, box):
    """The horizontal lockup: the mark, a gap, then GLIDER.

    Cap height matches the nose-to-tail height of the mark, and the gap is half
    the width of the mark. Both figures come from the mark itself, so the lockup
    stays correct if the dart is ever re-cut.

    Every fill reads --mark-body or --mark-wing, so one of the four colour pairs
    can be set from the page that inlines this file. The wordmark follows the
    body colour: on an ink ground both are bone, on a light ground both are ink.
    """
    x0, y0, x1, y1 = box
    mark_w, mark_h = x1 - x0, y1 - y0
    gap = mark_w / 2

    # Set the word once to find where its ink begins, then set it again shifted
    # so the ink starts exactly one gap after the mark.
    _, probe = wordmark_paths(font, "GLIDER", cap_height=mark_h, x0=0, baseline=mark_h)
    d, (tx0, ty0, tx1, ty1) = wordmark_paths(
        font, "GLIDER", cap_height=mark_h, x0=mark_w + gap - probe[0], baseline=mark_h
    )

    # The round letters of Archivo overshoot the cap line and the baseline by a
    # little. The box has to take them in, or G and D are clipped.
    top = min(0.0, ty0)
    height = max(mark_h, ty1) - top

    # Shift the mark so that its visible ink, and not its 100-unit box, starts
    # at the origin. A lockup aligns on ink.
    def tri(points):
        return "M " + " L ".join(f"{px - x0:g} {py - y0:g}" for px, py in points) + " Z"

    body = f"var(--mark-body, {BONE})"
    wing = f"var(--mark-wing, {CITRON})"

    svg = (
        f'<svg xmlns="http://www.w3.org/2000/svg"'
        f' viewBox="0 {top:.2f} {tx1:.2f} {height:.2f}" role="img" aria-label="Glider">\n'
        f"  <title>Glider</title>\n"
        f'  <path d="{tri(triangles[0])}" fill="{body}"/>\n'
        f'  <path d="{tri(triangles[1])}" fill="{wing}"/>\n'
        f'  <path d="{d}" fill="{body}"/>\n'
        f"</svg>\n"
    )
    out = os.path.join(BRAND, "logo.svg")
    open(out, "w", encoding="utf-8").write(svg)
    print(f"wordmark: wrote logo.svg ({tx1:.0f}x{height:.1f})")


# ───────────────────────── the raster cards ─────────────────────────

OG_W, OG_H = 1200, 630
THUMB_W, THUMB_H = 1280, 720
SS = 4  # supersampling factor; Pillow draws polygons without anti-aliasing


def draw_mark(draw, triangles, box, x, y, height):
    """Draws the dart with its nose-to-tail height at `height`, top-left at x, y.

    Returns the width it covered, so a caller can lay out what follows it
    without measuring the mark itself.
    """
    x0, y0, x1, y1 = box
    scale = height / (y1 - y0)
    for tri_pts, fill in zip(triangles, (BONE, CITRON)):
        draw.polygon(
            [((x + (px - x0) * scale) * SS, (y + (py - y0) * scale) * SS) for px, py in tri_pts],
            fill=fill,
        )
    return (x1 - x0) * scale


def draw_tracked(draw, x, baseline, text, font, fill, tracking):
    """Draws `text` with `tracking` extra pixels after every glyph but the last.

    Pillow has no letter-spacing, so the string is set one glyph at a time.
    Returns the x the ink ends at.
    """
    for i, ch in enumerate(text):
        draw.text((x * SS, baseline * SS), ch, font=font, fill=fill, anchor="ls")
        x += draw.textlength(ch, font=font) / SS
        if i != len(text) - 1:
            x += tracking
    return x


def build_thumb(triangles, box):
    """The 16:9 thumbnail: the lockup, the citron tick, then the syntax.

    Sized to survive being scaled down to about 320px wide, which is where a
    thumbnail is actually looked at. That is why there are only four elements
    and why the smallest of them is set at 44px: a quarter of that is still
    legible, and a fifth element would not be.

    The syntax is the hero, exactly as on the landing page. `/agy` is the one
    citron thing in the frame, so it is what the eye lands on.
    """
    img = Image.new("RGB", (THUMB_W * SS, THUMB_H * SS), INK)
    draw = ImageDraw.Draw(img)

    margin = 88
    cap = 108  # the lockup's cap height, and the mark's nose-to-tail height
    baseline = 250

    mark_w = draw_mark(draw, triangles, box, margin, baseline - cap, cap)
    display = ImageFont.truetype(CACHE, round(cap * 1000 / 686) * SS)
    draw_tracked(
        draw,
        # The gap after the mark is half the mark's width, as in logo.svg.
        margin + mark_w + mark_w / 2,
        baseline,
        "GLIDER",
        display,
        BONE,
        TRACKING * cap * 1000 / 686,
    )

    # The citron tick: a short bar on a dark ground. It is the signature the
    # landing hero, the demo film and the dashboard topbar all carry.
    draw.rectangle(
        [margin * SS, (baseline + 38) * SS, (margin + cap) * SS, (baseline + 45) * SS],
        fill=CITRON,
    )

    mono = ImageFont.truetype(plex_mono(), 116 * SS)
    end = draw.textlength("fix this ", font=mono) / SS
    draw.text((margin * SS, 470 * SS), "fix this ", font=mono, fill=BONE, anchor="ls")
    draw.text(((margin + end) * SS, 470 * SS), "/agy", font=mono, fill=CITRON, anchor="ls")

    caption = ImageFont.truetype(CACHE, 46 * SS)
    draw.text(
        (margin * SS, 578 * SS), "Multiple CLIs. One prompt.", font=caption, fill=BONE, anchor="ls"
    )

    out = os.path.join(BRAND, "thumb.png")
    img.resize((THUMB_W, THUMB_H), Image.LANCZOS).save(out)
    print(f"wordmark: wrote thumb.png ({THUMB_W}x{THUMB_H})")


def build_og(triangles, box):
    """The social card: ink ground, the mark at the left, the line at the right.

    The line is set on two lines because one line of 24 characters at weight 900
    would have to drop to about 58px beside the mark, which is too small to read
    as a thumbnail in a feed.

    Citron is a text colour here and nowhere else on a light ground. On ink it
    measures far above AA, and the landing page already sets links in citron on
    the same ground.
    """
    img = Image.new("RGB", (OG_W * SS, OG_H * SS), INK)
    draw = ImageDraw.Draw(img)

    x0, y0, x1, y1 = box
    mark_h = 176
    scale = mark_h / (y1 - y0)
    mark_x, mark_y = 96, (OG_H - mark_h) / 2
    for tri_pts, fill in zip(triangles, (BONE, CITRON)):
        draw.polygon(
            [
                ((mark_x + (px - x0) * scale) * SS, (mark_y + (py - y0) * scale) * SS)
                for px, py in tri_pts
            ],
            fill=fill,
        )

    # Clear space is the nose-to-tail height divided by four. The text starts
    # beyond it.
    text_x = mark_x + (x1 - x0) * scale + mark_h / 4 + 48
    size = 92
    font = ImageFont.truetype(CACHE, size * SS)
    lines = [("One terminal.", BONE), ("Every CLI.", CITRON)]
    line_h = 116

    # Set from the baseline, not from the ascender. The ascender of Archivo
    # sits well above the caps, and anchoring there pushes an all-caps and
    # lowercase line visibly high in its box.
    cap = size * 686 / 1000
    block = line_h * (len(lines) - 1) + cap
    first = (OG_H - block) / 2 + cap
    for i, (text, fill) in enumerate(lines):
        draw.text(
            (text_x * SS, (first + i * line_h) * SS),
            text,
            font=font,
            fill=fill,
            anchor="ls",
        )

    out = os.path.join(BRAND, "og.png")
    img.resize((OG_W, OG_H), Image.LANCZOS).save(out)
    print(f"wordmark: wrote og.png ({OG_W}x{OG_H})")


def main():
    triangles, box = read_mark()
    font = archivo_900()
    build_logo(font, triangles, box)
    build_og(triangles, box)
    build_thumb(triangles, box)


if __name__ == "__main__":
    main()
