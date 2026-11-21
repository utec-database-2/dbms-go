import os
from time import perf_counter

import librosa
import numpy as np

SAMPLE_RATE = 16000
SEGMENT_SECONDS = 5
HOP_SECONDS = 2.5
N_MFCC = 20
N_BLOCKS = 8
VECTOR_DIM = N_MFCC * N_BLOCKS * 2
BATCH_SIZE = 8


def load_audio(path):
    audio, sr = librosa.load(path, sr=SAMPLE_RATE, mono=True)
    if not np.all(np.isfinite(audio)):
        raise ValueError("El audio contiene valores no validos")
    return audio, sr


def _pool(mfcc):
    # (..., 20, frames) -> (..., 320), same ordering as AudioMatch v1.
    blocks = np.array_split(mfcc, N_BLOCKS, axis=-1)
    vector = np.concatenate(
        [x for block in blocks for x in (block.mean(axis=-1), block.std(axis=-1))],
        axis=-1,
    ).astype(np.float32)
    norms = np.linalg.norm(vector, axis=-1, keepdims=True)
    return np.divide(vector, norms, out=np.zeros_like(vector), where=norms > 0)


def extract_features(segment):
    """Original single-window implementation, retained for verification/fallback."""
    mfcc = librosa.feature.mfcc(
        y=segment, sr=SAMPLE_RATE, n_mfcc=N_MFCC + 1,
        n_fft=1024, hop_length=256, n_mels=64,
    )[1:]
    return _pool(mfcc)


def _extract_batch(segments):
    # The peak dB clipping must be independent for EACH segment.
    mel = librosa.feature.melspectrogram(
        y=segments, sr=SAMPLE_RATE, n_fft=1024, hop_length=256,
        n_mels=64,
    )
    db = librosa.power_to_db(mel, top_db=None)
    db = np.maximum(db, db.max(axis=(-2, -1), keepdims=True) - 80.0)
    mfcc = librosa.feature.mfcc(S=db, n_mfcc=N_MFCC + 1)[..., 1:, :]
    return _pool(mfcc)


def split_audio(audio, hop_seconds=HOP_SECONDS, mode=None):
    size = int(SEGMENT_SECONDS * SAMPLE_RATE)
    hop = int(hop_seconds * SAMPLE_RATE)
    if hop < 1 or hop > size:
        raise ValueError("Desplazamiento invalido")
    if len(audio) < size:
        raise ValueError("El audio debe durar al menos 5 segundos")
    starts = list(range(0, len(audio) - size + 1, hop))
    if starts[-1] != len(audio) - size:
        starts.append(len(audio) - size)
    starts = [s for s in starts if np.sqrt(np.mean(audio[s:s + size] ** 2)) >= 1e-5]
    mode = mode or os.getenv("AUDIO_FEATURE_MODE", "batch")
    if mode not in {"batch", "legacy"}:
        raise ValueError("Modo de caracteristicas invalido")
    results = []
    for offset in range(0, len(starts), BATCH_SIZE):
        batch_starts = starts[offset:offset + BATCH_SIZE]
        batch = np.stack([audio[s:s + size] for s in batch_starts])
        vectors = (_extract_batch(batch) if mode == "batch" else
                   np.stack([extract_features(segment) for segment in batch]))
        for start, vector in zip(batch_starts, vectors):
            results.append({"start": start / SAMPLE_RATE,
                            "end": (start + size) / SAMPLE_RATE,
                            "vector": vector.tolist()})
    return results


def waveform(audio, points=420):
    peaks = np.array([float(np.max(np.abs(block))) if len(block) else 0.0
                      for block in np.array_split(audio, points)])
    peak = peaks.max(initial=0.0)
    return (peaks / peak if peak else peaks).round(4).tolist()


def process_audio(path, hop_seconds=HOP_SECONDS, mode=None):
    start = perf_counter()
    audio, sr = load_audio(path)
    loaded = perf_counter()
    segments = split_audio(audio, hop_seconds, mode)
    featured = perf_counter()
    return {"duration": len(audio) / sr, "segments": segments,
            "waveform": waveform(audio),
            "load_ms": (loaded - start) * 1000,
            "features_ms": (featured - loaded) * 1000}


def warmup():
    t = np.arange(SAMPLE_RATE * SEGMENT_SECONDS, dtype=np.float32) / SAMPLE_RATE
    segment = (0.1 * np.sin(2 * np.pi * 440 * t)).astype(np.float32)
    if os.getenv("AUDIO_FEATURE_MODE", "batch") == "legacy":
        extract_features(segment)
    else:
        _extract_batch(np.stack([segment, segment]))
