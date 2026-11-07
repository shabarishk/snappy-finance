#!/usr/bin/env python3
"""Extract transactions from a searchable Chase credit-card statement PDF."""

from __future__ import annotations

import argparse
import json
import re
import sys
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any

import pymupdf
from openpyxl import Workbook
from openpyxl.styles import Font, PatternFill


SHORT_DATE_PATTERN = re.compile(r"^(?P<month>\d{1,2})/(?P<day>\d{1,2})(?:/(?P<year>\d{2,4}))?$")
AMOUNT_PATTERN = re.compile(r"^[+-]?\$?-?(?:\d[\d,]*)?\.\d{2}$")
OPENING_CLOSING_DATE_PATTERN = re.compile(
    r"Opening/Closing Date\s*:?\s*(\d{1,2}/\d{1,2}/\d{2,4})\s*-\s*"
    r"(\d{1,2}/\d{1,2}/\d{2,4})",
    re.IGNORECASE,
)
STATEMENT_DATE_PATTERN = re.compile(
    r"Statement\s+Date:\s*(\d{1,2}/\d{1,2}/\d{2,4})", re.IGNORECASE
)
STATEMENT_BALANCE_PATTERN = re.compile(
    r"\b(?:new\s+balance|statement\s+balance|total\s+balance)\b"
    r"\s*:?\s*\$?\s*((?:\d{1,3}(?:,\d{3})+|\d+)\.\d{2})",
    re.IGNORECASE,
)
MINIMUM_PAYMENT_DUE_PATTERN = re.compile(
    r"\bminimum\s+(?:payment|balance|amount)\s+due\b"
    r"\s*:?\s*\$?\s*((?:\d{1,3}(?:,\d{3})+|\d+)\.\d{2})",
    re.IGNORECASE,
)
INTEREST_SAVING_BALANCE_DUE_PATTERN = re.compile(
    r"\binterest\s+saving\s+balance(?:\s+due)?\b"
    r"\s*:?\s*\$?\s*((?:\d{1,3}(?:,\d{3})+|\d+)\.\d{2})",
    re.IGNORECASE,
)
PAYMENT_DUE_DATE_PATTERN = re.compile(
    r"\bpayment\s+due\s+date\b\s*:?\s*(\d{1,2}/\d{1,2}/\d{2,4})",
    re.IGNORECASE,
)
ACTIVITY_END_PATTERN = re.compile(
    r"CHASE\s+PAY\s+OVER\s+TIME|INTEREST\s+CHARGES", re.IGNORECASE
)
CATEGORY_PATTERNS = (
    (
        re.compile(r"PAYMENTS?\s+(?:AND\s+)?(?:OTHER\s+)?CREDITS?", re.IGNORECASE),
        "credit",
    ),
    (re.compile(r"PURCHASES?", re.IGNORECASE), "charge"),
    (re.compile(r"FEES?\s+CHARGED", re.IGNORECASE), "charge"),
    (re.compile(r"INTEREST\s+CHARGED", re.IGNORECASE), "charge"),
    (re.compile(r"CASH\s+ADVANCES?", re.IGNORECASE), "charge"),
    (re.compile(r"BALANCE\s+TRANSFERS?", re.IGNORECASE), "charge"),
)


def parse_statement_date(document: pymupdf.Document) -> datetime | None:
    for page in document:
        match = STATEMENT_DATE_PATTERN.search(page.get_text())
        if match:
            return parse_slash_date(match.group(1))

    for page in document:
        match = OPENING_CLOSING_DATE_PATTERN.search(page.get_text())
        if match:
            return parse_slash_date(match.group(2))
    return None


def parse_slash_date(text: str) -> datetime:
    date_format = "%m/%d/%y" if len(text.split("/")[-1]) == 2 else "%m/%d/%Y"
    return datetime.strptime(text, date_format)


def extract_statement_period(text: str) -> tuple[str | None, str | None]:
    match = OPENING_CLOSING_DATE_PATTERN.search(re.sub(r"\s+", " ", text))
    if not match:
        return None, None
    start = parse_slash_date(match.group(1))
    end = parse_slash_date(match.group(2))
    if start > end:
        return None, None
    return start.date().isoformat(), end.date().isoformat()


def extract_statement_balance(
    text: str,
) -> tuple[float | None, float | None, float | None, str | None]:
    normalized_text = re.sub(r"\s+", " ", text)
    balance_match = STATEMENT_BALANCE_PATTERN.search(normalized_text)
    minimum_payment_match = MINIMUM_PAYMENT_DUE_PATTERN.search(normalized_text)
    interest_saving_balance_match = INTEREST_SAVING_BALANCE_DUE_PATTERN.search(normalized_text)
    due_date_match = PAYMENT_DUE_DATE_PATTERN.search(normalized_text)
    balance = (
        float(balance_match.group(1).replace(",", ""))
        if balance_match
        else None
    )
    minimum_payment_due = (
        float(minimum_payment_match.group(1).replace(",", ""))
        if minimum_payment_match
        else None
    )
    interest_saving_balance_due = (
        float(interest_saving_balance_match.group(1).replace(",", ""))
        if interest_saving_balance_match
        else None
    )
    due_date = (
        parse_slash_date(due_date_match.group(1)).date().isoformat()
        if due_date_match
        else None
    )
    return balance, minimum_payment_due, interest_saving_balance_due, due_date


def parse_transaction_date(text: str, statement_date: datetime) -> datetime | None:
    match = SHORT_DATE_PATTERN.fullmatch(text.strip())
    if not match:
        return None

    month = int(match.group("month"))
    day = int(match.group("day"))
    year_text = match.group("year")
    if year_text:
        year = int(year_text)
        if year < 100:
            year += 2000
    else:
        year = statement_date.year
        month_delta = month - statement_date.month
        if month_delta > 6:
            year -= 1
        elif month_delta < -6:
            year += 1

    try:
        return datetime(year, month, day)
    except ValueError:
        return None


def parse_amount_token(text: str) -> float | None:
    cleaned = text.strip().replace("−", "-")
    if not AMOUNT_PATTERN.fullmatch(cleaned):
        return None
    cleaned = cleaned.replace("$", "").replace(",", "")
    return float(cleaned)


def group_page_lines(page: pymupdf.Page) -> list[tuple[float, list[tuple[float, str]]]]:
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


def extract_statement(
    pdf_path: Path,
    override_start: str | None = None,
    override_end: str | None = None,
) -> tuple[str | None, str | None, float | None, float | None, float | None, str | None, list[dict[str, Any]]]:
    with pymupdf.open(pdf_path) as document:
        statement_text = " ".join(page.get_text() for page in document)
        period_start, period_end = extract_statement_period(statement_text)
        (
            statement_balance,
            minimum_payment_due,
            interest_saving_balance_due,
            payment_due_date,
        ) = extract_statement_balance(statement_text)

        if override_start or override_end:
            if not override_start or not override_end:
                raise RuntimeError("Both statement period dates must be provided.")
            start_date = parse_iso_date(override_start, "Statement period start")
            end_date = parse_iso_date(override_end, "Statement period end")
            if start_date > end_date:
                raise RuntimeError("Statement period start must be on or before its end.")
            period_start, period_end = start_date.date().isoformat(), end_date.date().isoformat()

        statement_date = parse_statement_date(document)
        if statement_date is None and period_end:
            statement_date = datetime.strptime(period_end, "%Y-%m-%d")
        if statement_date is None:
            raise RuntimeError(
                "Could not find the statement date. This parser expects a Chase "
                "statement with a searchable text layer."
            )

        first_included_date = None
        last_included_date = None
        if period_start and period_end:
            first_included_date = (
                datetime.strptime(period_start, "%Y-%m-%d").date() - timedelta(days=2)
            )
            last_included_date = (
                datetime.strptime(period_end, "%Y-%m-%d").date() + timedelta(days=2)
            )

        rows: list[dict[str, Any]] = []
        current_category: str | None = None
        in_activity = False
        pending: dict[str, Any] | None = None

        def save_pending() -> None:
            nonlocal pending
            if pending is not None:
                rows.append(pending)
                pending = None

        for page in document:
            page_lines = group_page_lines(page)
            for _, positioned_words in page_lines:
                text = " ".join(word for _, word in positioned_words)
                normalized = re.sub(r"\s+", " ", text).strip()

                if re.search(r"\bACCOUNT ACTIVITY\b", normalized, re.IGNORECASE):
                    save_pending()
                    in_activity = True
                    current_category = None
                    continue
                if not in_activity:
                    continue

                if ACTIVITY_END_PATTERN.search(normalized):
                    save_pending()
                    in_activity = False
                    current_category = None
                    continue

                category = next(
                    (kind for pattern, kind in CATEGORY_PATTERNS if pattern.fullmatch(normalized)),
                    None,
                )
                if category:
                    save_pending()
                    current_category = category
                    continue

                if re.search(
                    r"Date of Transaction|Merchant Name|Transaction Description|\$ Amount|"
                    r"TOTAL FEES FOR THIS PERIOD|Total fees charged|Total interest charged|"
                    r"Year-to-date totals",
                    normalized,
                    re.IGNORECASE,
                ):
                    save_pending()
                    continue

                date_text = next(
                    (
                        word
                        for x, word in positioned_words
                        if x < page.rect.width * 0.2
                        and SHORT_DATE_PATTERN.fullmatch(word)
                    ),
                    None,
                )
                transaction_date = (
                    parse_transaction_date(date_text, statement_date) if date_text else None
                )

                if transaction_date:
                    save_pending()
                    if current_category is None:
                        continue

                    description_words = [
                        word
                        for x, word in positioned_words
                        if page.rect.width * 0.18 <= x < page.rect.width * 0.82
                    ]
                    amount_words = [
                        word
                        for x, word in positioned_words
                        if x >= page.rect.width * 0.82
                    ]
                    amount = next(
                        (
                            parse_amount_token(word)
                            for word in amount_words
                            if parse_amount_token(word) is not None
                        ),
                        None,
                    )
                    if amount is not None:
                        amount = abs(amount) if current_category == "credit" else -abs(amount)

                    pending = {
                        "Date": transaction_date,
                        "Description": " ".join(description_words).strip(),
                        "Amount": amount,
                    }
                    continue

                if pending:
                    description_words = [
                        word
                        for x, word in positioned_words
                        if page.rect.width * 0.18 <= x < page.rect.width * 0.82
                    ]
                    if description_words:
                        pending["Description"] = " ".join(
                            part
                            for part in (pending["Description"], " ".join(description_words))
                            if part
                        )

            save_pending()

    included_rows = [
        row
        for row in rows
        if first_included_date is None
        or first_included_date <= row["Date"].date() <= last_included_date
    ]
    if not included_rows:
        raise RuntimeError(
            f"No transaction rows were found in {pdf_path.name}. "
            "The PDF must be a Chase statement with a searchable text layer."
        )
    if any(row["Amount"] is None or not row["Description"] for row in included_rows):
        incomplete = sum(
            row["Amount"] is None or not row["Description"] for row in included_rows
        )
        raise RuntimeError(
            f"Found {len(included_rows)} rows, but {incomplete} row(s) had a missing "
            "description or amount. Review the PDF layout before using the workbook."
        )

    transactions = [
        {
            "date": row["Date"].date().isoformat(),
            "merchant": re.sub(r"\s+", " ", row["Description"]).strip(),
            "amount": round(float(row["Amount"]), 2),
        }
        for row in included_rows
    ]
    transactions.sort(key=lambda row: (row["date"], row["merchant"], row["amount"]))
    return (
        period_start,
        period_end,
        statement_balance,
        minimum_payment_due,
        interest_saving_balance_due,
        payment_due_date,
        transactions,
    )


def parse_iso_date(value: str, label: str) -> datetime:
    try:
        return datetime.strptime(value, "%Y-%m-%d")
    except ValueError as exc:
        raise RuntimeError(f"{label} must be YYYY-MM-DD.") from exc


def extract_transactions(pdf_path: Path) -> list[dict[str, Any]]:
    _, _, _, _, _, _, rows = extract_statement(pdf_path)
    return [
        {
            "Date": datetime.strptime(row["date"], "%Y-%m-%d"),
            "Description": row["merchant"],
            "Amount": row["amount"],
        }
        for row in rows
    ]


def write_workbook(path: Path, rows: list[dict[str, Any]]) -> None:
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
    parser = argparse.ArgumentParser(description="Parse a Chase statement for Snappy Finance.")
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
            statement_balance,
            minimum_payment_due,
            interest_saving_balance_due,
            payment_due_date,
            rows,
        ) = extract_statement(args.input, args.period_start, args.period_end)
        write_workbook(args.spreadsheet_output, rows)
        args.json_output.parent.mkdir(parents=True, exist_ok=True)
        args.json_output.write_text(
            json.dumps(
                {
                    "period_start": period_start,
                    "period_end": period_end,
                    "statement_balance": statement_balance,
                    "minimum_payment_due": minimum_payment_due,
                    "interest_saving_balance_due": interest_saving_balance_due,
                    "payment_due_date": payment_due_date,
                    "transactions": rows,
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
