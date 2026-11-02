"""Write the recolored icons into the AppIcon set (iOS + macOS sizes) and an
.iconset for iconutil."""
import json
import os
import sys

from PIL import Image

out, appiconset, iconset = sys.argv[1], sys.argv[2], sys.argv[3]
ios = Image.open(os.path.join(out, "ios-1024.png")).convert("RGB")
mac = Image.open(os.path.join(out, "mac-1024.png")).convert("RGBA")

for f in os.listdir(appiconset):
    if f.endswith(".png"):
        os.remove(os.path.join(appiconset, f))
ios.save(os.path.join(appiconset, "icon-1024.png"))
images = [{"filename": "icon-1024.png", "idiom": "universal", "platform": "ios", "size": "1024x1024"}]
dark = os.path.join(out, "ios-dark-1024.png")
if os.path.exists(dark):  # iOS 18-25 dark icon mode
    Image.open(dark).convert("RGBA").save(os.path.join(appiconset, "icon-dark-1024.png"))
    images.append({"appearances": [{"appearance": "luminosity", "value": "dark"}],
                   "filename": "icon-dark-1024.png", "idiom": "universal", "platform": "ios", "size": "1024x1024"})
for px in (16, 32, 64, 128, 256, 512, 1024):
    mac.resize((px, px), Image.LANCZOS).save(os.path.join(appiconset, f"mac-{px}.png"))
for pt in (16, 32, 128, 256, 512):
    images.append({"filename": f"mac-{pt}.png", "idiom": "mac", "scale": "1x", "size": f"{pt}x{pt}"})
    images.append({"filename": f"mac-{pt * 2}.png", "idiom": "mac", "scale": "2x", "size": f"{pt}x{pt}"})
with open(os.path.join(appiconset, "Contents.json"), "w") as fh:
    json.dump({"images": images, "info": {"author": "xcode", "version": 1}}, fh, indent=2)

os.makedirs(iconset, exist_ok=True)
for pt in (16, 32, 128, 256, 512):
    mac.resize((pt, pt), Image.LANCZOS).save(os.path.join(iconset, f"icon_{pt}x{pt}.png"))
    mac.resize((pt * 2, pt * 2), Image.LANCZOS).save(os.path.join(iconset, f"icon_{pt}x{pt}@2x.png"))
print("ok", len(images), "asset entries")
