"""Draws the Arca app icon and writes app/AppIcon.icns.

Run with Pillow installed:  python3 scripts/make_icon.py
The .icns is committed, so this only needs re-running when the design changes.
"""
import io
import struct
import sys
from pathlib import Path

from PIL import Image, ImageDraw, ImageFilter

S = 2048  # supersampled canvas; downscaled for every icon size


def lerp(a, b, t):
    return tuple(int(a[i] + (b[i] - a[i]) * t) for i in range(len(a)))


def draw_icon() -> Image.Image:
    img = Image.new("RGBA", (S, S), (0, 0, 0, 0))

    # macOS icon grid: the tile is ~80% of the canvas with a soft shadow.
    margin = int(S * 0.098)
    tile = (margin, margin, S - margin, S - margin)
    radius = int((S - 2 * margin) * 0.225)

    shadow = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    ImageDraw.Draw(shadow).rounded_rectangle(
        (tile[0], tile[1] + S * 0.012, tile[2], tile[3] + S * 0.012), radius, fill=(0, 0, 0, 110))
    img.alpha_composite(shadow.filter(ImageFilter.GaussianBlur(S * 0.018)))

    # Vertical gradient: deep teal at the top to indigo at the bottom.
    top, bottom = (24, 160, 150), (52, 64, 170)
    grad = Image.new("RGBA", (S, S))
    gd = ImageDraw.Draw(grad)
    for y in range(S):
        gd.line([(0, y), (S, y)], fill=lerp(top, bottom, y / S) + (255,))
    mask = Image.new("L", (S, S), 0)
    ImageDraw.Draw(mask).rounded_rectangle(tile, radius, fill=255)
    img.paste(grad, (0, 0), mask)

    d = ImageDraw.Draw(img)
    white = (255, 255, 255, 255)
    cx = S // 2

    # Archive box: a lid and a body, with a handle slot in the body.
    bw, lid_h, body_h, gap = int(S * 0.46), int(S * 0.095), int(S * 0.28), int(S * 0.028)
    lid_w = int(bw * 1.1)
    top_y = int(S * 0.33)
    d.rounded_rectangle((cx - lid_w // 2, top_y, cx + lid_w // 2, top_y + lid_h),
                        int(lid_h * 0.3), fill=white)
    by = top_y + lid_h + gap
    d.rounded_rectangle((cx - bw // 2, by, cx + bw // 2, by + body_h), int(S * 0.04), fill=white)
    slot_w, slot_h = int(bw * 0.36), int(S * 0.045)
    sy = by + int(body_h * 0.22)
    d.rounded_rectangle((cx - slot_w // 2, sy, cx + slot_w // 2, sy + slot_h), slot_h // 2,
                        fill=lerp(top, bottom, sy / S) + (255,))

    # A small spark above the box: the "assistant" part.
    sx, syc, r = int(cx + lid_w * 0.42), int(top_y - S * 0.07), int(S * 0.055)
    spark = [(sx, syc - r), (sx + r * 0.28, syc - r * 0.28), (sx + r, syc), (sx + r * 0.28, syc + r * 0.28),
             (sx, syc + r), (sx - r * 0.28, syc + r * 0.28), (sx - r, syc), (sx - r * 0.28, syc - r * 0.28)]
    d.polygon(spark, fill=(255, 236, 160, 255))
    return img


# ICNS chunk types holding PNG data, by pixel size.
ICNS_TYPES = [(b"icp4", 16), (b"icp5", 32), (b"icp6", 64), (b"ic07", 128), (b"ic08", 256),
              (b"ic09", 512), (b"ic10", 1024), (b"ic11", 32), (b"ic12", 64), (b"ic13", 256), (b"ic14", 512)]


def write_icns(img: Image.Image, path: Path) -> None:
    chunks = b""
    for kind, size in ICNS_TYPES:
        buf = io.BytesIO()
        img.resize((size, size), Image.LANCZOS).save(buf, "PNG", optimize=True)
        data = buf.getvalue()
        chunks += kind + struct.pack(">I", len(data) + 8) + data
    path.write_bytes(b"icns" + struct.pack(">I", len(chunks) + 8) + chunks)


if __name__ == "__main__":
    root = Path(__file__).resolve().parent.parent
    icon = draw_icon()
    write_icns(icon, root / "app" / "AppIcon.icns")
    if len(sys.argv) > 1:  # optional preview PNG path
        icon.resize((512, 512), Image.LANCZOS).save(sys.argv[1])
