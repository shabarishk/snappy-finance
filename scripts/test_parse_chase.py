import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import pymupdf
from openpyxl import load_workbook

import parse_chase


class ChaseStatementParserTests(unittest.TestCase):
    def create_statement(self, path: Path, include_period: bool = True) -> None:
        document = pymupdf.open()
        page = document.new_page(width=612, height=792)
        if include_period:
            page.insert_text((20, 30), "Opening/Closing Date")
            page.insert_text((20, 45), "10/05/26 - 11/04/26")
        page.insert_text((20, 65), "Statement Date:")
        page.insert_text((20, 80), "11/04/26")
        page.insert_text((20, 100), "New Balance:")
        page.insert_text((20, 115), "$1,234.56")
        page.insert_text((20, 135), "Minimum Payment Due:")
        page.insert_text((20, 150), "$25.00")
        page.insert_text((20, 170), "Payment Due Date:")
        page.insert_text((20, 185), "11/25/26")

        page.insert_text((20, 190), "ACCOUNT ACTIVITY")
        page.insert_text((20, 220), "PAYMENTS AND OTHER CREDITS")
        self.insert_transaction(page, 245, "11/01", "Payment from bank", "$50.00")
        page.insert_text((20, 275), "PURCHASES")
        self.insert_transaction(page, 300, "10/03", "Boundary purchase", "$12.34")
        self.insert_transaction(page, 325, "10/02", "Outside period", "$99.00")
        self.insert_transaction(page, 350, "11/03", "Coffee Shop", "$4.56")

        document.save(path)
        document.close()

    @staticmethod
    def insert_transaction(
        page: pymupdf.Page, y: int, date: str, merchant: str, amount: str
    ) -> None:
        page.insert_text((20, y), date)
        page.insert_text((140, y), merchant)
        page.insert_text((520, y), amount)

    def test_extracts_app_statement_data_and_filters_to_period_boundary(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            pdf_path = Path(temporary_directory) / "chase.pdf"
            self.create_statement(pdf_path)

            start, end, balance, minimum_due, due_date, rows = parse_chase.extract_statement(pdf_path)

            self.assertEqual((start, end), ("2026-10-05", "2026-11-04"))
            self.assertEqual((balance, minimum_due, due_date), (1234.56, 25.0, "2026-11-25"))
            self.assertEqual(
                rows,
                [
                    {"date": "2026-10-03", "merchant": "Boundary purchase", "amount": -12.34},
                    {"date": "2026-11-01", "merchant": "Payment from bank", "amount": 50.0},
                    {"date": "2026-11-03", "merchant": "Coffee Shop", "amount": -4.56},
                ],
            )

    def test_accepts_application_period_overrides(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            pdf_path = Path(temporary_directory) / "chase.pdf"
            self.create_statement(pdf_path, include_period=False)

            start, end, _, _, _, _ = parse_chase.extract_statement(
                pdf_path, "2026-10-05", "2026-11-04"
            )

            self.assertEqual((start, end), ("2026-10-05", "2026-11-04"))

    def test_application_cli_writes_json_and_standard_spreadsheet(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = Path(temporary_directory)
            pdf_path = root / "chase.pdf"
            json_path = root / "statement.json"
            spreadsheet_path = root / "statement.xlsx"
            self.create_statement(pdf_path)

            with patch(
                "sys.argv",
                [
                    "parse_chase.py",
                    "--input",
                    str(pdf_path),
                    "--json-output",
                    str(json_path),
                    "--spreadsheet-output",
                    str(spreadsheet_path),
                ],
            ):
                self.assertEqual(parse_chase.main(), 0)

            parsed = json.loads(json_path.read_text(encoding="utf-8"))
            self.assertEqual(parsed["period_start"], "2026-10-05")
            self.assertEqual(parsed["period_end"], "2026-11-04")
            self.assertEqual(parsed["statement_balance"], 1234.56)
            self.assertEqual(parsed["minimum_payment_due"], 25.0)
            self.assertEqual(parsed["payment_due_date"], "2026-11-25")
            self.assertEqual(len(parsed["transactions"]), 3)

            worksheet = load_workbook(spreadsheet_path, data_only=True)["Transactions"]
            self.assertEqual(
                [cell.value for cell in worksheet[1]],
                ["Date", "Merchant", "Amount"],
            )
            self.assertEqual(worksheet.max_row, 4)


if __name__ == "__main__":
    unittest.main()
