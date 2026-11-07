#!/usr/bin/env python3
"""Extract transactions and summary data from an American Express Gold statement."""

from __future__ import annotations

import argparse
import json
import re
import sys
from datetime import datetime
from pathlib import Path
from typing import Any

import pymupdf
from openpyxl import Workbook
from openpyxl.styles import Font, PatternFill


DATE_PATTERN = re.compile(r"^\d{1,2}/\d{1,2}/\d{2,4}\*?$")
AMOUNT_PATTERN = re.compile(r"^[+-]?\$?-?(?:\d[\d,]*)?\.\d{2}$")
STATEMENT_DATE_PATTERN = re.compile(
    r"\b(?:closing|statement)\s+date\b\s*:?\s*(\d{1,2}/\d{1,2}/\d{2,4})",
    re.IGNORECASE,
)
PAYMENT_DUE_DATE_PATTERN = re.compile(
    r"\bpayment\s+due\s+date\b\s*:?\s*(\d{1,2}/\d{1,2}/\d{2,4})",
    re.IGNORECASE,
)
STATEMENT_BALANCE_PATTERN = re.compile(
    r"\b(?:new\s+balance|statement\s+balance)\b"
    r"\s*:?\s*\$?\s*((?:\d{1,3}(?:,\d{3})+|\d+)\.\d{2})",
    re.IGNORECASE,
)
MINIMUM_PAYMENT_DUE_PATTERN = re.compile(
    r"\bminimum\s+(?:payment|balance|amount)\s+due\b"
    r"\s*:?\s*\$?\s*((?:\d{1,3}(?:,\d{3})+|\d+)\.\d{2})",
    re.IGNORECASE,
)
DETAIL_SECTION_PATTERN = re.compile(
    r"^(payments|credits|new charges)\s+details$", re.IGNORECASE
)
DETAIL_SECTION_END_PATTERN = re.compile(
    r"^(?:fees|interest charged|about trailing interest|"
    r"interest charge calculation|payments and credits summary|"
    r"new charges summary)$",
    re.IGNORECASE,
)
NON_MERCHANT_LINE_PATTERN = re.compile(
    r"^(?:[\d\s()+./-]+|"
    r"PHARMACIES|COMPUTER STORE|RECORD STORE|GROCERY STORES?)$",
    re.IGNORECASE,
)


def parse_slash_date(value: str) -> datetime:
    date_format = "%m/%d/%y" if len(value.rsplit("/", 1)[-1]) == 2 else "%m/%d/%Y"
    return datetime.strptime(value, date_format)


def parse_amount(value: str) -> float | None:
    cleaned = value.strip().replace("−", "-")
    if not AMOUNT_PATTERN.fullmatch(cleaned):
        return None
    return float(cleaned.replace("$", "").replace(",", ""))


def extract_statement_metadata(
    text: str,
) -> tuple[str | None, str | None, float | None, float | None]:
    normalized_text = re.sub(r"\s+", " ", text)
    statement_date_match = STATEMENT_DATE_PATTERN.search(normalized_text)
    due_date_match = PAYMENT_DUE_DATE_PATTERN.search(normalized_text)
    balance_match = STATEMENT_BALANCE_PATTERN.search(normalized_text)
    minimum_due_match = MINIMUM_PAYMENT_DUE_PATTERN.search(normalized_text)

    statement_date = (
        parse_slash_date(statement_date_match.group(1)).date().isoformat()
        if statement_date_match
        else None
    )
    payment_due_date = (
        parse_slash_date(due_date_match.group(1)).date().isoformat()
        if due_date_match
        else None
    )
    statement_balance = (
        float(balance_match.group(1).replace(",", ""))
        if balance_match
        else None
    )
    minimum_payment_due = (
        float(minimum_due_match.group(1).replace(",", ""))
        if minimum_due_match
        else None
    )
    return statement_date, payment_due_date, statement_balance, minimum_payment_due


def group_page_lines(
    page: pymupdf.Page,
) -> list[tuple[float, list[tuple[float, str]]]]:
    words = sorted(page.get_text("words"), key=lambda word: (word[1], word[0]))
    grouped: list[list[tuple[float, float, str]]] = []
    for x0, y0, _x1, _y1, text, *_ in words:
        if not text.strip():
            continue
        if not grouped or abs(y0 - sum(word[0] for word in grouped[-1]) / len(grouped[-1])) > 2.5:
            grouped.append([])
        grouped[-1].append((y0, x0, text.strip()))

    return [
        (
            sum(word[0] for word in line) / len(line),
            [(word[1], word[2]) for word in sorted(line, key=lambda word: word[1])],
        )
        for line in grouped
    ]


def extract_transactions(document: pymupdf.Document) -> list[dict[str, Any]]:
    transactions: list[dict[str, Any]] = []
    section: str | None = None
    pending: dict[str, Any] | None = None
    pending_page = 0

    def save_pending() -> None:
        nonlocal pending
        if pending is None:
            return
        if not pending["merchant"] or pending["amount"] is None:
            raise RuntimeError(
                f"Could not read the merchant or amount for a transaction on page {pending_page}."
            )
        transactions.append(pending)
        pending = None

    for page_number, page in enumerate(document, start=1):
        for _, positioned_words in group_page_lines(page):
            normalized = re.sub(r"\s+", " ", " ".join(word for _, word in positioned_words)).strip()
            if normalized.casefold() == "date description type amount":
                continue
            section_match = DETAIL_SECTION_PATTERN.fullmatch(normalized)
            if section_match:
                save_pending()
                section = section_match.group(1).lower()
                continue
            if section and DETAIL_SECTION_END_PATTERN.fullmatch(normalized):
                save_pending()
                section = None
                continue
            if not section:
                continue

            date_token = next(
                (
                    word
                    for x, word in positioned_words
                    if x < page.rect.width * 0.2 and DATE_PATTERN.fullmatch(word)
                ),
                None,
            )
            if date_token:
                save_pending()
                try:
                    transaction_date = parse_slash_date(date_token.rstrip("*"))
                except ValueError as exc:
                    raise RuntimeError(
                        f"Invalid transaction date {date_token!r} on page {page_number}."
                    ) from exc
                description = " ".join(
                    word
                    for x, word in positioned_words
                    if page.rect.width * 0.15 <= x < page.rect.width * 0.7
                ).strip()
                amount = next(
                    (
                        parsed
                        for x, word in positioned_words
                        if x >= page.rect.width * 0.82
                        for parsed in [parse_amount(word)]
                        if parsed is not None
                    ),
                    None,
                )
                if amount is not None:
                    amount = abs(amount) if section in {"payments", "credits"} else -abs(amount)
                pending = {
                    "date": transaction_date.date().isoformat(),
                    "merchant": description,
                    "amount": amount,
                }
                pending_page = page_number
                continue

            if pending:
                continuation = " ".join(
                    word
                    for x, word in positioned_words
                    if page.rect.width * 0.15 <= x < page.rect.width * 0.7
                ).strip()
                if continuation and not NON_MERCHANT_LINE_PATTERN.fullmatch(continuation) and "@" not in continuation:
                    pending["merchant"] = f"{pending['merchant']} {continuation}".strip()

    save_pending()
    transactions.sort(key=lambda row: (row["date"], row["merchant"], row["amount"]))
    return transactions


def extract_statement(
    pdf_path: Path,
    override_start: str | None = None,
    override_end: str | None = None,
) -> tuple[
    str | None,
    str | None,
    str | None,
    str | None,
    float | None,
    float | None,
    list[dict[str, Any]],
]:
    with pymupdf.open(pdf_path) as document:
        statement_text = " ".join(page.get_text() for page in document)
        statement_date, payment_due_date, statement_balance, minimum_payment_due = (
            extract_statement_metadata(statement_text)
        )
        transactions = extract_transactions(document)

    if statement_date is None:
        raise RuntimeError("Could not find the statement closing date.")
    if not transactions:
        raise RuntimeError("No transactions were found in the statement details.")

    period_start = None
    period_end = statement_date
    if override_start or override_end:
        if not override_start or not override_end:
            raise RuntimeError("Both statement period dates must be provided.")
        try:
            start_date = datetime.strptime(override_start, "%Y-%m-%d")
            end_date = datetime.strptime(override_end, "%Y-%m-%d")
        except ValueError as exc:
            raise RuntimeError("Statement period dates must be YYYY-MM-DD.") from exc
        if start_date > end_date:
            raise RuntimeError("Statement period start must be on or before its end.")
        period_start = start_date.date().isoformat()
        period_end = end_date.date().isoformat()

    return (
        period_start,
        period_end,
        statement_date,
        payment_due_date,
        statement_balance,
        minimum_payment_due,
        transactions,
    )


def write_spreadsheet(path: Path, rows: list[dict[str, Any]]) -> None:
    workbook = Workbook()
    worksheet = workbook.active
    worksheet.title = "Transactions"
    worksheet.append(["Date", "Merchant", "Amount"])
    for cell in worksheet[1]:
        cell.font = Font(bold=True, color="FFFFFF")
        cell.fill = PatternFill("solid", fgColor="243746")
    for row in rows:
        worksheet.append(
            [datetime.strptime(row["date"], "%Y-%m-%d").date(), row["merchant"], row["amount"]]
        )
        worksheet.cell(worksheet.max_row, 1).number_format = "yyyy-mm-dd"
        worksheet.cell(worksheet.max_row, 2).data_type = "s"
        worksheet.cell(worksheet.max_row, 3).number_format = '$#,##0.00;-$#,##0.00'
    worksheet.freeze_panes = "A2"
    worksheet.auto_filter.ref = worksheet.dimensions
    worksheet.column_dimensions["A"].width = 16
    worksheet.column_dimensions["B"].width = 70
    worksheet.column_dimensions["C"].width = 16
    path.parent.mkdir(parents=True, exist_ok=True)
    workbook.save(path)


def main() -> int:
    parser = argparse.ArgumentParser(description="Parse an American Express Gold statement.")
    parser.add_argument("--input", type=Path, required=True, help="Statement PDF")
    parser.add_argument("--json-output", type=Path, required=True)
    parser.add_argument("--spreadsheet-output", type=Path, required=True)
    parser.add_argument("--period-start")
    parser.add_argument("--period-end")
    args = parser.parse_args()

    try:
        (
            period_start,
            period_end,
            statement_date,
            payment_due_date,
            statement_balance,
            minimum_payment_due,
            transactions,
        ) = extract_statement(args.input, args.period_start, args.period_end)
        write_spreadsheet(args.spreadsheet_output, transactions)
        args.json_output.parent.mkdir(parents=True, exist_ok=True)
        args.json_output.write_text(
            json.dumps(
                {
                    "period_start": period_start,
                    "period_end": period_end,
                    "statement_date": statement_date,
                    "payment_due_date": payment_due_date,
                    "statement_balance": statement_balance,
                    "minimum_payment_due": minimum_payment_due,
                    "transactions": transactions,
                }
            ),
            encoding="utf-8",
        )
    except (OSError, RuntimeError, ValueError) as exc:
        print(f"Error: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
