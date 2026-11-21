"""Detección MTCNN y embedding FaceNet, inferencia local sin servicios externos."""
import io
import threading
from PIL import Image, ImageOps, UnidentifiedImageError

from db import check_vector

MAX_IMAGE = 5 * 1024 * 1024
MAX_PIXELS = 12_000_000
MAX_UPLOAD = 15 * 1024 * 1024
MAX_UPLOAD_PIXELS = 32_000_000


def decode_image(content, max_bytes=MAX_IMAGE, max_pixels=MAX_PIXELS, max_side=960):
    if not content or len(content) > max_bytes:
        raise ValueError(f'Sube una imagen de hasta {max_bytes // (1024 * 1024)} MB.')
    try:
        with Image.open(io.BytesIO(content)) as original:
            if original.width * original.height > max_pixels:
                raise ValueError('Imagen demasiado grande. Reduce su resolución.')
            oriented = ImageOps.exif_transpose(original)
            if 'A' in oriented.getbands() or 'transparency' in oriented.info:
                image = oriented.convert('RGBA')
                image.thumbnail((max_side, max_side))
                result = Image.new('RGB', image.size, 'white')
                result.paste(image, mask=image.getchannel('A'))
                return result
            image = oriented.convert('RGB')
            image.thumbnail((max_side, max_side))
            return image
    except (UnidentifiedImageError, OSError, Image.DecompressionBombError) as e:
        raise ValueError('No se pudo abrir la imagen. Prueba con JPG, PNG o WebP.') from e


def normalize_upload(content):
    image = decode_image(content, MAX_UPLOAD, MAX_UPLOAD_PIXELS, 1600)
    output = io.BytesIO()
    image.save(output, format='JPEG', quality=90)
    return output.getvalue()


class FaceModel:
    def __init__(self):
        self.lock = threading.Lock()
        self.detector = self.network = self.torch = self.device = None

    def load(self):
        if self.network is not None:
            return
        import torch
        from facenet_pytorch import MTCNN, InceptionResnetV1
        self.torch = torch
        self.device = torch.device('cuda' if torch.cuda.is_available() else 'cpu')
        self.detector = MTCNN(image_size=160, margin=0, keep_all=True, device=self.device)
        self.network = InceptionResnetV1(pretrained='vggface2').eval().to(self.device)

    def embed(self, content):
        vector, _ = self.embed_with_box(content)
        return vector

    def embed_with_box(self, content):
        image = decode_image(content)
        with self.lock:
            self.load()
            with self.torch.inference_mode():
                boxes, _ = self.detector.detect(image)
                if boxes is None or len(boxes) == 0:
                    raise ValueError('No se detectó un rostro.')
                if len(boxes) != 1:
                    raise ValueError('Debe aparecer exactamente un rostro.')
                faces = self.detector.extract(image, boxes, None)
                vector = self.network(faces.to(self.device))[0].cpu().tolist()
                # Coordenadas relativas a la imagen ya orientada, no al recorte.
                x1, y1, x2, y2 = boxes[0]
                face_box = [max(0.0, min(1.0, float(v / size))) for v, size in
                            zip((x1, y1, x2, y2), (image.width, image.height, image.width, image.height))]
        return check_vector(vector), face_box
