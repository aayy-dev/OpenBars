"""Redraw the signal-bars icon from scratch (vector shapes rendered at 4x and
downsampled) in iOS system green. Geometry measured from the original:
body = superellipse n~4.5, 820 px, centered at (512, 511) on a 1024 canvas;
dot + 4 capsules, 80 px wide, 128 px apart, bottoms at y=728, +87 px each.

usage: render.py OUTDIR  -> mac-1024.png (macOS style), ios-1024.png (full bleed),
                            ios-dark-1024.png (iOS 18+ dark: green bars, no background)

iOS 26+ and macOS 26+ use AppIcon.icon instead (make_icon_composer.py).
"""
import os
import sys

import numpy as np
from PIL import Image, ImageDraw, ImageFilter

SS = 4  # supersampling
TOP, BOT = (64, 224, 106), (38, 164, 72)  # mid ~ #34C759 (iOS Settings Cellular)
GLYPH_TOP, GLYPH_BOT = 249, 205           # bar shading, as in the original

# glyph in original 1024 coordinates
CENTERS = [256, 384, 512, 640, 768]
WIDTH, BOTTOM, STEP = 80, 728, 87


def lerp(a, b, f):
    return tuple(round(a[i] + (b[i] - a[i]) * f) for i in range(3))


def vgradient(size, top, bot, y0=0, y1=None):
    w, h = size
    y1 = h if y1 is None else y1
    f = np.clip((np.arange(h) - y0) / max(y1 - y0, 1), 0, 1)[:, None, None]
    arr = np.array(top)[None, None, :] * (1 - f) + np.array(bot)[None, None, :] * f
    return Image.fromarray(np.repeat(arr, w, axis=1).astype(np.uint8), "RGB")


def superellipse_mask(n_px, cx, cy, half, n=4.5):
    y, x = np.mgrid[0:n_px, 0:n_px].astype(float) + 0.5
    v = np.abs((x - cx) / half) ** n + np.abs((y - cy) / half) ** n
    return Image.fromarray(((v <= 1) * 255).astype(np.uint8), "L")


def glyph_layers(n_px, tf):
    """Mask of the bars and their shading, with tf mapping original coords."""
    mask = Image.new("L", (n_px, n_px), 0)
    d = ImageDraw.Draw(mask)
    r = WIDTH / 2
    for i, c in enumerate(CENTERS):
        # measured tops: 293, 380.5, 468, 555.5 for the bars; the dot is a circle
        top = 293 + 87.5 * (4 - i) if i else BOTTOM - WIDTH
        x0, y0 = tf(c - r, top)
        x1, y1 = tf(c + r, BOTTOM)
        if i == 0:
            d.ellipse((x0, y0, x1, y1), fill=255)
        else:
            d.rounded_rectangle((x0, y0, x1, y1), radius=(x1 - x0) / 2, fill=255)
    # shading: one vertical ramp across the whole glyph, like the original
    _, ytop = tf(0, 293)
    _, ybot = tf(0, BOTTOM)
    shade = vgradient((n_px, n_px), (GLYPH_TOP,) * 3, (GLYPH_BOT,) * 3, int(ytop), int(ybot))
    return mask, shade


def render(full_bleed):
    n = 1024 * SS
    if full_bleed:  # map the 820 px body onto the whole iOS canvas
        k = 1024 / 820
        tf = lambda x, y: ((x - 102) * k * SS, (y - 101) * k * SS)
        body = Image.new("L", (n, n), 255)
        bg = vgradient((n, n), TOP, BOT)
    else:
        tf = lambda x, y: (x * SS, y * SS)
        body = superellipse_mask(n, 512 * SS, 511 * SS, 410 * SS)
        bg = vgradient((n, n), TOP, BOT, 101 * SS, 921 * SS)
    canvas = bg.convert("RGBA")

    gmask, shade = glyph_layers(n, tf)
    # soft drop shadow under the bars, kept inside the body
    shadow = Image.new("L", (n, n), 0)
    shadow.paste(gmask, (0, int(7 * SS)))
    shadow = shadow.filter(ImageFilter.GaussianBlur(9 * SS)).point(lambda v: int(v * 0.42))
    canvas = Image.composite(Image.new("RGBA", (n, n), (12, 70, 32, 255)), canvas, shadow)
    canvas = Image.composite(shade.convert("RGBA"), canvas, gmask)

    if not full_bleed:
        # faint light rim just inside the edge, as on the original
        inner = body.filter(ImageFilter.MinFilter(4 * SS + 1))
        rim = Image.fromarray(np.clip(np.asarray(body, float) - np.asarray(inner, float), 0, 255).astype(np.uint8))
        rim = rim.filter(ImageFilter.GaussianBlur(1.5 * SS)).point(lambda v: int(v * 0.35))
        canvas = Image.composite(Image.new("RGBA", (n, n), (255, 255, 255, 255)), canvas, rim)
        canvas.putalpha(body)
    out = canvas.resize((1024, 1024), Image.LANCZOS)
    return out.convert("RGB") if full_bleed else out


def render_dark():
    """iOS 18-25 dark variant: the bars in the icon's green on a transparent
    background; the system draws its own dark background behind it."""
    n = 1024 * SS
    k = 1024 / 820
    tf = lambda x, y: ((x - 102) * k * SS, (y - 101) * k * SS)
    gmask, _ = glyph_layers(n, tf)
    _, ytop = tf(0, 293)
    _, ybot = tf(0, BOTTOM)
    green = vgradient((n, n), TOP, BOT, int(ytop), int(ybot)).convert("RGBA")
    green.putalpha(gmask)
    return green.resize((1024, 1024), Image.LANCZOS)


if __name__ == "__main__":
    outdir = sys.argv[1]
    os.makedirs(outdir, exist_ok=True)
    render(False).save(os.path.join(outdir, "mac-1024.png"))
    render(True).save(os.path.join(outdir, "ios-1024.png"))
    render_dark().save(os.path.join(outdir, "ios-dark-1024.png"))
    print("rendered")
