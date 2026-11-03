import tempfile
import unittest
from datetime import date
from pathlib import Path

import pymupdf
from openpyxl import load_workbook

import parse_bilt
import parse_bilt_app


class BiltStatementParserTests(unittest.TestCase):
    def test_recognizes_current_and_legacy_transaction_sections(self):
        section_pattern = parse_bilt.SECTION_PATTERNS[1]
        self.assertRegex("Your transactions", section_pattern)
        self.assertRegex("Transactions", section_pattern)
        self.assertNotRegex(
            "Shabarish's wo transactions . Authorized user",
            section_pattern,
        )

    def test_recognizes_ocr_noise_in_authorized_user_section_header(self):
        self.assertRegex(
            "Shabarish's wo transactions . Authorized user",
            parse_bilt.SECTION_PATTERNS[2],
        )

    def test_extracts_transactions_under_legacy_heading(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            pdf_path = Path(temporary_directory) / "legacy-bilt.pdf"
            document = pymupdf.open()
            page = document.new_page(width=612, height=792)
            page.insert_text((20, 60), "Transactions")
            page.insert_text((20, 90), "Oct 06, 2026")
            page.insert_text((180, 90), "Coffee Shop")
            page.insert_text((510, 90), "$12.34")
            document.save(pdf_path)
            document.close()

            _, _, _, _, rows = parse_bilt_app.extract_rows(pdf_path)
            self.assertEqual(
                rows,
                [{"date": "2026-10-06", "merchant": "Coffee Shop", "amount": -12.34}],
            )

    def test_filters_rows_outside_period_and_keeps_two_day_boundary_rows(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            pdf_path = Path(temporary_directory) / "statement-range.pdf"
            document = pymupdf.open()
            page = document.new_page(width=612, height=792)
            page.insert_text((20, 30), "Statement period: 10/01/2026 - 10/31/2026")
            page.insert_text((20, 60), "Transactions")
            for y, day, merchant in (
                (90, "Sep 29, 2026", "At boundary"),
                (120, "Sep 28, 2026", "Too early"),
                (150, "Nov 03, 2026", "Too late"),
            ):
                page.insert_text((20, y), day)
                page.insert_text((180, y), merchant)
                page.insert_text((510, y), "$10.00")
            document.save(pdf_path)
            document.close()

            _, _, _, _, rows = parse_bilt_app.extract_rows(pdf_path)
            self.assertEqual(
                rows,
                [{"date": "2026-09-29", "merchant": "At boundary", "amount": -10.0}],
            )

    def test_extracts_statement_period(self):
        self.assertEqual(
            parse_bilt_app.extract_statement_period(
                "Statement period: 10/01/2026 - 10/31/2026"
            ),
            ("2026-10-01", "2026-10-31"),
        )
        self.assertEqual(
            parse_bilt_app.extract_statement_period(
                "Billing cycle October 1, 2026 through October 31, 2026"
            ),
            ("2026-10-01", "2026-10-31"),
        )
        self.assertEqual(
            parse_bilt_app.extract_statement_period(
                "Bilt Palladium Card Sep 27 - Oct 26, 2026"
            ),
            ("2026-09-27", "2026-10-26"),
        )
        self.assertEqual(parse_bilt_app.extract_statement_period("No period here"), (None, None))

    def test_extracts_statement_balance_and_due_date(self):
        self.assertEqual(
            parse_bilt_app.extract_statement_balance(
                "New Balance: $1,234.56 Payment Due Date: November 21, 2026"
            ),
            (1234.56, "2026-11-21"),
        )
        self.assertEqual(
            parse_bilt_app.extract_statement_balance("No balance or due date"),
            (None, None),
        )

    def test_extracts_standardized_transactions_and_spreadsheet(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = Path(temporary_directory)
            pdf_path = root / "bilt.pdf"
            document = pymupdf.open()
            page = document.new_page(width=612, height=792)
            page.insert_text((20, 30), "Statement period: 10/01/2026 - 10/31/2026")
            page.insert_text((20, 60), "Payment and credits")
            page.insert_text((20, 90), "Oct 05, 2026")
            page.insert_text((180, 90), "Payment from bank")
            page.insert_text((510, 90), "$50.00")
            page.insert_text((20, 120), "Your transactions")
            page.insert_text((20, 150), "Oct 06, 2026")
            page.insert_text((180, 150), "Coffee Shop")
            page.insert_text((510, 150), "$12.34")
            page.insert_text((20, 180), "Shabarish's transactions")
            page.insert_text((20, 210), "Oct 08, 2026")
            page.insert_text((180, 210), "Grocery Mart")
            page.insert_text((510, 210), "$30.00")
            page.insert_text((20, 700), "New Balance: $1,234.56")
            page.insert_text((20, 725), "Payment Due Date: November 21, 2026")
            document.save(pdf_path)
            document.close()

            start, end, balance, due_date, rows = parse_bilt_app.extract_rows(pdf_path)
            self.assertEqual((start, end), ("2026-10-01", "2026-10-31"))
            self.assertEqual((balance, due_date), (1234.56, "2026-11-21"))
            self.assertEqual(
                rows,
                [
                    {"date": "2026-10-05", "merchant": "Payment from bank", "amount": 50.0},
                    {"date": "2026-10-06", "merchant": "Coffee Shop", "amount": -12.34},
                ],
            )

            spreadsheet_path = root / "transactions.xlsx"
            parse_bilt_app.write_spreadsheet(spreadsheet_path, rows)
            workbook = load_workbook(spreadsheet_path, data_only=False)
            worksheet = workbook["Transactions"]
            self.assertEqual(
                [cell.value for cell in worksheet[1]],
                ["Date", "Merchant", "Amount"],
            )
            self.assertEqual(worksheet.max_column, 3)
            self.assertEqual(worksheet["A2"].value.date(), date(2026, 10, 5))

    def test_spreadsheet_stores_merchant_as_text(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            path = Path(temporary_directory) / "transactions.xlsx"
            parse_bilt_app.write_spreadsheet(
                path,
                [{"date": "2026-10-01", "merchant": "=1+1", "amount": 2.0}],
            )
            cell = load_workbook(path, data_only=False)["Transactions"]["B2"]
            self.assertEqual(cell.value, "=1+1")
            self.assertEqual(cell.data_type, "s")


if __name__ == "__main__":
    unittest.main()
