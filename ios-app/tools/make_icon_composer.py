"""Write AppIcon.icon (an Icon Composer document) for iOS 26+ and macOS 26+.

Light: white signal bars on the green gradient (same as the PNG icon).
Dark:  the same bars in that green on the system's dark icon background
       ("system-dark", what Apple's own apps use in dark icon mode).
Tinted/clear: left to the system.

Bar geometry is the same as render_icon.py (full-bleed iOS mapping).

usage: make_icon_composer.py OUT_DIR   -> OUT_DIR/AppIcon.icon/
"""
import json
import os
import sys

# Same greens as render_icon.py (mid ~ #34C759, iOS Settings › Cellular).
TOP, BOT = (64, 224, 106), (38, 164, 72)
CENTERS = [256, 384, 512, 640, 768]
WIDTH, BOTTOM, STEP = 80, 728, 87.5


def color(rgb):
    return "extended-srgb:" + ",".join(f"{c / 255:.5f}" for c in rgb) + ",1.00000"


def bars_svg():
    k = 1024 / 820  # the 820 px body of the original maps onto the full canvas
    tx = lambda x: (x - 102) * k
    ty = lambda y: (y - 101) * k
    r = WIDTH / 2
    shapes = []
    for i, c in enumerate(CENTERS):
        x0, x1 = tx(c - r), tx(c + r)
        y1 = ty(BOTTOM)
        if i == 0:  # the dot
            cx, cy, rad = tx(c), ty(BOTTOM - r), r * k
            shapes.append(f'<circle cx="{cx:.2f}" cy="{cy:.2f}" r="{rad:.2f}"/>')
        else:
            y0 = ty(293 + STEP * (4 - i))
            w = x1 - x0
            shapes.append(f'<rect x="{x0:.2f}" y="{y0:.2f}" width="{w:.2f}" height="{y1 - y0:.2f}" rx="{w / 2:.2f}"/>')
    return ('<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024" viewBox="0 0 1024 1024">'
            '<g fill="#FFFFFF">' + "".join(shapes) + "</g></svg>\n")


def icon_json():
    green = {"linear-gradient": [color(TOP), color(BOT)]}
    return {
        "fill-specializations": [
            {"value": green},
            {"appearance": "dark", "value": "system-dark"},
        ],
        "groups": [{
            "layers": [{
                "name": "bars",
                "image-name": "bars.svg",
                "fill-specializations": [
                    {"value": {"solid": "extended-gray:1.00000,1.00000"}},
                    {"appearance": "dark", "value": green},
                ],
                "glass": False,
                "position": {"scale": 1, "translation-in-points": [0, 0]},
            }],
            "shadow": {"kind": "neutral", "opacity": 0.5},
            "translucency": {"enabled": False, "value": 0.5},
            "specular": True,
        }],
        "supported-platforms": {"squares": "shared"},
    }


if __name__ == "__main__":
    out = os.path.join(sys.argv[1], "AppIcon.icon")
    os.makedirs(os.path.join(out, "Assets"), exist_ok=True)
    with open(os.path.join(out, "Assets", "bars.svg"), "w") as fh:
        fh.write(bars_svg())
    with open(os.path.join(out, "icon.json"), "w") as fh:
        json.dump(icon_json(), fh, indent=2)
        fh.write("\n")
    print("wrote", out)
