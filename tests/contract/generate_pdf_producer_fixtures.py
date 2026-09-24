"""Generate owned independent-producer PDF fixtures for A6 qualification."""

from __future__ import annotations

import argparse
from io import BytesIO
from pathlib import Path

EXPECTED_VERSIONS = {
    "reportlab": "4.4.9",
    "pypdf": "6.10.0",
    "cryptography": "50.0.1",
}
FIXTURE_NAMES = (
    "independent-reportlab-plain.pdf",
    "independent-reportlab-flate.pdf",
    "independent-aes-user-password.pdf",
    "independent-aes-empty-user-password.pdf",
)
NEW_FIXTURE_NAMES = (
    "independent-reportlab-form-reused.pdf",
    "independent-pypdf-merged-rotated.pdf",
    "independent-rc4-128-empty-user-password.pdf",
    "independent-aes-128-empty-user-password.pdf",
)
PLAIN_TOKEN = "A6INDEPENDENTPLAIN"
FLATE_TOKEN = "A6INDEPENDENTFLATE"
FORM_TOKEN = "A6FORMVISIBLE"
PAGE_ONE_TOKEN = "A6PAGEONE"
PAGE_TWO_TOKEN = "A6PAGETWO"


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--new-only",
        action="store_true",
        help="generate only the four new fixtures; leave the original four untouched",
    )
    args = parser.parse_args()

    # Ordinary corpus generation and CI only read committed fixtures.
    import cryptography
    import pypdf
    import reportlab
    from pypdf import PdfReader, PdfWriter
    from reportlab.pdfgen import canvas

    versions = {
        "reportlab": reportlab.Version,
        "pypdf": pypdf.__version__,
        "cryptography": cryptography.__version__,
    }
    if versions != EXPECTED_VERSIONS:
        raise RuntimeError(f"unexpected producer tool versions: {versions}")

    destination = Path(__file__).with_name("pdf_producer_fixtures")
    destination.mkdir(parents=True, exist_ok=True)

    def reportlab_pdf(name: str, token: str, compressed: bool) -> Path:
        path = destination / name
        document = canvas.Canvas(str(path), pageCompression=1 if compressed else 0)
        document.setFont("Helvetica", 12)
        document.drawString(72, 720, token)
        document.save()
        return path

    def page_bytes(token: str) -> bytes:
        output = BytesIO()
        document = canvas.Canvas(output)
        document.setFont("Helvetica", 12)
        document.drawString(72, 720, token)
        document.save()
        return output.getvalue()

    def encrypted(
        source: Path | bytes, name: str, user_password: str, algorithm: str = "AES-256"
    ) -> None:
        reader = PdfReader(str(source) if isinstance(source, Path) else BytesIO(source))
        writer = PdfWriter()
        for page in reader.pages:
            writer.add_page(page)
        writer.encrypt(
            user_password=user_password,
            owner_password="a6-owned-owner-password",
            algorithm=algorithm,
        )
        with (destination / name).open("wb") as output:
            writer.write(output)

    if args.new_only:
        form = canvas.Canvas(str(destination / NEW_FIXTURE_NAMES[0]))
        form.beginForm("A6RepeatedForm", 0, 0, 220, 50)
        form.setFont("Helvetica", 12)
        form.drawString(0, 20, FORM_TOKEN)
        form.endForm()
        for y in (650, 600):
            form.saveState()
            form.translate(72, y)
            form.doForm("A6RepeatedForm")
            form.restoreState()
        form.save()

        writer = PdfWriter()
        writer.append(PdfReader(BytesIO(page_bytes(PAGE_ONE_TOKEN))))
        writer.append(PdfReader(BytesIO(page_bytes(PAGE_TWO_TOKEN))))
        writer.pages[1].rotate(90)
        with (destination / NEW_FIXTURE_NAMES[1]).open("wb") as output:
            writer.write(output)

        encrypted(page_bytes("A6RC4HIDDEN"), NEW_FIXTURE_NAMES[2], "", "RC4-128")
        encrypted(page_bytes("A6AES128HIDDEN"), NEW_FIXTURE_NAMES[3], "", "AES-128")
        return

    plain = reportlab_pdf(FIXTURE_NAMES[0], PLAIN_TOKEN, compressed=False)
    flate = reportlab_pdf(FIXTURE_NAMES[1], FLATE_TOKEN, compressed=True)
    encrypted(plain, FIXTURE_NAMES[2], "a6-owned-user-password")
    encrypted(flate, FIXTURE_NAMES[3], "")


if __name__ == "__main__":
    main()
