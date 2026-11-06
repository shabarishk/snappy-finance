#!/usr/bin/env python3
"""Application adapter for the bundled Bilt statement parser."""

from __future__ import annotations

import argparse
import csv
import json
import re
import shutil
import subprocess
import sys
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any

import pymupdf
from openpyxl import Workbook
from openpyxl.styles import Font, PatternFill

import parse_bilt


OCR_WORDS: list[dict[str, Any]] = []
DATE_TEXT = (
    r"(?:Jan(?:uary)?|Feb(?:ruary)?|Mar(?:ch)?|Apr(?:il)?|May|Jun(?:e)?|"
    r"Jul(?:y)?|Aug(?:ust)?|Sep(?:tember)?|Oct(?:ober)?|Nov(?:ember)?|"
    r"Dec(?:ember)?)\s+\d{1,2},?\s+\d{4}|\d{1,2}[/-]\d{1,2}[/-]\d{2,4}"
)
PERIOD_PATTERN = re.compile(
    rf"(?:statement\s+period|billing\s+period|billing\s+cycle|statement\s+cycle)"
    rf"\s*:?\s*(?:from\s+)?({DATE_TEXT})\s*(?:to|through|[-–—])\s*({DATE_TEXT})",
    re.IGNORECASE,
)
MONTH_NAME = (
    r"Jan(?:uary)?|Feb(?:ruary)?|Mar(?:ch)?|Apr(?:il)?|May|Jun(?:e)?|"
    r"Jul(?:y)?|Aug(?:ust)?|Sep(?:tember)?|Oct(?:ober)?|Nov(?:ember)?|"
    r"Dec(?:ember)?"
)
SHORT_PERIOD_PATTERN = re.compile(
    rf"({MONTH_NAME})\s+(\d{{1,2}})\s*[-–—]\s*"
    rf"({MONTH_NAME})\s+(\d{{1,2}}),?\s+(\d{{4}})",
    re.IGNORECASE,
)
STATEMENT_BALANCE_PATTERN = re.compile(
    r"\b(?:new\s+balance|statement\s+balance|total\s+balance)\b"
    r"\s*:?\s*\$?\s*((?:\d{1,3}(?:,\d{3})+|\d+)\.\d{2})",
    re.IGNORECASE,
)
MINIMUM_PAYMENT_DUE_PATTERN = re.compile(
    r"\bminimum\s+(?:payment(?:\s+due)?|amount\s+due)\b"
    r"\s*:?\s*\$?\s*((?:\d{1,3}(?:,\d{3})+|\d+)\.\d{2})",
    re.IGNORECASE,
)
PAYMENT_DUE_DATE_PATTERN = re.compile(
    rf"\b(?:payment\s+due\s+date|payment\s+due|due\s+date)\b"
    rf"\s*:?\s*(?:is\s+)?({DATE_TEXT})",
    re.IGNORECASE,
)


def tesseract_ocr(image_path: Path) -> list[dict[str, Any]]:
    tesseract = shutil.which("tesseract")
    if not tesseract:
        raise RuntimeError("Tesseract OCR is not installed in the application container.")
    try:
        result = subprocess.run(
            [tesseract, str(image_path), "stdout", "tsv"],
            capture_output=True,
            text=True,
            check=False,
            timeout=60,
        )
    except subprocess.TimeoutExpired as exc:
        raise RuntimeError("Statement OCR exceeded the time limit.") from exc
    if result.returncode:
        raise RuntimeError(result.stderr.strip() or "Tesseract OCR failed.")

    words = []
    for row in csv.DictReader(result.stdout.splitlines(), delimiter="\t"):
        text = row.get("text", "").strip()
        if not text:
            continue
        words.append(
            {
                "text": text,
                "x": float(row["left"]),
                "y": float(row["top"]),
                "width": float(row["width"]),
                "height": float(row["height"]),
            }
        )
    OCR_WORDS.append({"text": " ".join(word["text"] for word in words)})
    return words


def parse_period_date(value: str) -> datetime | None:
    parsed = parse_bilt.parse_date(value)
    if parsed:
        return parsed
    for pattern in ("%m/%d/%Y", "%m/%d/%y", "%m-%d-%Y", "%m-%d-%y"):
        try:
            return datetime.strptime(value.strip(), pattern)
        except ValueError:
            continue
    return None


def extract_statement_period(text: str) -> tuple[str | None, str | None]:
    normalized_text = re.sub(r"\s+", " ", text)
    match = PERIOD_PATTERN.search(normalized_text)
    if match:
        start = parse_period_date(match.group(1))
        end = parse_period_date(match.group(2))
        if start and end and start <= end:
            return start.date().isoformat(), end.date().isoformat()

    match = SHORT_PERIOD_PATTERN.search(normalized_text)
    if not match:
        return None, None
    start_month = datetime.strptime(match.group(1)[:3].title(), "%b").month
    start_day = int(match.group(2))
    end_month = datetime.strptime(match.group(3)[:3].title(), "%b").month
    end_day = int(match.group(4))
    end_year = int(match.group(5))
    start_year = end_year - (1 if start_month > end_month else 0)
    try:
        start = datetime(start_year, start_month, start_day)
        end = datetime(end_year, end_month, end_day)
    except ValueError:
        return None, None
    return start.date().isoformat(), end.date().isoformat()


def extract_statement_balance(text: str) -> tuple[float | None, float | None, str | None]:
    normalized_text = re.sub(r"\s+", " ", text)
    balance_match = STATEMENT_BALANCE_PATTERN.search(normalized_text)
    minimum_payment_match = MINIMUM_PAYMENT_DUE_PATTERN.search(normalized_text)
    due_date_match = PAYMENT_DUE_DATE_PATTERN.search(normalized_text)
    balance = None
    minimum_payment_due = None
    due_date = None
    if balance_match:
        balance = float(balance_match.group(1).replace(",", ""))
    if minimum_payment_match:
        minimum_payment_due = float(minimum_payment_match.group(1).replace(",", ""))
    if due_date_match:
        parsed_due_date = parse_period_date(due_date_match.group(1))
        if parsed_due_date:
            due_date = parsed_due_date.date().isoformat()
    return balance, minimum_payment_due, due_date


def extract_rows(
    pdf_path: Path,
    override_start: str | None = None,
    override_end: str | None = None,
) -> tuple[str | None, str | None, float | None, float | None, str | None, list[dict[str, Any]]]:
    OCR_WORDS.clear()
    parsed_sections = parse_bilt.extract_transactions(pdf_path)
    with pymupdf.open(pdf_path) as document:
        statement_text = " ".join(page.get_text() for page in document)
    statement_text += " " + " ".join(str(entry["text"]) for entry in OCR_WORDS)
    period_start, period_end = extract_statement_period(statement_text)
    statement_balance, minimum_payment_due, payment_due_date = extract_statement_balance(
        statement_text
    )
    if override_start or override_end:
        if not override_start or not override_end:
            raise RuntimeError("Both statement period dates must be provided.")
        start = datetime.strptime(override_start, "%Y-%m-%d").date()
        end = datetime.strptime(override_end, "%Y-%m-%d").date()
        if start > end:
            raise RuntimeError("The statement period start must be on or before its end.")
        period_start, period_end = start.isoformat(), end.isoformat()

    first_transaction_date = None
    last_transaction_date = None
    if period_start and period_end:
        first_transaction_date = datetime.strptime(period_start, "%Y-%m-%d").date() - timedelta(days=2)
        last_transaction_date = datetime.strptime(period_end, "%Y-%m-%d").date() + timedelta(days=2)

    rows = []
    incomplete_rows = 0
    for section in parse_bilt.SECTIONS:
        for parsed in parsed_sections[section]:
            transaction_date = parsed["Date"]
            if not transaction_date:
                incomplete_rows += 1
                continue
            date = transaction_date.date()
            if first_transaction_date and last_transaction_date:
                if date < first_transaction_date or date > last_transaction_date:
                    continue
            amount = parsed["Amount"]
            merchant = re.sub(r"\s+", " ", str(parsed["Description"])).strip()
            if amount is None or not merchant:
                incomplete_rows += 1
                continue
            signed_amount = abs(float(amount)) if section == parse_bilt.SECTIONS[0] else -abs(float(amount))
            rows.append(
                {
                    "date": date.isoformat(),
                    "merchant": merchant,
                    "amount": round(signed_amount, 2),
                }
            )
    if incomplete_rows:
        raise RuntimeError(
            f"The parser could not read all transaction rows ({incomplete_rows} incomplete). "
            "No statement data was imported."
        )
    if not rows:
        raise RuntimeError("No complete transactions were found in this Bilt statement.")
    rows.sort(key=lambda row: (row["date"], row["merchant"], row["amount"]))
    return (
        period_start,
        period_end,
        statement_balance,
        minimum_payment_due,
        payment_due_date,
        rows,
    )


def write_spreadsheet(path: Path, rows: list[dict[str, Any]]) -> None:
    workbook = Workbook()
    sheet = workbook.active
    sheet.title = "Transactions"
    sheet.append(["Date", "Merchant", "Amount"])
    for cell in sheet[1]:
        cell.font = Font(bold=True, color="FFFFFF")
        cell.fill = PatternFill("solid", fgColor="243746")
    for row in rows:
        sheet.append([datetime.strptime(row["date"], "%Y-%m-%d").date(), row["merchant"], row["amount"]])
        sheet.cell(sheet.max_row, 1).number_format = "yyyy-mm-dd"
        sheet.cell(sheet.max_row, 2).data_type = "s"
        sheet.cell(sheet.max_row, 3).number_format = '$#,##0.00;-$#,##0.00'
    sheet.freeze_panes = "A2"
    sheet.auto_filter.ref = sheet.dimensions
    sheet.column_dimensions["A"].width = 16
    sheet.column_dimensions["B"].width = 70
    sheet.column_dimensions["C"].width = 16
    path.parent.mkdir(parents=True, exist_ok=True)
    workbook.save(path)


def main() -> int:
    argument_parser = argparse.ArgumentParser()
    argument_parser.add_argument("--input", type=Path, required=True)
    argument_parser.add_argument("--json-output", type=Path, required=True)
    argument_parser.add_argument("--spreadsheet-output", type=Path, required=True)
    argument_parser.add_argument("--period-start")
    argument_parser.add_argument("--period-end")
    args = argument_parser.parse_args()

    parse_bilt.windows_ocr = tesseract_ocr
    try:
        (
            period_start,
            period_end,
            statement_balance,
            minimum_payment_due,
            payment_due_date,
            rows,
        ) = extract_rows(args.input, args.period_start, args.period_end)
        write_spreadsheet(args.spreadsheet_output, rows)
        args.json_output.parent.mkdir(parents=True, exist_ok=True)
        args.json_output.write_text(
            json.dumps(
                {
                    "period_start": period_start,
                    "period_end": period_end,
                    "statement_balance": statement_balance,
                    "minimum_payment_due": minimum_payment_due,
                    "payment_due_date": payment_due_date,
                    "transactions": rows,
                }
            ),
            encoding="utf-8",
        )
    except (OSError, RuntimeError, ValueError, csv.Error) as exc:
        print(str(exc), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
