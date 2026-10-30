from __future__ import annotations

from collections import Counter, defaultdict
from dataclasses import dataclass
import heapq
import json
import math
from pathlib import Path
import re
from typing import Iterable, Mapping

FORMAT = "dbms-go-tfidf-v1"
TOKEN_RE = re.compile(r"[^\W_]+", re.UNICODE)


def tokenize(text: str) -> list[str]:
    return TOKEN_RE.findall(text.lower())


@dataclass(frozen=True)
class SearchHit:
    doc_id: str
    score: float



# TF - 42
def tf_weight(freq):
    if freq <= 0:
        return 0.0
    return 1.0 + math.log10(freq)


# IDF - 47
def idf_weight(total_docs, doc_frequency):
    if doc_frequency <= 0 or total_docs <= 0:
        return 0.0
    return math.log10(total_docs / doc_frequency)


# TF-IDF
def tfidf_weight(freq, total_docs, doc_frequency):
    return tf_weight(freq) * idf_weight(
        total_docs, doc_frequency
    )


class TFIDFRanker:
    
    def __init__(
        self,
        postings: Mapping[str, Mapping[str, int]],
        *,
        doc_ids: Iterable[str] | None = None,
        total_documents: int | None = None,
    ) -> None:
        explicit_ids = doc_ids is not None
        ids = [str(x) for x in doc_ids] if explicit_ids else []
        if len(set(ids)) != len(ids):
            raise ValueError("doc_ids contiene identificadores duplicados")
        known_ids = set(ids)
        normalized: dict[str, dict[str, int]] = {}
        discovered: set[str] = set()

        for term, entries in postings.items():
            if not isinstance(term, str) or tokenize(term) != [term]:
                raise ValueError(f"término SPIMI no normalizado: {term!r}")
            if not isinstance(entries, Mapping):
                raise TypeError("cada lista de postings debe ser {doc_id: tf}")
            new_entries: dict[str, int] = {}
            for raw_id, tf in entries.items():
                document_id = str(raw_id)
                if type(tf) is not int or tf <= 0:
                    raise ValueError("las frecuencias TF deben ser enteros positivos")
                if explicit_ids and document_id not in known_ids:
                    raise ValueError(f"documento {document_id!r} ausente de doc_ids")
                if document_id in new_entries:
                    raise ValueError(f"identificador duplicado: {document_id!r}")
                new_entries[document_id] = tf
                discovered.add(document_id)
            if new_entries:
                normalized[term] = new_entries

        if not explicit_ids:
            known_ids = discovered
        n = len(known_ids) if total_documents is None else total_documents
        if type(n) is not int or n < len(known_ids) or n < 0:
            raise ValueError("total_documents debe incluir todos los documentos conocidos")
        if explicit_ids and total_documents is not None and n != len(known_ids):
            raise ValueError("total_documents y doc_ids deben tener el mismo tamaño")

        self.postings = normalized
        self.doc_ids = known_ids
        self.total_documents = n
        self.df = {t: len(entries) for t, entries in normalized.items()}
        self.document_norm: dict[str, float] = {doc_id: 0.0 for doc_id in known_ids}
        
        # Norma euclidiana de todos los vectores TF-IDF, calculada una sola vez.
        for term, entries in normalized.items():
            idf = self.idf(term)
            for doc_id, freq in entries.items():
                weight = tf_weight(freq) * idf
                self.document_norm[doc_id] += weight * weight
                
        for doc_id, squared_norm in self.document_norm.items():
            self.document_norm[doc_id] = math.sqrt(squared_norm)
    

    @classmethod
    def from_documents(cls, documents: Iterable[tuple[str, str]]) -> "TFIDFRanker":
        """Modo de demostración sin SPIMI; no reemplaza el SPIMI"""
        postings: dict[str, dict[str, int]] = defaultdict(dict)
        ids: list[str] = []
        used: set[str] = set()
        for raw_id, text in documents:
            doc_id = str(raw_id)
            if doc_id in used:
                raise ValueError(f"ID repetido: {doc_id}")
            if not isinstance(text, str):
                raise TypeError("el contenido del documento debe ser texto")
            used.add(doc_id)
            ids.append(doc_id)
            for term, tf in Counter(tokenize(text)).items():
                postings[term][doc_id] = tf
        return cls(postings, doc_ids=ids)

    @classmethod
    def from_tokenized_documents(
        cls, documents: Iterable[tuple[str, Iterable[str]]]
    ) -> "TFIDFRanker":
        postings: dict[str, dict[str, int]] = defaultdict(dict)
        ids: list[str] = []
        seen: set[str] = set()
        for raw_id, tokens in documents:
            doc_id = str(raw_id)
            if doc_id in seen:
                raise ValueError(f"ID repetido: {doc_id}")
            if isinstance(tokens, (str, bytes)):
                raise TypeError("tokens debe ser una secuencia de términos, no texto")
            terms = list(tokens)
            for term in terms:
                if not isinstance(term, str) or tokenize(term) != [term]:
                    raise ValueError(f"token no normalizado: {term!r}")
            seen.add(doc_id)
            ids.append(doc_id)
            for term, tf in Counter(terms).items():
                postings[term][doc_id] = tf
        return cls(postings, doc_ids=ids)

    def idf(self, term: str) -> float:
        return idf_weight(
            self.total_documents,
            self.df.get(term, 0)
    )
    
    def search(self, query: str, top_k: int = 10) -> list[SearchHit]:
        return self.search_tokens(tokenize(query), top_k)

    def search_tokens(
        self, query_tokens: Iterable[str], top_k: int = 10
    ) -> list[SearchHit]:
        """Consulta usando exactamente los términos normalizados recibidos."""
        if top_k < 0:
            raise ValueError("top_k no puede ser negativo")
        if top_k == 0 or not self.total_documents:
            return []
        if isinstance(query_tokens, (str, bytes)):
            raise TypeError("query_tokens debe ser una secuencia de términos")
        terms = list(query_tokens)
        for term in terms:
            if not isinstance(term, str) or tokenize(term) != [term]:
                raise ValueError(f"token de consulta no normalizado: {term!r}")
        query_tf = Counter(terms)
        query_weights: dict[str, float] = {}
        for term, tf in query_tf.items():
            # palabras desconocidas no forman parte del vocabulario entrenado.
            if term in self.postings:
                query_weights[term] = tf_weight(tf) * self.idf(term)
        if not query_weights:
            return []

        norm_query = math.sqrt(sum(weight * weight for weight in query_weights.values()))
        dots: dict[str, float] = defaultdict(float)
        for term, query_weight in query_weights.items():
            idf = self.idf(term)
            for doc_id, doc_tf in self.postings[term].items():
                doc_weight = tf_weight(doc_tf) * idf
                dots[doc_id] += doc_weight * query_weight

        def ranked_hits():
            for doc_id, dot in dots.items():
                norm_doc = self.document_norm[doc_id]
                if dot > 0.0 and norm_doc > 0.0:
                    score = dot / (norm_doc * norm_query)
                    # Floating-point
                    yield SearchHit(doc_id, min(1.0, max(0.0, score)))

        # top-k sin ordenar todos los candidatos // empates por ID ascendente.
        return heapq.nsmallest(top_k, ranked_hits(), key=lambda h: (-h.score, h.doc_id))

    def save(self, path: str | Path) -> None:
        destination = Path(path)
        destination.parent.mkdir(parents=True, exist_ok=True)
        data = {
            "format": FORMAT,
            "total_documents": self.total_documents,
            "doc_ids": sorted(self.doc_ids) if self.total_documents == len(self.doc_ids) else None,
            "postings": {t: dict(sorted(d.items())) for t, d in sorted(self.postings.items())},
        }
        with destination.open("w", encoding="utf-8") as f:
            json.dump(data, f, ensure_ascii=False, separators=(",", ":"))

    @classmethod
    def load(cls, path: str | Path) -> "TFIDFRanker":
        with Path(path).open(encoding="utf-8") as f:
            data = json.load(f)
        if data.get("format") != FORMAT:
            raise ValueError(f"se esperaba un índice {FORMAT}")
        return cls(data["postings"], doc_ids=data.get("doc_ids"),
                   total_documents=data["total_documents"])

    @classmethod
    def from_spimi_file(cls, path: str | Path) -> "TFIDFRanker":
        with Path(path).open(encoding="utf-8") as f:
            data = json.load(f)
        if "postings" not in data:
            raise ValueError("SPIMI debe exportar 'postings'")
        return cls(data["postings"], doc_ids=data.get("doc_ids"),
                   total_documents=data.get("total_documents"))

# ranker = TFIDFRanker.from_spimi_file("indice_spimi.json")
# resultados = ranker.search("gato", top_k=3)