"""Generate every yoto-proxy icon from the logo drawn below.

Usage (from the repo root): python3 -I tools/make_icons.py

The logo is a Yoto-shaped card whose label is a tiny server, broadcasting
sound, drawn in the app's palette (web/src/app.css). There are two variants:
orange tile (default) and dark tile (dark mode where the platform supports
switching: browser favicons, the app header, the GitHub README).

Writes SVGs directly and renders PNG/ICO files with ImageMagick (`magick`);
the social preview is composed with Pillow.
"""
import os
import subprocess
import tempfile

from PIL import Image, ImageDraw, ImageFont

CREAM, CREAM_EDGE = "#faf7f2", "#e7dfd4"
BROWN, BROWN_3, BROWN_LINE = "#2a2420", "#4a3f37", "#6a5b50"
ORANGE, ORANGE_DARK = "#f25c2a", "#c4461c"
GOLD = "#e3a93a"
MUTED = "#7a6f66"

VARIANTS = {
    # tile, optional tile outline, card, card edge, drawers, lights, slot lines, waves
    "light": dict(tile=ORANGE, outline=None, card=CREAM, edge=CREAM_EDGE, drawer=BROWN,
                  light1=ORANGE, light2=GOLD, line=BROWN_LINE, wave=CREAM),
    "dark": dict(tile=BROWN, outline=BROWN_3, card=ORANGE, edge=ORANGE_DARK, drawer=BROWN,
                 light1=CREAM, light2=GOLD, line=BROWN_LINE, wave=GOLD),
}


def artwork(c):
    """The card and waves, as SVG elements in a 512x512 space."""
    parts = [
        f'<path d="M330 206 A70 70 0 0 1 330 302 M366 172 A118 118 0 0 1 366 336" fill="none" '
        f'stroke="{c["wave"]}" stroke-width="24" stroke-linecap="round"/>',
        f'<rect x="110" y="118" width="188" height="300" rx="28" fill="{c["edge"]}"/>',
        f'<rect x="100" y="104" width="188" height="300" rx="28" fill="{c["card"]}"/>',
    ]
    for i, y in enumerate((138, 221, 304)):
        light = c["light2"] if i == 1 else c["light1"]
        parts += [
            f'<rect x="126" y="{y}" width="136" height="66" rx="14" fill="{c["drawer"]}"/>',
            f'<circle cx="152" cy="{y + 33}" r="11" fill="{light}"/>',
            f'<rect x="176" y="{y + 28}" width="62" height="10" rx="5" fill="{c["line"]}"/>',
        ]
    return "\n    ".join(parts)


def tile(c, full_bleed=False):
    if full_bleed:  # square; iOS and launchers apply their own mask
        return f'<rect width="512" height="512" fill="{c["tile"]}"/>'
    outline = f' stroke="{c["outline"]}" stroke-width="6"' if c["outline"] else ""
    return f'<rect x="3" y="3" width="506" height="506" rx="113" fill="{c["tile"]}"{outline}/>'


def logo_svg(variant, full_bleed=False):
    c = VARIANTS[variant]
    return (
        '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512">\n'
        '  <title>yoto-proxy</title>\n'
        f'  {tile(c, full_bleed)}\n'
        f'  <g>\n    {artwork(c)}\n  </g>\n'
        '</svg>\n'
    )


def favicon_svg():
    """Both variants in one file, switched by the browser's color scheme."""
    light, dark = VARIANTS["light"], VARIANTS["dark"]
    return (
        '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512">\n'
        '  <style>.dark{display:none}@media (prefers-color-scheme:dark){.light{display:none}.dark{display:inline}}</style>\n'
        f'  <g class="light">\n    {tile(light)}\n    {artwork(light)}\n  </g>\n'
        f'  <g class="dark">\n    {tile(dark)}\n    {artwork(dark)}\n  </g>\n'
        '</svg>\n'
    )


def render(svg_text, size, out):
    with tempfile.NamedTemporaryFile("w", suffix=".svg", delete=False) as f:
        f.write(svg_text)
    try:
        density = max(72, int(72 * size / 512 * 4))  # render at 4x, then downsample
        subprocess.run(["magick", "-background", "none", "-density", str(density), f.name,
                        "-resize", f"{size}x{size}", out], check=True)
    finally:
        os.unlink(f.name)


def social_preview(logo_png, out):
    sp = Image.new("RGB", (1280, 640), CREAM)
    logo = Image.open(logo_png).convert("RGBA")
    sp.paste(logo, (120, 120), logo)
    d = ImageDraw.Draw(sp)

    def font(size):
        for f in ("/System/Library/Fonts/SFNSRounded.ttf", "/System/Library/Fonts/SFNS.ttf",
                  "/System/Library/Fonts/Helvetica.ttc"):
            try:
                return ImageFont.truetype(f, size)
            except OSError:
                pass
        return ImageFont.load_default()

    d.text((590, 230), "yoto-proxy", font=font(84), fill=BROWN)
    d.text((594, 345), "Your Navidrome music and Audiobookshelf", font=font(32), fill=MUTED)
    d.text((594, 390), "books on Yoto Make Your Own cards", font=font(32), fill=MUTED)
    sp.save(out, optimize=True)


def main():
    static, docs = "web/static/", "docs/"
    light, dark = logo_svg("light"), logo_svg("dark")

    for path, text in ((static + "logo.svg", light), (static + "logo-dark.svg", dark),
                       (static + "favicon.svg", favicon_svg())):
        with open(path, "w") as f:
            f.write(text)

    bleed = logo_svg("light", full_bleed=True)
    for name, n in (("apple-touch-icon.png", 180), ("apple-touch-icon-precomposed.png", 180),
                    ("icon-192.png", 192), ("icon-512.png", 512)):
        render(bleed, n, static + name)
    for name, text, n in (("favicon-32.png", light, 32), ("favicon-64.png", light, 64),
                          ("logo.png", light, 128)):
        render(text, n, static + name)
    render(light, 256, docs + "logo.png")
    render(dark, 256, docs + "logo-dark.png")

    with tempfile.TemporaryDirectory() as tmp:
        frames = []
        for n in (16, 32, 48):
            p = os.path.join(tmp, f"{n}.png")
            render(light, n, p)
            frames.append(p)
        subprocess.run(["magick", *frames, static + "favicon.ico"], check=True)
        big = os.path.join(tmp, "400.png")
        render(light, 400, big)
        social_preview(big, docs + "social-preview.png")


if __name__ == "__main__":
    main()
