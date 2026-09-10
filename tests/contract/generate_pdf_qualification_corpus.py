"""Generate the owned A6 PDF decision corpus; no third-party fixture downloads."""

import json
import zlib
from pathlib import Path


def generated_documents() -> dict[str, bytes]:
    samples: dict[str, bytes] = {}

    def stream(data: bytes, filter_name: str = "") -> bytes:
        filter_value = f" /Filter /{filter_name}" if filter_name else ""
        return f"<< /Length {len(data)}{filter_value} >>\nstream\n".encode() + data + b"\nendstream"

    def document(objects: list[bytes], header_padding: bytes = b"") -> bytes:
        data = bytearray(b"%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
        data.extend(header_padding)
        offsets = []
        for number, value in enumerate(objects, 1):
            offsets.append(len(data))
            data.extend(f"{number} 0 obj\n".encode() + value + b"\nendobj\n")
        start = len(data)
        data.extend(f"xref\n0 {len(objects) + 1}\n0000000000 65535 f\r\n".encode())
        for offset in offsets:
            data.extend(f"{offset:010d} 00000 n\r\n".encode())
        data.extend(
            f"trailer\n<< /Size {len(objects) + 1} /Root 1 0 R >>\nstartxref\n{start}\n%%EOF\n".encode()
        )
        return bytes(data)

    common = [b"<< /Type /Catalog /Pages 2 0 R >>", b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>"]
    cmap = b"/CIDInit /ProcSet findresource begin\n12 dict begin begincmap\n/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def\n/CMapName /VibeTableNonIdentity def /CMapType 2 def\n1 begincodespacerange <0000> <FFFF> endcodespacerange\n4 beginbfchar <0001> <6570> <0002> <636E> <0003> <5DE5> <0004> <4F5C> endbfchar\nendcmap CMapName currentdict /CMap defineresource pop end end"
    objects = [
        *common,
        b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 8 0 R >>",
        b"<< /Type /Font /Subtype /Type0 /BaseFont /STSong-Light /Encoding /Identity-H /DescendantFonts [5 0 R] /ToUnicode 7 0 R >>",
        b"<< /Type /Font /Subtype /CIDFontType0 /BaseFont /STSong-Light /CIDSystemInfo << /Registry (Adobe) /Ordering (GB1) /Supplement 0 >> /FontDescriptor 6 0 R /DW 1000 >>",
        b"<< /Type /FontDescriptor /FontName /STSong-Light /Flags 6 /FontBBox [-25 -254 1000 880] /ItalicAngle 0 /Ascent 752 /Descent -271 /CapHeight 737 /StemV 58 >>",
        stream(cmap),
        stream(zlib.compress(b"BT /F1 12 Tf 72 720 Td <0001000200030004> Tj ET"), "FlateDecode"),
    ]
    samples["nonidentity-tounicode-cjk.pdf"] = document(objects)
    missing_mapping = list(objects)
    missing_mapping[6] = stream(
        cmap.replace(b"4 beginbfchar", b"3 beginbfchar").replace(b" <0004> <4F5C>", b"")
    )
    samples["missing-glyph-mapping.pdf"] = document(missing_mapping)
    no_mapping = list(objects)
    no_mapping[3] = no_mapping[3].replace(b" /ToUnicode 7 0 R", b"")
    samples["missing-tounicode.pdf"] = document(no_mapping)
    objects = [
        *common,
        b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
        b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
        stream(b"BT /F1 12 Tf 72 720 Td (Visible report) Tj ET"),
        stream(b"BT /F1 12 Tf 72 720 Td (ORPHAN FORBIDDEN) Tj ET"),
    ]
    samples["orphan-content.pdf"] = document(objects)
    large = [
        *common,
        b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
        b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
        stream(zlib.compress(b" " * (33 * 1024 * 1024)), "FlateDecode"),
    ]
    samples["flate-over-32m.pdf"] = document(large)
    compressed = zlib.compress(b"BT /F1 12 Tf 72 720 Td (Incomplete forbidden) Tj ET")
    large[4] = stream(compressed[:-4], "FlateDecode")
    samples["flate-missing-trailer.pdf"] = document(large)
    for missing in (1, 2, 3):
        large[4] = stream(compressed[:-missing], "FlateDecode")
        samples[f"flate-missing-trailer-{missing}.pdf"] = document(large)
    large[4] = stream(compressed[:-1] + bytes([compressed[-1] ^ 1]), "FlateDecode")
    samples["flate-wrong-adler.pdf"] = document(large)
    large[4] = stream(compressed + b"trailing", "FlateDecode")
    samples["flate-trailing-data.pdf"] = document(large)
    large[4] = stream(zlib.compress(b""), "FlateDecode")
    samples["flate-empty.pdf"] = document(large)
    cmf = 136
    flg = -cmf * 256 % 31
    large[4] = stream(bytes([cmf, flg]) + compressed[2:], "FlateDecode")
    samples["flate-invalid-window.pdf"] = document(large)
    encoder = zlib.compressobj(zdict=b"Incomplete forbidden")
    large[4] = stream(
        encoder.compress(b"BT /F1 12 Tf 72 720 Td (Incomplete forbidden) Tj ET") + encoder.flush(),
        "FlateDecode",
    )
    samples["flate-preset-dictionary.pdf"] = document(large)
    page_ids = [3 + index * 2 for index in range(9)]
    cumulative_objects = [
        common[0],
        f"<< /Type /Pages /Kids [{' '.join(f'{number} 0 R' for number in page_ids)}] /Count 9 >>".encode(),
    ]
    page_padding = zlib.compress(b" " * (32 * 1024 * 1024))
    for page_id in page_ids:
        cumulative_objects.extend(
            [
                f"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> /Contents {page_id + 1} 0 R >>".encode(),
                stream(page_padding, "FlateDecode"),
            ]
        )
    samples["flate-cumulative-over-256m.pdf"] = document(cumulative_objects)

    def xref_document(separator=b"\n", trailing=b"", length_delta=0):
        values = [
            *common,
            b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
            b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
            stream(b"BT /F1 12 Tf 72 720 Td (Xref boundary) Tj ET"),
        ]
        data = bytearray(b"%PDF-1.7\n")
        offsets = [0]
        for number, value in enumerate(values, 1):
            offsets.append(len(data))
            data.extend(f"{number} 0 obj\n".encode() + value + b"\nendobj\n")
        offsets.append(len(data))
        entries = b"\x00\x00\x00\x00\x00\xff\xff" + b"".join(
            b"\x01" + offset.to_bytes(4, "big") + b"\x00\x00" for offset in offsets[1:]
        )
        compressed = zlib.compress(entries) + trailing
        data.extend(
            f"6 0 obj\n<< /Type /XRef /Size 7 /Root 1 0 R /W [1 4 2] /Filter /FlateDecode /Length {len(compressed) + length_delta} >>\nstream\n".encode()
        )
        data.extend(compressed + separator + b"endstream\nendobj\n")
        data.extend(f"startxref\n{offsets[6]}\n%%EOF\n".encode())
        return bytes(data)

    for name, arguments in {
        "xref-lf": {},
        "xref-crlf": {"separator": b"\r\n"},
        "xref-trailing-inside-length": {"trailing": b"forbidden"},
        "xref-short-declared-length": {"length_delta": -1},
    }.items():
        samples[f"{name}.pdf"] = xref_document(**arguments)
    outside_page_objects = [
        b"<< /Type /Catalog /Pages 2 0 R /Metadata 6 0 R /Names << /EmbeddedFiles << /Names [(note.txt) 7 0 R] >> >> >>",
        common[1],
        b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R /Annots [9 0 R] >>",
        b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
        stream(b"BT /F1 12 Tf 72 720 Td (Visible boundary report) Tj ET"),
        stream(
            b'<x:xmpmeta xmlns:x="adobe:ns:meta/"><description>BT (METADATA FORBIDDEN) Tj ET</description></x:xmpmeta>'
        ).replace(b"<< /Length", b"<< /Type /Metadata /Subtype /XML /Length"),
        b"<< /Type /Filespec /F (note.txt) /EF << /F 8 0 R >> >>",
        stream(b"BT (ATTACHMENT FORBIDDEN) Tj ET").replace(
            b"<< /Length", b"<< /Type /EmbeddedFile /Length"
        ),
        b"<< /Type /Annot /Subtype /FreeText /Rect [72 500 250 530] /Contents (ANNOTATION FORBIDDEN) /DA (/F1 12 Tf 0 g) /AP << /N 12 0 R >> >>",
        b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 11 0 R >>",
        stream(b"BT /F1 12 Tf 72 720 Td (UNREACHABLE PAGE FORBIDDEN) Tj ET"),
        stream(b"BT /F1 12 Tf 0 0 Td (APPEARANCE FORBIDDEN) Tj ET").replace(
            b"<< /Length",
            b"<< /Type /XObject /Subtype /Form /BBox [0 0 180 30] /Resources << /Font << /F1 4 0 R >> >> /Length",
        ),
    ]
    samples["page-boundary-exclusions.pdf"] = document(outside_page_objects)
    image_objects = [
        *common,
        b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /XObject << /Im1 5 0 R >> >> /Contents 4 0 R >>",
        stream(b"q 200 0 0 200 72 500 cm /Im1 Do Q"),
        stream(bytes([255, 0, 0, 0, 255, 0, 0, 0, 255, 255, 255, 255])).replace(
            b"<< /Length",
            b"<< /Type /XObject /Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Length",
        ),
    ]
    samples["image-only-rgb.pdf"] = document(image_objects)
    input_limit = 64 * 1024 * 1024
    padding_size = input_limit - len(samples["image-only-rgb.pdf"])
    # Recompute xref offsets; decimal startxref growth changes the final size.
    for _ in range(3):
        comment = b"% padding\n"
        padding = comment * (padding_size // len(comment)) + b" " * (padding_size % len(comment))
        exact_input = document(image_objects, padding)
        difference = input_limit - len(exact_input)
        if difference == 0:
            break
        padding_size += difference
    else:
        raise ValueError("Exact PDF input boundary did not converge")
    samples["input-exact-64m.pdf"] = exact_input

    oversized_input = list(image_objects)
    oversized_input.append(stream(b" " * (64 * 1024 * 1024)))
    samples["input-over-64m.pdf"] = document(oversized_input)
    output_objects = [
        *common,
        b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 2000 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
        b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
        stream(b"BT /F1 0.001 Tf 10 720 Td " + (b"(" + b"B" * 500 + b") Tj\n") * 4001 + b"ET"),
    ]
    samples["output-over-2m.pdf"] = document(output_objects)
    text_operators = (
        b"BT /F1 12 Tf 72 720 Td "
        rb"(Operator \(report\) \\ path) Tj "
        b"0 -20 Td [(Array ) 0 (joined ) 0 (token)] TJ "
        rb"0 -20 Td (Octal \101\102\103) Tj ET"
    )
    for name, payload, filter_name in (
        ("text-operators-plain.pdf", text_operators, ""),
        ("text-operators-flate.pdf", zlib.compress(text_operators), "FlateDecode"),
    ):
        operator_objects = [
            *common,
            b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
            b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
            stream(payload, filter_name),
        ]
        samples[name] = document(operator_objects)
    large_first = zlib.compress(
        b"BT /F1 0.001 Tf 10 720 Td " + (b"(" + b"B" * 500 + b") Tj\n") * 4001 + b"ET"
    )
    valid_second = zlib.compress(b"BT /F1 12 Tf 72 720 Td (SECOND_PAGE_REACHABLE) Tj ET")
    invalid_second = valid_second[:-1] + bytes([valid_second[-1] ^ 1])
    for filename, second in (
        ("output-limit-later-valid-page.pdf", valid_second),
        ("output-limit-later-invalid-page.pdf", invalid_second),
    ):
        samples[filename] = document(
            [
                b"<< /Type /Catalog /Pages 2 0 R >>",
                b"<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>",
                b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 2000 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 6 0 R >>",
                b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 7 0 R >>",
                b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
                stream(large_first, "FlateDecode"),
                stream(second, "FlateDecode"),
            ]
        )
    return samples


def main() -> None:
    root = Path(__file__).resolve().parents[2]
    destination = root / "build" / "qa" / "pdf-qualification" / "v1"
    manifest = json.loads(
        (Path(__file__).with_name("pdf_qualification_corpus.json")).read_text(encoding="utf-8")
    )
    samples = generated_documents()
    if len(manifest["cases"]) != len(samples) or set(samples) != {
        case["file"] for case in manifest["cases"]
    }:
        raise ValueError("PDF corpus manifest and generator disagree")
    destination.mkdir(parents=True, exist_ok=True)
    if {path.name for path in destination.glob("*.pdf")} - samples.keys():
        raise ValueError("PDF corpus output contains undeclared samples; preserve and inspect it")
    for name, payload in samples.items():
        (destination / name).write_bytes(payload)
    print(json.dumps({"corpusVersion": manifest["corpusVersion"], "documents": len(samples)}))


if __name__ == "__main__":
    main()
