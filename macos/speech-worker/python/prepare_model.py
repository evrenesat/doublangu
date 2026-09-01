#!/usr/bin/env python3
"""Download and activate one exact public model/tokenizer snapshot."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shutil
from pathlib import Path

from huggingface_hub import snapshot_download


MODEL_REPOSITORY = "mlx-community/chatterbox-multilingual-v3"
TOKENIZER_REPOSITORY = "mlx-community/S3TokenizerV2"


def digest_tree(root: Path) -> str:
    digest = hashlib.sha256()
    paths = (
        path
        for path in root.rglob("*")
        if path.is_file() and ".cache" not in path.relative_to(root).parts
    )
    for path in sorted(paths):
        digest.update(str(path.relative_to(root)).encode())
        digest.update(path.read_bytes())
    return digest.hexdigest()


def secure_tree(root: Path) -> None:
    for path in root.rglob("*"):
        if path.is_symlink():
            continue
        path.chmod(0o700 if path.is_dir() else 0o600)
    root.chmod(0o700)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--model-revision", required=True)
    parser.add_argument("--tokenizer-revision", required=True)
    parser.add_argument("--model-path", required=True, type=Path)
    parser.add_argument("--cache-path", required=True, type=Path)
    parser.add_argument("--receipt-path", required=True, type=Path)
    args = parser.parse_args()

    args.model_path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    args.cache_path.mkdir(parents=True, exist_ok=True, mode=0o700)
    temporary = args.model_path.with_name(args.model_path.name + ".partial")
    if temporary.exists():
        shutil.rmtree(temporary)
    temporary.mkdir(mode=0o700)
    tokenizer_cache = args.cache_path / "tokenizer"
    tokenizer_cache.mkdir(mode=0o700, exist_ok=True)
    model_snapshot = snapshot_download(
        repo_id=MODEL_REPOSITORY,
        revision=args.model_revision,
        cache_dir=str(args.cache_path),
        local_dir=str(temporary),
        allow_patterns=["model.safetensors", "tokenizer.json", "config.json", "Cangjie5_TC.json"],
    )
    tokenizer_snapshot = snapshot_download(
        repo_id=TOKENIZER_REPOSITORY,
        revision=args.tokenizer_revision,
        cache_dir=str(args.cache_path),
        local_dir=str(tokenizer_cache),
        allow_patterns=["model.safetensors", "config.json"],
    )
    model_root = Path(model_snapshot)
    tokenizer_root = Path(tokenizer_snapshot)
    if not (model_root / "model.safetensors").is_file() or not (model_root / "tokenizer.json").is_file() or not (tokenizer_root / "model.safetensors").is_file():
        raise SystemExit("required model files are missing")
    if args.model_path.exists():
        shutil.rmtree(args.model_path)
    os.replace(temporary, args.model_path)
    secure_tree(args.model_path)
    secure_tree(args.cache_path)
    secure_tree(tokenizer_root)
    receipt = {
        "schema_version": 1,
        "model_repository": MODEL_REPOSITORY,
        "model_revision": args.model_revision,
        "tokenizer_repository": TOKENIZER_REPOSITORY,
        "tokenizer_revision": args.tokenizer_revision,
        "model_tree_sha256": digest_tree(args.model_path),
        "tokenizer_tree_sha256": digest_tree(tokenizer_root),
    }
    args.receipt_path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    temporary_receipt = args.receipt_path.with_suffix(".partial")
    temporary_receipt.write_text(json.dumps(receipt, sort_keys=True, separators=(",", ":")) + "\n")
    temporary_receipt.chmod(0o600)
    os.replace(temporary_receipt, args.receipt_path)


if __name__ == "__main__":
    main()
