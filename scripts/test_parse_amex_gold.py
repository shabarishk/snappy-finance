import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import pymupdf
from openpyxl import load_workbook

import parse_amex_gold


class AmexGoldStatementParserTests(unittest.TestCase):
    def create_statement(self, path: Path) -> None:
        document = pymupdf.open()
        summary = document.new_page(width=612, height=792)
        summary.insert_text((20, 30), "American Express Gold Card")
        summary.insert_text((20, 50), "Closing Date 11/06/26")
        summary.insert_text((20, 80), "Payment Summary")
        summary.insert_text((20, 100), "New Balance")
        summary.insert_text((20, 115), "$361.88")
        summary.insert_text((20, 140), "Minimum Payment Due")
        summary.insert_text((20, 155), "$40.00")
        summary.insert_text((20, 180), "Payment Due Date")
        summary.insert_text((20, 195), "12/01/26")

        details = document.new_page(width=612, height=792)
        details.insert_text((50, 30), "Payments Details")
        self.insert_header(details, 50)
        self.insert_transaction(details, 75, "11/01/26*", "AUTOPAY PAYMENT RECEIVED - THANK YOU", "-$400.47")
        details.insert_text((50, 110), "Credits Details")
        self.insert_header(details, 130)
        self.insert_transaction(details, 155, "10/12/26", "AMEX Dining Credit", "-$10.00")
        details.insert_text((50, 190), "New Charges Details")
        self.insert_header(details, 210)
        self.insert_transaction(details, 235, "10/09/26", "COVE SMART DRAPER UT", "$25.99")

        continued_details = document.new_page(width=612, height=792)
        self.insert_header(continued_details, 50)
        self.insert_transaction(continued_details, 75, "11/02/26", "AMAZON MARKETPLACE", "$.75")
        continued_details.insert_text((50, 110), "Fees")

        document.save(path)
        document.close()

    @staticmethod
    def insert_header(page: pymupdf.Page, y: int) -> None:
        page.insert_text((50, y), "Date")
        page.insert_text((101, y), "Description")
        page.insert_text((451, y), "Type")
        page.insert_text((553, y), "Amount")

    @staticmethod
    def insert_transaction(
        page: pymupdf.Page, y: int, date: str, merchant: str, amount: str
    ) -> None:
        page.insert_text((50, y), date)
        page.insert_text((101, y), merchant)
        page.insert_text((451, y), "Pay Over Time")
        page.insert_text((553, y), amount)

    def test_extracts_summary_and_transactions_across_detail_pages(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            pdf_path = Path(temporary_directory) / "amex-gold.pdf"
            self.create_statement(pdf_path)

            period_start, period_end, statement_date, due_date, balance, minimum_due, transactions = (
                parse_amex_gold.extract_statement(pdf_path)
            )

        self.assertIsNone(period_start)
        self.assertEqual(period_end, "2026-11-06")
        self.assertEqual(statement_date, "2026-11-06")
        self.assertEqual(due_date, "2026-12-01")
        self.assertEqual((balance, minimum_due), (361.88, 40.0))
        self.assertEqual(
            transactions,
            [
                {"date": "2026-10-09", "merchant": "COVE SMART DRAPER UT", "amount": -25.99},
                {"date": "2026-10-12", "merchant": "AMEX Dining Credit", "amount": 10.0},
                {"date": "2026-11-01", "merchant": "AUTOPAY PAYMENT RECEIVED - THANK YOU", "amount": 400.47},
                {"date": "2026-11-02", "merchant": "AMAZON MARKETPLACE", "amount": -0.75},
            ],
        )

    def test_accepts_application_period_overrides(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            pdf_path = Path(temporary_directory) / "amex-gold.pdf"
            self.create_statement(pdf_path)

            period_start, period_end, statement_date, *_ = parse_amex_gold.extract_statement(
                pdf_path, "2026-10-07", "2026-11-06"
            )

        self.assertEqual((period_start, period_end), ("2026-10-07", "2026-11-06"))
        self.assertEqual(statement_date, "2026-11-06")

    def test_cli_writes_json_and_spreadsheet(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = Path(temporary_directory)
            pdf_path = root / "amex-gold.pdf"
            json_path = root / "statement.json"
            spreadsheet_path = root / "statement.xlsx"
            self.create_statement(pdf_path)

            with patch(
                "sys.argv",
                [
                    "parse_amex_gold.py",
                    "--input",
                    str(pdf_path),
                    "--json-output",
                    str(json_path),
                    "--spreadsheet-output",
                    str(spreadsheet_path),
                ],
            ):
                self.assertEqual(parse_amex_gold.main(), 0)

            parsed = json.loads(json_path.read_text(encoding="utf-8"))
            self.assertIsNone(parsed["period_start"])
            self.assertEqual(parsed["period_end"], "2026-11-06")
            self.assertEqual(parsed["statement_date"], "2026-11-06")
            self.assertEqual(parsed["payment_due_date"], "2026-12-01")
            self.assertEqual(parsed["statement_balance"], 361.88)
            self.assertEqual(parsed["minimum_payment_due"], 40.0)
            self.assertEqual(len(parsed["transactions"]), 4)

            worksheet = load_workbook(spreadsheet_path, data_only=True)["Transactions"]
            self.assertEqual(worksheet.max_row, 5)


if __name__ == "__main__":
    unittest.main()
