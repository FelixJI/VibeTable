"""Generate the four owned independent-producer PDF fixtures for A6 qualification."""

from __future__ import annotations

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
PLAIN_TOKEN = "A6INDEPENDENTPLAIN"
FLATE_TOKEN = "A6INDEPENDENTFLATE"


def main() -> None:
    # Keep these imports inside main so ordinary corpus generation needs no extra package.
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

    def encrypted(source: Path, name: str, user_password: str) -> None:
        reader = PdfReader(str(source))
        writer = PdfWriter()
        for page in reader.pages:
            writer.add_page(page)
        writer.encrypt(
            user_password=user_password,
            owner_password="a6-owned-owner-password",
            algorithm="AES-256",
        )
        with (destination / name).open("wb") as output:
            writer.write(output)

    plain = reportlab_pdf(FIXTURE_NAMES[0], PLAIN_TOKEN, compressed=False)
    flate = reportlab_pdf(FIXTURE_NAMES[1], FLATE_TOKEN, compressed=True)
    encrypted(plain, FIXTURE_NAMES[2], "a6-owned-user-password")
    encrypted(flate, FIXTURE_NAMES[3], "")


if __name__ == "__main__":
    main()
