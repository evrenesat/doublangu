#!/usr/bin/env python3
"""Loopback-only Chatterbox service.

The Swift parent supplies the immutable model/reference identities. This
process never receives or stores perimeter or worker credentials.
"""

from __future__ import annotations

import argparse
import io
import json
import os
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from threading import Lock
from typing import Any

import numpy as np


MAX_REQUEST_BYTES = 64 * 1024
MAX_RESPONSE_BYTES = 64 * 1024 * 1024
MAX_TEXT_CHARS = 32_000
MODEL_REPOSITORY = "mlx-community/chatterbox-multilingual-v3"
TOKENIZER_REPOSITORY = "mlx-community/S3TokenizerV2"


class Configuration:
    def __init__(
        self,
        model_path: Path,
        model_revision: str,
        reference_audio: Path,
        tokenizer_revision: str,
    ) -> None:
        self.model_path = model_path
        self.model_revision = model_revision
        self.reference_audio = reference_audio
        self.tokenizer_revision = tokenizer_revision


class ChatterboxRuntime:
    def __init__(self, configuration: Configuration) -> None:
        self.configuration = configuration
        self._model: Any | None = None
        self._lock = Lock()

    @property
    def loaded(self) -> bool:
        return self._model is not None

    def model(self) -> Any:
        with self._lock:
            if self._model is None:
                self._model = self._load()
            return self._model

    def _load(self) -> Any:
        if not self.configuration.model_path.is_dir():
            raise RuntimeError("model directory is not ready")
        if not self.configuration.reference_audio.is_file():
            raise RuntimeError("reference audio is not ready")

        # mlx-audio 0.4.7 loads the tokenizer from its default repository.
        # Pin that lookup before importing the model hook so the child cannot
        # silently drift to a different tokenizer revision.
        import huggingface_hub

        snapshot_download = huggingface_hub.snapshot_download

        def pinned_snapshot_download(repo_id: str, *args: Any, **kwargs: Any) -> str:
            if repo_id == TOKENIZER_REPOSITORY:
                kwargs["revision"] = self.configuration.tokenizer_revision
            return snapshot_download(repo_id, *args, **kwargs)

        huggingface_hub.snapshot_download = pinned_snapshot_download
        from mlx_audio.tts.utils import load_model

        return load_model(self.configuration.model_path, lazy=False, strict=True)

    def generate(self, text: str) -> bytes:
        from mlx_audio import audio_io

        result = next(
            self.model().generate(
                text,
                ref_audio=str(self.configuration.reference_audio),
                lang_code="nl",
                max_tokens=1000,
                verbose=False,
            )
        )
        audio = np.asarray(result.audio)
        if audio.ndim != 1:
            audio = np.squeeze(audio)
        if audio.ndim != 1 or audio.size == 0 or not np.isfinite(audio).all():
            raise RuntimeError("model returned invalid audio")
        if audio.size > 24000 * 180:
            raise RuntimeError("model returned overlong audio")
        output = io.BytesIO()
        audio_io.write(output, audio, samplerate=24000, format="wav")
        value = output.getvalue()
        if len(value) < 44 or len(value) > MAX_RESPONSE_BYTES or value[:4] != b"RIFF" or value[8:12] != b"WAVE":
            raise RuntimeError("model returned invalid WAV")
        return value


class Handler(BaseHTTPRequestHandler):
    server_version = "DoublanguChatterbox/1"

    def log_message(self, _format: str, *_args: Any) -> None:
        # Never put request text, reference paths, or model details in stdout.
        return

    @property
    def runtime(self) -> ChatterboxRuntime:
        return self.server.runtime  # type: ignore[attr-defined]

    def do_GET(self) -> None:
        if self.path != "/health":
            self._send_json(404, {"error": "not_found"})
            return
        self._send_json(200, {"status": "ok", "model_loaded": self.runtime.loaded})

    def do_POST(self) -> None:
        if self.path != "/generate":
            self._send_json(404, {"error": "not_found"})
            return
        try:
            request = self._request_json()
            expected = {"text", "lang_code", "model_revision", "reference_audio_path"}
            if set(request) != expected:
                raise ValueError("unexpected request fields")
            text = request["text"]
            if not isinstance(text, str) or not text or len(text) > MAX_TEXT_CHARS:
                raise ValueError("invalid text")
            if request["lang_code"] != "nl":
                raise ValueError("invalid language")
            config = self.runtime.configuration
            if request["model_revision"] != config.model_revision:
                raise ValueError("model revision mismatch")
            if request["reference_audio_path"] != str(config.reference_audio):
                raise ValueError("reference path mismatch")
            value = self.runtime.generate(text)
            self.send_response(200)
            self.send_header("Content-Type", "audio/wav")
            self.send_header("Content-Length", str(len(value)))
            self.end_headers()
            self.wfile.write(value)
        except ValueError as error:
            self._send_json(400, {"error": str(error)})
        except Exception:
            self._send_json(500, {"error": "generation_failed"})

    def _request_json(self) -> dict[str, Any]:
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError as error:
            raise ValueError("invalid content length") from error
        if length <= 0 or length > MAX_REQUEST_BYTES:
            raise ValueError("request too large")
        body = self.rfile.read(length)
        if len(body) != length:
            raise ValueError("truncated request")

        def pairs(pairs_list: list[tuple[str, Any]]) -> dict[str, Any]:
            value: dict[str, Any] = {}
            for key, item in pairs_list:
                if key in value:
                    raise ValueError("duplicate request field")
                value[key] = item
            return value

        try:
            value = json.loads(body.decode("utf-8"), object_pairs_hook=pairs, parse_constant=lambda _: (_ for _ in ()).throw(ValueError("invalid number")))
        except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as error:
            raise ValueError("invalid JSON") from error
        if not isinstance(value, dict):
            raise ValueError("JSON object required")
        return value

    def _send_json(self, status: int, value: dict[str, Any]) -> None:
        body = json.dumps(value, separators=(",", ":")).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", required=True)
    parser.add_argument("--port", required=True, type=int)
    parser.add_argument("--model-path", required=True, type=Path)
    parser.add_argument("--model-revision", required=True)
    parser.add_argument("--reference-audio", required=True, type=Path)
    parser.add_argument("--tokenizer-revision", required=True)
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    if args.host != "127.0.0.1":
        raise SystemExit("only loopback binding is allowed")
    if not args.model_revision or not args.tokenizer_revision:
        raise SystemExit("pinned revisions are required")
    os.environ.setdefault("PYTHONNOUSERSITE", "1")
    runtime = ChatterboxRuntime(Configuration(args.model_path, args.model_revision, args.reference_audio, args.tokenizer_revision))
    server = HTTPServer((args.host, args.port), Handler)
    server.runtime = runtime  # type: ignore[attr-defined]
    server.serve_forever()


if __name__ == "__main__":
    main()
