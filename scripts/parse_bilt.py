#!/usr/bin/env python3
"""Extract transaction tables from Bilt statement PDFs into three Excel files."""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from datetime import datetime
from pathlib import Path
from typing import Any

import pymupdf
from openpyxl import Workbook
from openpyxl.styles import Font, PatternFill


SECTIONS = (
    "Payment and credits",
    "Your transactions",
    "Shabarish's transactions",
)
DATE_PATTERN = re.compile(
    r"\b(Jan(?:uary)?|Feb(?:ruary)?|Mar(?:ch)?|Apr(?:il)?|May|Jun(?:e)?|"
    r"Jul(?:y)?|Aug(?:ust)?|Sep(?:tember)?|Oct(?:ober)?|Nov(?:ember)?|"
    r"Dec(?:ember)?)\s+(\d{1,2}),?\s+(\d{4})\b",
    re.IGNORECASE,
)
SECTION_PATTERNS = (
    re.compile(r"payments?\s+and\s+credits?", re.IGNORECASE),
    re.compile(r"^(?!.*shabarish['’]s)(?:your\s+)?transactions\b", re.IGNORECASE),
    re.compile(r"shabarish['’]s.{0,24}\btransactions\b", re.IGNORECASE),
)


def parse_date(text: str) -> datetime | None:
    match = DATE_PATTERN.search(text)
    if not match:
        return None
    try:
        return datetime.strptime(
            f"{match.group(1)[:3]} {match.group(2)} {match.group(3)}",
            "%b %d %Y",
        )
    except ValueError:
        return None


def parse_amount(text: str) -> float | None:
    if not re.search(r"\d", text):
        return None
    negative = "-" in text or "\u2212" in text
    normalized = text.lower().replace(" ", "").replace(",", "")
    normalized = re.sub(r"^[-\u2212]?[$s]", "", normalized)
    normalized = normalized.translate(
        str.maketrans({"o": "0", "i": "1", "l": "1", "b": "8", "g": "9", "s": "5"})
    )
    digits = re.sub(r"\D", "", normalized)
    if len(digits) < 3:
        return None
    amount = int(digits[:-2]) + int(digits[-2:]) / 100
    return -amount if negative else amount


WINDOWS_OCR_SCRIPT = r"""
Add-Type -AssemblyName System.Runtime.WindowsRuntime
$StorageFile = [Windows.Storage.StorageFile,Windows.Storage,ContentType=WindowsRuntime]
$FileAccessMode = [Windows.Storage.FileAccessMode,Windows.Storage,ContentType=WindowsRuntime]
$StorageStream = [Windows.Storage.Streams.IRandomAccessStream,Windows.Storage.Streams,ContentType=WindowsRuntime]
$Decoder = [Windows.Graphics.Imaging.BitmapDecoder,Windows.Graphics.Imaging,ContentType=WindowsRuntime]
$SoftwareBitmap = [Windows.Graphics.Imaging.SoftwareBitmap,Windows.Graphics.Imaging,ContentType=WindowsRuntime]
$Engine = [Windows.Media.Ocr.OcrEngine,Windows.Media.Ocr,ContentType=WindowsRuntime]
$OcrResult = [Windows.Media.Ocr.OcrResult,Windows.Foundation,ContentType=WindowsRuntime]
function Await($operation, $resultType) {
    $method = [System.WindowsRuntimeSystemExtensions].GetMethods() |
        Where-Object {
            $_.Name -eq 'AsTask' -and $_.IsGenericMethod -and
            $_.GetParameters().Count -eq 1 -and
            $_.GetParameters()[0].ParameterType.Name -eq 'IAsyncOperation`1'
        } | Select-Object -First 1
    $task = $method.MakeGenericMethod($resultType).Invoke($null, @($operation))
    $task.Wait()
    return $task.Result
}
$file = Await ($StorageFile::GetFileFromPathAsync($env:BILT_OCR_IMAGE)) $StorageFile
$stream = Await ($file.OpenAsync($FileAccessMode::Read)) $StorageStream
$decoder = Await ($Decoder::CreateAsync($stream)) $Decoder
$bitmap = Await ($decoder.GetSoftwareBitmapAsync()) $SoftwareBitmap
$engine = $Engine::TryCreateFromUserProfileLanguages()
if (-not $engine) { throw 'No Windows OCR language pack is installed.' }
$recognized = Await ($engine.RecognizeAsync($bitmap)) $OcrResult
$words = @(
    foreach ($line in $recognized.Lines) {
        foreach ($word in $line.Words) {
            [PSCustomObject]@{
                text = $word.Text
                x = $word.BoundingRect.X
                y = $word.BoundingRect.Y
                width = $word.BoundingRect.Width
                height = $word.BoundingRect.Height
            }
        }
    }
)
ConvertTo-Json -InputObject $words -Compress
"""


def windows_ocr(image_path: Path) -> list[dict[str, Any]]:
    powershell = shutil.which("powershell.exe") or shutil.which("powershell")
    if not powershell:
        raise RuntimeError(
            "This PDF needs OCR. Run the script on Windows with the English OCR "
            "language pack installed."
        )
    env = os.environ.copy()
    env["BILT_OCR_IMAGE"] = str(image_path.resolve())
    completed = subprocess.run(
        [powershell, "-NoProfile", "-NonInteractive", "-Command", WINDOWS_OCR_SCRIPT],
        capture_output=True,
        text=True,
        env=env,
        check=False,
    )
    if completed.returncode:
        raise RuntimeError(completed.stderr.strip() or "Windows OCR failed.")
    output = completed.stdout.strip()
    return json.loads(output) if output else []


def searchable_words(page: pymupdf.Page) -> list[dict[str, Any]]:
    return [
        {
            "text": word[4],
            "x": word[0],
            "y": word[1],
            "width": word[2] - word[0],
            "height": word[3] - word[1],
        }
        for word in page.get_text("words")
        if word[4].strip()
    ]


def ocr_orientation_score(words: list[dict[str, Any]]) -> int:
    text = " ".join(str(word.get("text", "")) for word in words)
    date_count = len(DATE_PATTERN.findall(text))
    section_count = sum(bool(pattern.search(text)) for pattern in SECTION_PATTERNS)
    return len(words) + date_count * 20 + section_count * 120


def recognized_lines(
    words: list[dict[str, Any]], page_width: float
) -> list[tuple[float, str, str, str, str]]:
    """Group positioned OCR words into table baselines and their three columns."""
    items = [
        (
            float(word["y"]) + float(word["height"]) / 2,
            float(word["x"]),
            str(word["text"]).strip(),
        )
        for word in words
        if str(word.get("text", "")).strip()
    ]
    items.sort(key=lambda item: (item[0], item[1]))
    grouped: list[list[tuple[float, float, str]]] = []
    for item in items:
        center = sum(entry[0] for entry in grouped[-1]) / len(grouped[-1]) if grouped else 0
        if not grouped or abs(item[0] - center) > 14:
            grouped.append([item])
        else:
            grouped[-1].append(item)

    output = []
    for group in grouped:
        group.sort(key=lambda item: item[1])
        text = " ".join(item[2] for item in group)
        date_text = " ".join(item[2] for item in group if item[1] < page_width * 0.2)
        description = " ".join(
            item[2] for item in group if page_width * 0.2 <= item[1] < page_width * 0.8
        )
        amount_text = " ".join(item[2] for item in group if item[1] >= page_width * 0.8)
        output.append(
            (sum(item[0] for item in group) / len(group), text, date_text, description, amount_text)
        )
    return output


def extract_transactions(pdf_path: Path) -> dict[str, list[dict[str, Any]]]:
    transactions: dict[str, list[dict[str, Any]]] = {name: [] for name in SECTIONS}
    current_section: str | None = None
    ocr_word_counts: list[int] = []

    with pymupdf.open(pdf_path) as document:
        with tempfile.TemporaryDirectory(prefix="bilt_ocr_") as temporary_dir:
            for page_number, page in enumerate(document, start=1):
                words = searchable_words(page)
                if words:
                    page_width = page.rect.width
                    page_height = page.rect.height
                else:
                    # The sample PDF is landscape-sized but its page content is
                    # rotated; ordinary portrait PDFs should be OCRed as-is.
                    rotation = 90 if page.rect.width > page.rect.height else 0
                    pixmap = page.get_pixmap(
                        matrix=pymupdf.Matrix(2, 2).prerotate(rotation),
                        alpha=False,
                    )
                    image_path = Path(temporary_dir) / f"page_{page_number}.png"
                    pixmap.save(image_path)
                    words = windows_ocr(image_path)

                    # Some statements have portrait page dimensions with
                    # sideways content, or the reverse. Retry the other
                    # orientation if the likely orientation yielded little OCR.
                    if ocr_orientation_score(words) < 180:
                        alternate = page.get_pixmap(
                            matrix=pymupdf.Matrix(2, 2).prerotate(90 - rotation),
                            alpha=False,
                        )
                        alternate_path = Path(temporary_dir) / f"page_{page_number}_alternate.png"
                        alternate.save(alternate_path)
                        alternate_words = windows_ocr(alternate_path)
                        if ocr_orientation_score(alternate_words) > ocr_orientation_score(words):
                            words = alternate_words
                            pixmap = alternate

                    page_width = pixmap.width
                    page_height = pixmap.height

                ocr_word_counts.append(len(words))
                page_lines = recognized_lines(words, page_width)
                pending: dict[str, Any] | None = None

                for y, text, date_text, description_text, amount_text in page_lines:
                    normalized = re.sub(r"\s+", " ", text).strip()
                    if y > page_height * 0.93:
                        continue

                    detected_section = next(
                        (
                            section
                            for section, pattern in zip(SECTIONS, SECTION_PATTERNS)
                            if pattern.search(normalized)
                        ),
                        None,
                    )
                    if detected_section:
                        if pending and current_section:
                            transactions[current_section].append(pending)
                            pending = None
                        current_section = detected_section
                        continue

                    if re.search(r"(?:statement|billing)\s+(?:period|cycle)", normalized, re.IGNORECASE):
                        continue

                    transaction_date = parse_date(date_text)
                    if transaction_date:
                        if pending and current_section:
                            transactions[current_section].append(pending)
                        amount = parse_amount(amount_text)
                        if amount is not None and current_section == SECTIONS[0] and amount > 0:
                            amount = -amount
                        pending = {
                            "Date": transaction_date,
                            "Description": description_text.strip(),
                            "Amount": amount,
                        }
                        continue

                    if not current_section:
                        continue

                    if re.search(r"total\s+(?:payments|new charges)", normalized, re.IGNORECASE):
                        if pending:
                            transactions[current_section].append(pending)
                            pending = None
                        continue

                    if re.search(
                        r"^(?:date\s+description|amount|authorized user|page\s+\d+\s+of\s+\d+|"
                        r"bilt card|bilt palladium card|cardless inc)",
                        normalized,
                        re.IGNORECASE,
                    ):
                        continue

                    if pending and description_text:
                        pending["Description"] = " ".join(
                            part
                            for part in (pending["Description"], description_text.strip())
                            if part
                        )

                if pending and current_section:
                    transactions[current_section].append(pending)

    if not any(transactions.values()):
        raise RuntimeError(
            "No transaction rows were recognized in "
            f"{pdf_path.name} ({len(ocr_word_counts)} pages; "
            f"OCR words per page: {ocr_word_counts}). "
            "Confirm this is a Bilt statement and that the Windows English OCR "
            "language pack is installed."
        )
    return transactions


def add_transactions_sheet(
    workbook: Workbook,
    title: str,
    rows: list[dict[str, Any]],
    amount_sign: int = 1,
) -> None:
    worksheet = workbook.create_sheet(title)
    worksheet.append(["Date", "Description", "Amount"])

    for cell in worksheet[1]:
        cell.font = Font(bold=True, color="FFFFFF")
        cell.fill = PatternFill("solid", fgColor="243746")

    for row in rows:
        amount = row["Amount"]
        if amount is not None:
            amount = abs(amount) * amount_sign
        worksheet.append([row["Date"], row["Description"], amount])
        worksheet.cell(worksheet.max_row, 1).number_format = "mmm d, yyyy"
        worksheet.cell(worksheet.max_row, 3).number_format = '$#,##0.00;-$#,##0.00'

    worksheet.freeze_panes = "A2"
    worksheet.auto_filter.ref = worksheet.dimensions
    worksheet.column_dimensions["A"].width = 16
    worksheet.column_dimensions["B"].width = 70
    worksheet.column_dimensions["C"].width = 16


def write_payments_and_transactions(path: Path, data: dict[str, list[dict[str, Any]]]) -> None:
    workbook = Workbook()
    workbook.remove(workbook.active)
    add_transactions_sheet(workbook, "Payment and transactions", data[SECTIONS[0]], 1)
    add_transactions_sheet(workbook, "Your transactions", data[SECTIONS[1]], -1)
    path.parent.mkdir(parents=True, exist_ok=True)
    workbook.save(path)


def write_workbook(path: Path, rows: list[dict[str, Any]]) -> None:
    workbook = Workbook()
    workbook.remove(workbook.active)
    add_transactions_sheet(workbook, "Transactions", rows)
    path.parent.mkdir(parents=True, exist_ok=True)
    workbook.save(path)


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Extract Bilt payment and transaction tables into one Excel workbook."
    )
    parser.add_argument("pdf", type=Path, help="Path to the Bilt statement PDF")
    parser.add_argument(
        "-o",
        "--output-dir",
        type=Path,
        help="Folder for the output workbook (default: next to the PDF)",
    )
    args = parser.parse_args()

    if not args.pdf.is_file():
        parser.error(f"PDF not found: {args.pdf}")
    output_dir = args.output_dir or args.pdf.parent

    try:
        extracted = extract_transactions(args.pdf)
        combined_path = output_dir / f"{args.pdf.stem}.xlsx"
        write_payments_and_transactions(combined_path, extracted)
        print(
            f"Payment and transactions: {len(extracted[SECTIONS[0]])} rows; "
            f"Your transactions: {len(extracted[SECTIONS[1]])} rows -> {combined_path}"
        )
    except (OSError, RuntimeError, ValueError) as exc:
        print(f"Error: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())