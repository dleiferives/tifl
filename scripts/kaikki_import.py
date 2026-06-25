#!/usr/bin/env python3
"""Download and import kaikki.org Wiktextract dictionary dumps into tifl.

Three steps, all optional:
  1. English Wiktionary → import (English definitions, source=wiktionary)
  2. Native Wiktionary  → import (definitions in target language, source=wiktionary-native)
  3. Translate missing  → LLM translates native-only entries (source=wiktionary-translated)
"""

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


def section(title: str) -> None:
    print()
    print(bold(c(BLUE, f"── {title} ")))
    print()


# ── Language catalogue ────────────────────────────────────────────────────────

# Maps tifl language code →
#   (display name,
#    English-Wiktionary kaikki stem,
#    native-Wiktionary kaikki stem or None if unknown)
LANGUAGES: dict[str, tuple[str, str, str | None]] = {
    "el": ("Greek",   "kaikki.org-dictionary-Greek",   "kaikki.org-dictionary-Greek-el"),
    "ar": ("Arabic",  "kaikki.org-dictionary-Arabic",  "kaikki.org-dictionary-Arabic-ar"),
    "de": ("German",  "kaikki.org-dictionary-German",  "kaikki.org-dictionary-German-de"),
    "es": ("Spanish", "kaikki.org-dictionary-Spanish", "kaikki.org-dictionary-Spanish-es"),
    "fr": ("French",  "kaikki.org-dictionary-French",  "kaikki.org-dictionary-French-fr"),
    "it": ("Italian", "kaikki.org-dictionary-Italian", "kaikki.org-dictionary-Italian-it"),
    "ja": ("Japanese","kaikki.org-dictionary-Japanese","kaikki.org-dictionary-Japanese-ja"),
    "la": ("Latin",   "kaikki.org-dictionary-Latin",   None),
    "ru": ("Russian", "kaikki.org-dictionary-Russian", "kaikki.org-dictionary-Russian-ru"),
    "zh": ("Chinese", "kaikki.org-dictionary-Chinese", "kaikki.org-dictionary-Chinese-zh"),
}

KAIKKI_BASE = "https://kaikki.org/dictionary"

# ── Helpers ───────────────────────────────────────────────────────────────────

def _lang_folder(lang_code: str) -> str:
    name, _, _ = LANGUAGES[lang_code]
    return name


def kaikki_url_english(lang_code: str) -> str:
    _, stem, _ = LANGUAGES[lang_code]
    folder = _lang_folder(lang_code)
    return f"{KAIKKI_BASE}/{folder}/{stem}.jsonl.gz"


def kaikki_url_native(lang_code: str) -> str | None:
    _, _, native_stem = LANGUAGES[lang_code]
    if native_stem is None:
        return None
    folder = _lang_folder(lang_code)
    return f"{KAIKKI_BASE}/{folder}/{lang_code}/{native_stem}.jsonl.gz"


def find_binary(name: str, aliases: list[str] | None = None) -> Path | None:
    script_dir = Path(__file__).parent
    names = [name] + (aliases or [])
    for n in names:
        for candidate in [
            script_dir.parent / "bin" / f"tifl-{n}",
            script_dir.parent / "bin" / n,
        ]:
            if candidate.exists() and os.access(candidate, os.X_OK):
                return candidate
        found = shutil.which(f"tifl-{n}") or shutil.which(n)
        if found:
            return Path(found)
    return None


def default_db() -> Path:
    config = Path(__file__).parent.parent / "tifl.yaml"
    if config.exists():
        for line in config.read_text().splitlines():
            stripped = line.strip()
            if stripped.startswith("db_path:"):
                val = stripped.split(":", 1)[1].split("#")[0].strip().strip('"').strip("'")
                if val:
                    return Path(__file__).parent.parent / val
    return Path(__file__).parent.parent / "data" / "tifl.db"


# ── Spinner ───────────────────────────────────────────────────────────────────

def run_with_spinner(label: str, cmd: list[str]) -> int:
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


# ── Download ──────────────────────────────────────────────────────────────────

def download(url: str, dest: Path) -> None:
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
            print(f"\r  {c(BLUE, bar)} {pct:3d}%  {mb_done:.1f}/{mb_total:.1f} MB", end="", flush=True)
        else:
            mb_done = downloaded / 1_048_576
            print(f"\r  {mb_done:.1f} MB downloaded", end="", flush=True)

    try:
        urllib.request.urlretrieve(url, tmp, reporthook)
        print()
        tmp.rename(dest)
    except Exception:
        tmp.unlink(missing_ok=True)
        raise


def ensure_dump(url: str, dest: Path, dry_run: bool) -> bool:
    """Download dump if needed. Returns True if the file is ready to import."""
    if dest.exists():
        size_mb = dest.stat().st_size / 1_048_576
        if not prompt_yes(f"Found {dest.name} ({size_mb:.1f} MB). Re-download?", default=False):
            info(f"Using cached: {dest}")
            return True
    if dry_run:
        info(f"Would download: {url}")
        return False
    info(f"Downloading {url}")
    try:
        download(url, dest)
    except Exception as e:
        err(f"Download failed: {e}")
        return False
    ok("Download complete")
    return True


# ── Prompts ───────────────────────────────────────────────────────────────────

def prompt_choice(question: str, choices: list[tuple[str, str]], default: int = 0) -> str:
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


# ── Import step ───────────────────────────────────────────────────────────────

def run_import(
    label: str,
    dump_file: Path,
    lang_code: str,
    db_path: Path,
    binary: Path,
    dataset_version: str,
    native: bool,
    dry_run: bool,
) -> None:
    cmd = [
        str(binary),
        "-input", str(dump_file),
        "-language", lang_code,
        "-db", str(db_path),
        "-dataset-version", dataset_version,
    ]
    if native:
        cmd.append("-native")

    print(dim("  " + " ".join(cmd)))
    print()
    if dry_run:
        warn("Dry run — skipping import.")
        return

    db_path.parent.mkdir(parents=True, exist_ok=True)
    rc = run_with_spinner(label, cmd)
    if rc != 0:
        err(f"Import failed (exit {rc})")
        sys.exit(rc)
    ok(f"{label} complete")


# ── Translate step ────────────────────────────────────────────────────────────

def run_translate(
    lang_code: str,
    db_path: Path,
    translate_binary: Path,
    batch: int,
    limit: int,
    llm_url: str,
    llm_model: str,
    dry_run: bool,
) -> None:
    cmd = [
        str(translate_binary),
        "-language", lang_code,
        "-db", str(db_path),
        "-batch", str(batch),
    ]
    if limit > 0:
        cmd += ["-limit", str(limit)]
    if llm_url:
        cmd += ["-llm-url", llm_url]
    if llm_model:
        cmd += ["-llm-model", llm_model]

    print(dim("  " + " ".join(cmd)))
    print()
    if dry_run:
        warn("Dry run — skipping translation.")
        return

    rc = run_with_spinner("Translating…", cmd)
    if rc != 0:
        err(f"Translation failed (exit {rc})")
        sys.exit(rc)
    ok("Translation complete")


# ── Entry point ───────────────────────────────────────────────────────────────

def main() -> None:
    parser = argparse.ArgumentParser(
        description="Download and import kaikki.org dictionary dumps into tifl.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__,
    )
    parser.add_argument("-l", "--language", metavar="CODE", choices=list(LANGUAGES),
                        help=f"language code: {', '.join(LANGUAGES)}")
    parser.add_argument("--db", metavar="PATH", help="SQLite DB path (default: auto-detected)")
    parser.add_argument("--dataset-version", metavar="VER",
                        default=datetime.today().strftime("%Y-%m-%d"))
    parser.add_argument("--binary", metavar="PATH", help="path to kaikki-import binary")
    parser.add_argument("--translate-binary", metavar="PATH", help="path to kaikki-translate binary")
    parser.add_argument("--llm-url", metavar="URL", default="",
                        help="LLM gateway URL for translation (default: from tifl.yaml)")
    parser.add_argument("--llm-model", metavar="MODEL", default="",
                        help="model to use for translation")
    parser.add_argument("--translate-batch", type=int, default=20,
                        help="definitions per LLM call (default: 20)")
    parser.add_argument("--translate-limit", type=int, default=0,
                        help="cap on definitions to translate this run (0=all)")

    # Step flags — if any are given, run only those steps non-interactively
    parser.add_argument("--step-english", action="store_true",
                        help="run English Wiktionary import")
    parser.add_argument("--step-native", action="store_true",
                        help="run native Wiktionary import")
    parser.add_argument("--step-translate", action="store_true",
                        help="run LLM translation of native-only entries")
    parser.add_argument("--input-english", metavar="FILE",
                        help="pre-downloaded English dump file")
    parser.add_argument("--input-native", metavar="FILE",
                        help="pre-downloaded native dump file")
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    explicit_steps = args.step_english or args.step_native or args.step_translate

    # ── Banner ────────────────────────────────────────────────────────────────
    print()
    print(bold(c(CYAN, "  kaikki → tifl importer")))
    print(dim("  Wiktextract dictionary dump downloader + importer"))
    print()

    # ── Resolve import binary ─────────────────────────────────────────────────
    if args.binary:
        import_bin = Path(args.binary)
        if not import_bin.exists():
            err(f"Import binary not found: {import_bin}")
            sys.exit(1)
    else:
        import_bin = find_binary("kaikki-import", ["tifl-kaikki-import"])
        if import_bin is None:
            err("Could not find kaikki-import binary. Build with: go build ./cmd/kaikki-import/")
            sys.exit(1)
    ok(f"Import binary:    {import_bin}")

    # Translate binary (optional — only needed for step 3)
    if args.translate_binary:
        translate_bin: Path | None = Path(args.translate_binary)
    else:
        translate_bin = find_binary("kaikki-translate", ["tifl-kaikki-translate"])
    if translate_bin:
        ok(f"Translate binary: {translate_bin}")
    else:
        warn("kaikki-translate binary not found — translation step will be unavailable")
        warn("Build with: go build ./cmd/kaikki-translate/")

    # ── Language ──────────────────────────────────────────────────────────────
    if args.language:
        lang_code = args.language
    else:
        choices = [(code, name) for code, (name, _, _) in LANGUAGES.items()]
        lang_code = prompt_choice("Which language?", choices, default=0)
    lang_name, _, native_stem = LANGUAGES[lang_code]
    ok(f"Language: {lang_name} ({lang_code})")

    # ── DB path ───────────────────────────────────────────────────────────────
    db_path = Path(args.db) if args.db else default_db()
    if not args.db:
        db_path = prompt_path("SQLite database path", default=db_path)
    ok(f"Database: {db_path}")

    cache_dir = Path(__file__).parent.parent / "data" / "kaikki"
    _, eng_stem, _ = LANGUAGES[lang_code]
    dataset_ver = args.dataset_version

    try:
        # ── Step 1: English Wiktionary ────────────────────────────────────────
        do_english = args.step_english if explicit_steps else prompt_yes(
            "Step 1: Import English Wiktionary definitions?", default=True)

        if do_english:
            section("Step 1 — English Wiktionary")
            if args.input_english:
                eng_dump = Path(args.input_english)
                info(f"Using provided file: {eng_dump}")
                ready = eng_dump.exists()
            else:
                eng_dump = cache_dir / (eng_stem + ".jsonl.gz")
                ready = ensure_dump(kaikki_url_english(lang_code), eng_dump, args.dry_run)

            if ready or args.dry_run:
                run_import(
                    label="Importing (English Wikt)…",
                    dump_file=eng_dump,
                    lang_code=lang_code,
                    db_path=db_path,
                    binary=import_bin,
                    dataset_version=dataset_ver,
                    native=False,
                    dry_run=args.dry_run,
                )

        # ── Step 2: Native Wiktionary ─────────────────────────────────────────
        if native_stem is None:
            if not explicit_steps:
                warn(f"No native Wiktionary URL known for {lang_name} — skipping step 2")
            do_native = False
        else:
            do_native = args.step_native if explicit_steps else prompt_yes(
                f"Step 2: Import {lang_name} Wiktionary definitions (native glosses)?",
                default=True,
            )

        if do_native:
            section(f"Step 2 — {lang_name} Wiktionary (native)")
            native_url = kaikki_url_native(lang_code)
            assert native_url  # guarded above

            if args.input_native:
                nat_dump = Path(args.input_native)
                info(f"Using provided file: {nat_dump}")
                ready = nat_dump.exists()
            else:
                _, _, native_kaikki_stem = LANGUAGES[lang_code]
                nat_dump = cache_dir / (native_kaikki_stem + ".jsonl.gz")  # type: ignore[operator]
                info(f"Note: URL {native_url}")
                info("If this 404s, check kaikki.org for the correct native dump URL and pass --input-native.")
                ready = ensure_dump(native_url, nat_dump, args.dry_run)

            if ready or args.dry_run:
                run_import(
                    label="Importing (native Wikt)…",
                    dump_file=nat_dump,
                    lang_code=lang_code,
                    db_path=db_path,
                    binary=import_bin,
                    dataset_version=dataset_ver,
                    native=True,
                    dry_run=args.dry_run,
                )

        # ── Step 3: LLM translation ───────────────────────────────────────────
        if translate_bin is None:
            if not explicit_steps:
                warn("Skipping step 3 — kaikki-translate binary not found")
            do_translate = False
        else:
            do_translate = args.step_translate if explicit_steps else prompt_yes(
                "Step 3: LLM-translate native-only entries to English?",
                default=False,
            )

        if do_translate:
            section("Step 3 — LLM translation")
            assert translate_bin
            run_translate(
                lang_code=lang_code,
                db_path=db_path,
                translate_binary=translate_bin,
                batch=args.translate_batch,
                limit=args.translate_limit,
                llm_url=args.llm_url,
                llm_model=args.llm_model,
                dry_run=args.dry_run,
            )

        print()
        ok("All done.")

    except KeyboardInterrupt:
        print()
        warn("Cancelled.")
        sys.exit(130)
    except Exception as e:
        err(str(e))
        sys.exit(1)


if __name__ == "__main__":
    main()
