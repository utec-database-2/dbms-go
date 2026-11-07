"""Genera la galería de demostración de la Parte 4 (imágenes sintéticas).

Crea 8 categorías de paisajes simples (6 imágenes cada una) en
datos_demo/imagenes/. Los vectores se calculan después con Go, con el mismo
extractor que usa el servidor para las imágenes que sube el usuario:

    python3 datos_demo/generar_imagenes.py
    go run ./dbms/cmd/imgcsv -dir datos_demo/imagenes -out datos_demo/imagenes.csv

Requiere Pillow (pip install pillow).
"""

import os
import random

from PIL import Image, ImageDraw, ImageFilter

W, H = 192, 144
OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "imagenes")


def jitter(color, amount, rnd):
    return tuple(max(0, min(255, c + rnd.randint(-amount, amount))) for c in color)


def gradient(draw, top, bottom):
    for y in range(H):
        t = y / (H - 1)
        draw.line([(0, y), (W, y)], fill=tuple(int(a + (b - a) * t) for a, b in zip(top, bottom)))


def atardecer(draw, rnd):
    gradient(draw, jitter((250, 120, 60), 25, rnd), jitter((120, 40, 110), 25, rnd))
    r = rnd.randint(16, 28)
    x, y = rnd.randint(40, W - 40), rnd.randint(60, 95)
    draw.ellipse([x - r, y - r, x + r, y + r], fill=jitter((255, 210, 90), 15, rnd))
    draw.rectangle([0, 105, W, H], fill=jitter((60, 25, 50), 15, rnd))


def oceano(draw, rnd):
    gradient(draw, jitter((140, 200, 240), 20, rnd), jitter((20, 70, 150), 20, rnd))
    for i in range(rnd.randint(5, 9)):
        y = rnd.randint(60, H)
        x = rnd.randint(-40, W)
        draw.arc([x, y - 10, x + rnd.randint(40, 120), y + 10], 180, 360,
                 fill=jitter((230, 245, 255), 10, rnd), width=2)


def bosque(draw, rnd):
    gradient(draw, jitter((170, 215, 160), 20, rnd), jitter((40, 100, 50), 20, rnd))
    for i in range(rnd.randint(7, 12)):
        x = rnd.randint(0, W)
        h = rnd.randint(50, 100)
        draw.polygon([(x, H - h), (x - 18, H), (x + 18, H)], fill=jitter((20, 90, 35), 20, rnd))
        draw.rectangle([x - 3, H - 8, x + 3, H], fill=(90, 60, 30))


def desierto(draw, rnd):
    gradient(draw, jitter((240, 220, 170), 15, rnd), jitter((250, 200, 120), 15, rnd))
    for i in range(3):
        y = rnd.randint(70, 115)
        draw.ellipse([rnd.randint(-80, 60), y, rnd.randint(140, 280), y + 120],
                     fill=jitter((215, 165, 90), 20, rnd))


def noche(draw, rnd):
    gradient(draw, jitter((10, 15, 45), 8, rnd), jitter((35, 40, 95), 12, rnd))
    for i in range(rnd.randint(40, 80)):
        x, y = rnd.randint(0, W), rnd.randint(0, H - 30)
        draw.point((x, y), fill=(255, 255, 230))
    r = rnd.randint(10, 16)
    x, y = rnd.randint(20, W - 20), rnd.randint(15, 50)
    draw.ellipse([x - r, y - r, x + r, y + r], fill=(240, 240, 210))


def nieve(draw, rnd):
    gradient(draw, jitter((200, 225, 245), 12, rnd), jitter((245, 248, 252), 5, rnd))
    for i in range(rnd.randint(2, 4)):
        x = rnd.randint(0, W)
        h = rnd.randint(60, 110)
        draw.polygon([(x, H - h), (x - 70, H), (x + 70, H)], fill=jitter((150, 165, 190), 15, rnd))
        draw.polygon([(x, H - h), (x - 20, H - h + 30), (x + 20, H - h + 30)], fill=(255, 255, 255))


def flores(draw, rnd):
    gradient(draw, jitter((120, 190, 90), 20, rnd), jitter((60, 140, 50), 20, rnd))
    palette = [(230, 60, 90), (250, 200, 40), (200, 90, 220), (255, 140, 60), (250, 250, 250)]
    for i in range(rnd.randint(18, 30)):
        x, y = rnd.randint(0, W), rnd.randint(30, H)
        r = rnd.randint(4, 9)
        draw.ellipse([x - r, y - r, x + r, y + r], fill=jitter(rnd.choice(palette), 15, rnd))


def ciudad(draw, rnd):
    gradient(draw, jitter((150, 160, 175), 15, rnd), jitter((90, 95, 110), 15, rnd))
    x = 0
    while x < W:
        w = rnd.randint(16, 34)
        h = rnd.randint(40, 120)
        draw.rectangle([x, H - h, x + w, H], fill=jitter((60, 62, 70), 15, rnd))
        for wy in range(H - h + 6, H - 6, 10):
            for wx in range(x + 4, x + w - 4, 8):
                if rnd.random() < 0.55:
                    draw.rectangle([wx, wy, wx + 3, wy + 4], fill=(250, 215, 110))
        x += w + rnd.randint(1, 5)


CATEGORIAS = {
    "atardecer": atardecer,
    "oceano": oceano,
    "bosque": bosque,
    "desierto": desierto,
    "noche": noche,
    "nieve": nieve,
    "flores": flores,
    "ciudad": ciudad,
}


def main():
    os.makedirs(OUT, exist_ok=True)
    rnd = random.Random(2026)
    for nombre, pintar in CATEGORIAS.items():
        for i in range(1, 7):
            img = Image.new("RGB", (W, H))
            pintar(ImageDraw.Draw(img), rnd)
            img = img.filter(ImageFilter.SMOOTH)
            img.save(os.path.join(OUT, f"{nombre}_{i}.png"), optimize=True)
    print(f"{len(CATEGORIAS) * 6} imágenes en {OUT}")


if __name__ == "__main__":
    main()
