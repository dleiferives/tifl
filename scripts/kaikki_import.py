#!/usr/bin/env python3
"""Download and import a kaikki.org Wiktextract dictionary dump into tifl."""

import argparse
import os
import shutil
import subprocess
import sys
import threading
import time
import urllib.request
from datetime import datetime
from pathlib import Path

# ── ANSI colours ─────────────────────────────────────────────────────────────

RESET = "\033[0m"
BOLD = "\033[1m"
DIM = "\033[2m"
GREEN = "\033[32m"
CYAN = "\033[36m"
YELLOW = "\033[33m"
RED = "\033[31m"
BLUE = "\033[34m"

NO_COLOR = not sys.stdout.isatty() or os.environ.get("NO_COLOR")


def c(color: str, text: str) -> str:
    return text if NO_COLOR else f"{color}{text}{RESET}"


def bold(text: str) -> str:
    return text if NO_COLOR else f"{BOLD}{text}{RESET}"


def dim(text: str) -> str:
    return text if NO_COLOR else f"{DIM}{text}{RESET}"


def ok(msg: str) -> None:
    print(c(GREEN, "✓ ") + msg)


def info(msg: str) -> None:
    print(c(CYAN, "→ ") + msg)


def warn(msg: str) -> None:
    print(c(YELLOW, "! ") + msg, file=sys.stderr)


def err(msg: str) -> None:
    print(c(RED, "✗ ") + msg, file=sys.stderr)


# ── Language catalogue ────────────────────────────────────────────────────────

# Maps tifl language code → (display name, kaikki folder name, kaikki file stem)
LANGUAGES: dict[str, tuple[str, str, str]] = {
    "el": ("Greek",   "Greek",   "kaikki.org-dictionary-Greek"),
    "ar": ("Arabic",  "Arabic",  "kaikki.org-dictionary-Arabic"),
    "de": ("German",  "German",  "kaikki.org-dictionary-German"),
    "es": ("Spanish", "Spanish", "kaikki.org-dictionary-Spanish"),
    "fr": ("French",  "French",  "kaikki.org-dictionary-French"),
    "it": ("Italian", "Italian", "kaikki.org-dictionary-Italian"),
    "ja": ("Japanese","Japanese","kaikki.org-dictionary-Japanese"),
    "la": ("Latin",   "Latin",   "kaikki.org-dictionary-Latin"),
    "ru": ("Russian", "Russian", "kaikki.org-dictionary-Russian"),
    "zh": ("Chinese", "Chinese", "kaikki.org-dictionary-Chinese"),
}

KAIKKI_BASE = "https://kaikki.org/dictionary"

# ── Helpers ───────────────────────────────────────────────────────────────────

def kaikki_url(lang_code: str) -> str:
    _, folder, stem = LANGUAGES[lang_code]
    return f"{KAIKKI_BASE}/{folder}/{stem}.jsonl.gz"


def find_import_binary() -> Path | None:
    """Look for the kaikki-import binary next to this script, in bin/, or on PATH."""
    script_dir = Path(__file__).parent
    candidates = [
        script_dir.parent / "bin" / "tifl-kaikki-import",
        script_dir.parent / "bin" / "kaikki-import",
    ]
    for p in candidates:
        if p.exists() and os.access(p, os.X_OK):
            return p
    found = shutil.which("tifl-kaikki-import") or shutil.which("kaikki-import")
    return Path(found) if found else None


def default_db() -> Path:
    """Guess the default SQLite path from tifl.yaml if present."""
    config = Path(__file__).parent.parent / "tifl.yaml"
    if config.exists():
        for line in config.read_text().splitlines():
            stripped = line.strip()
            if stripped.startswith("db_path:"):
                val = stripped.split(":", 1)[1].split("#")[0].strip().strip('"').strip("'")
                if val:
                    return Path(__file__).parent.parent / val
    return Path(__file__).parent.parent / "data" / "tifl.db"


def run_with_spinner(label: str, cmd: list[str]) -> int:
    """Run cmd, streaming its stdout as spinner status. Returns exit code."""
    frames = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"]
    start = time.monotonic()
    proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1)
    status: list[str] = [""]
    done = threading.Event()
    width = 70

    def spin() -> None:
        i = 0
        while not done.is_set():
            elapsed = time.monotonic() - start
            frame = frames[i % len(frames)] if not NO_COLOR else "-"
            line = f"  {c(CYAN, frame)} {label}  {dim(f'{elapsed:.0f}s')}  {dim(status[0])}"
            print(f"\r{line:<{width}}", end="", flush=True)
            i += 1
            time.sleep(0.1)

    spinner = threading.Thread(target=spin, daemon=True)
    spinner.start()

    assert proc.stdout
    for raw in proc.stdout:
        status[0] = raw.rstrip()

    proc.wait()
    done.set()
    spinner.join()

    elapsed = time.monotonic() - start
    print(f"\r  {c(GREEN, '✓')} {label}  {dim(f'{elapsed:.0f}s')}{' ' * 30}")
    return proc.returncode


def download(url: str, dest: Path) -> None:
    """Stream-download url → dest with a simple progress bar."""
    dest.parent.mkdir(parents=True, exist_ok=True)
    tmp = dest.with_suffix(dest.suffix + ".part")

    def reporthook(block_num: int, block_size: int, total_size: int) -> None:
        downloaded = block_num * block_size
        if total_size > 0:
            pct = min(100, downloaded * 100 // total_size)
            bar_width = 40
            filled = bar_width * pct // 100
            bar = "█" * filled + "░" * (bar_width - filled)
            mb_done = downloaded / 1_048_576
            mb_total = total_size / 1_048_576
            print(
                f"\r  {c(BLUE, bar)} {pct:3d}%  {mb_done:.1f}/{mb_total:.1f} MB",
                end="",
                flush=True,
            )
        else:
            mb_done = downloaded / 1_048_576
            print(f"\r  {mb_done:.1f} MB downloaded", end="", flush=True)

    try:
        urllib.request.urlretrieve(url, tmp, reporthook)
        print()  # newline after progress bar
        tmp.rename(dest)
    except Exception:
        tmp.unlink(missing_ok=True)
        raise


# ── Interactive prompts (no extra deps) ──────────────────────────────────────

def prompt_choice(question: str, choices: list[tuple[str, str]], default: int = 0) -> str:
    """Simple numbered-menu prompt. Returns the key of the chosen item."""
    print()
    print(bold(question))
    for i, (key, label) in enumerate(choices):
        marker = c(GREEN, "▶") if i == default else " "
        print(f"  {marker} {c(CYAN, str(i + 1))}) {label}  {dim(key)}")
    print()
    while True:
        try:
            raw = input(f"  Enter number [{default + 1}]: ").strip()
            if raw == "":
                return choices[default][0]
            n = int(raw) - 1
            if 0 <= n < len(choices):
                return choices[n][0]
        except (ValueError, KeyboardInterrupt):
            pass
        print(c(RED, f"  Please enter 1–{len(choices)}"))


def prompt_path(question: str, default: Path) -> Path:
    print()
    print(bold(question))
    raw = input(f"  [{default}]: ").strip()
    return Path(raw) if raw else default


def prompt_yes(question: str, default: bool = True) -> bool:
    hint = "[Y/n]" if default else "[y/N]"
    try:
        raw = input(f"\n{bold(question)} {hint} ").strip().lower()
    except KeyboardInterrupt:
        print()
        sys.exit(0)
    if raw == "":
        return default
    return raw in ("y", "yes")


# ── Core action ───────────────────────────────────────────────────────────────

def run(
    lang_code: str,
    dump_file: Path | None,
    db_path: Path,
    binary: Path,
    dataset_version: str,
    dry_run: bool,
) -> None:
    if dump_file is None:
        url = kaikki_url(lang_code)
        default_dir = Path(__file__).parent.parent / "data" / "kaikki"
        _, _, stem = LANGUAGES[lang_code]
        dump_file = default_dir / (stem + ".jsonl.gz")

        if dump_file.exists():
            size_mb = dump_file.stat().st_size / 1_048_576
            if not prompt_yes(
                f"Found existing dump at {dump_file} ({size_mb:.1f} MB). Re-download?",
                default=False,
            ):
                info(f"Using cached file: {dump_file}")
            else:
                if dry_run:
                    info(f"Would re-download {url}")
                else:
                    info(f"Downloading {url}")
                    download(url, dump_file)
                    ok("Download complete")
        else:
            if dry_run:
                info(f"Would download {url}")
            else:
                info(f"Downloading {url}")
                download(url, dump_file)
                ok("Download complete")

    print()
    info(f"Importing {dump_file.name} → {db_path}")

    cmd = [
        str(binary),
        "-input", str(dump_file),
        "-language", lang_code,
        "-db", str(db_path),
    ]
    if dataset_version:
        cmd += ["-dataset-version", dataset_version]

    print(dim("  " + " ".join(cmd)))
    print()

    if dry_run:
        warn("Dry run — skipping import.")
        return

    db_path.parent.mkdir(parents=True, exist_ok=True)
    rc = run_with_spinner("Importing…", cmd)
    if rc != 0:
        err(f"Import failed (exit {rc})")
        sys.exit(rc)
    ok("Import complete")


# ── Entry point ───────────────────────────────────────────────────────────────

def main() -> None:
    parser = argparse.ArgumentParser(
        description="Download and import a kaikki.org dictionary dump into tifl.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="Run with no arguments for interactive mode.",
    )
    parser.add_argument(
        "-l", "--language",
        metavar="CODE",
        choices=list(LANGUAGES),
        help=f"language code: {', '.join(LANGUAGES)}",
    )
    parser.add_argument(
        "-i", "--input",
        metavar="FILE",
        help="use an already-downloaded .jsonl or .jsonl.gz file",
    )
    parser.add_argument(
        "--db",
        metavar="PATH",
        help="SQLite DB path (default: auto-detected from tifl.yaml)",
    )
    parser.add_argument(
        "--dataset-version",
        metavar="VER",
        default=datetime.today().strftime("%Y-%m-%d"),
        help="version string for the import audit row (default: today)",
    )
    parser.add_argument(
        "--binary",
        metavar="PATH",
        help="path to the tifl-kaikki-import binary",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="download only; skip the import step",
    )
    args = parser.parse_args()

    # ── Banner ────────────────────────────────────────────────────────────────
    print()
    print(bold(c(CYAN, "  kaikki → tifl importer")))
    print(dim("  Wiktextract dictionary dump downloader + importer"))
    print()

    # ── Resolve binary ────────────────────────────────────────────────────────
    if args.binary:
        binary = Path(args.binary)
        if not binary.exists():
            err(f"Binary not found: {binary}")
            sys.exit(1)
    else:
        binary = find_import_binary()
        if binary is None:
            err("Could not find tifl-kaikki-import. Build it with: make build")
            err("Or pass --binary /path/to/tifl-kaikki-import")
            sys.exit(1)
    ok(f"Binary: {binary}")

    # ── Language ──────────────────────────────────────────────────────────────
    if args.language:
        lang_code = args.language
        ok(f"Language: {LANGUAGES[lang_code][0]} ({lang_code})")
    else:
        choices = [(code, name) for code, (name, _, _) in LANGUAGES.items()]
        lang_code = prompt_choice("Which language?", choices, default=0)

    # ── DB path ───────────────────────────────────────────────────────────────
    db_path = Path(args.db) if args.db else default_db()
    if not args.db:
        db_path = prompt_path("SQLite database path", default=db_path)
    ok(f"Database: {db_path}")

    # ── Input file ────────────────────────────────────────────────────────────
    input_file = Path(args.input) if args.input else None

    # ── Go ────────────────────────────────────────────────────────────────────
    try:
        run(
            lang_code=lang_code,
            dump_file=input_file,
            db_path=db_path,
            binary=binary,
            dataset_version=args.dataset_version,
            dry_run=args.dry_run,
        )
    except KeyboardInterrupt:
        print()
        warn("Cancelled.")
        sys.exit(130)
    except Exception as e:
        err(str(e))
        sys.exit(1)


if __name__ == "__main__":
    main()
