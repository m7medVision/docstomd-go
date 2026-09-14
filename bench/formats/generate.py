"""Generated formats corpus: deterministic docx/xlsx/pptx packages at several sizes.

Every document exercises the structures the bench counts (headings, lists,
tables, links, footnotes or notes) with prose varied enough for word
trigrams to be meaningful.
"""

from __future__ import annotations

import zipfile
from pathlib import Path
from xml.sax.saxutils import escape

ZIP_DATE = (2026, 1, 1, 0, 0, 0)
SIZES = {"small": 1, "medium": 12, "large": 80}

SUBJECTS = ["harbour", "ledger", "meadow", "turbine", "archive", "orchard", "signal", "quarry", "lantern", "glacier"]
VERBS = ["records", "shapes", "follows", "measures", "restores", "outlines", "balances", "delivers"]
OBJECTS = ["seasonal output", "regional demand", "maintenance windows", "survey results", "budget lines", "field notes"]

W = 'xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"'
R = 'xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"'
REL = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
PKG_REL = "http://schemas.openxmlformats.org/package/2006/relationships"
A = 'xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"'
P = 'xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"'
DECL = '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n'


def sentence(seed: int) -> str:
    subject = SUBJECTS[seed % len(SUBJECTS)]
    verb = VERBS[(seed * 3 + 1) % len(VERBS)]
    obj = OBJECTS[(seed * 7 + 2) % len(OBJECTS)]
    return f"The {subject} team {verb} {obj} for cycle {seed}."


def relationships(entries: list[tuple[str, str, str]], external: set[str] = frozenset()) -> str:
    rows = "".join(
        f'<Relationship Id="{rid}" Type="{REL}/{kind}" Target="{escape(target)}"'
        + (' TargetMode="External"' if rid in external else "")
        + "/>"
        for rid, kind, target in entries
    )
    return f'{DECL}<Relationships xmlns="{PKG_REL}">{rows}</Relationships>'


def content_types(defaults: dict[str, str], overrides: dict[str, str]) -> str:
    body = "".join(f'<Default Extension="{ext}" ContentType="{ct}"/>' for ext, ct in defaults.items())
    body += "".join(f'<Override PartName="{part}" ContentType="{ct}"/>' for part, ct in overrides.items())
    return f'{DECL}<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">{body}</Types>'


def write_package(path: Path, parts: dict[str, str]) -> None:
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as archive:
        for name, text in parts.items():
            info = zipfile.ZipInfo(name, date_time=ZIP_DATE)
            info.compress_type = zipfile.ZIP_DEFLATED
            archive.writestr(info, text.encode("utf-8"))


PACKAGE_DEFAULTS = {
    "rels": "application/vnd.openxmlformats-package.relationships+xml",
    "xml": "application/xml",
}


def docx(scale: int) -> dict[str, str]:
    def run(text: str) -> str:
        return f'<w:r><w:t xml:space="preserve">{escape(text)}</w:t></w:r>'

    def para(text: str, style: str | None = None, num: tuple[int, int] | None = None) -> str:
        props = ""
        if style or num:
            props = "<w:pPr>"
            if style:
                props += f'<w:pStyle w:val="{style}"/>'
            if num:
                props += f'<w:numPr><w:ilvl w:val="{num[1]}"/><w:numId w:val="{num[0]}"/></w:numPr>'
            props += "</w:pPr>"
        return f"<w:p>{props}{run(text)}</w:p>"

    body = []
    link_rels = []
    footnotes = []
    for section in range(scale):
        base = section * 20
        body.append(para(f"Section {section + 1} overview", "Heading1"))
        body.append(para(sentence(base) + " " + sentence(base + 1)))
        body.append(para(f"Details for part {section + 1}", "Heading2"))
        for item in range(3):
            body.append(para(sentence(base + 2 + item), num=(1, 0)))
        body.append(para(sentence(base + 5), num=(1, 1)))
        for item in range(2):
            body.append(para(sentence(base + 6 + item), num=(2, 0)))
        rows = []
        for row in range(4):
            cells = "".join(
                f"<w:tc><w:tcPr><w:tcW w:w=\"2000\" w:type=\"dxa\"/></w:tcPr>{para(f'Row {row} {SUBJECTS[(base + row + col) % len(SUBJECTS)]} {col}')}</w:tc>"
                for col in range(3)
            )
            rows.append(f"<w:tr>{cells}</w:tr>")
        body.append(f"<w:tbl><w:tblPr><w:tblW w:w=\"6000\" w:type=\"dxa\"/></w:tblPr><w:tblGrid>{'<w:gridCol w:w=\"2000\"/>' * 3}</w:tblGrid>{''.join(rows)}</w:tbl>")
        rid = f"rIdLink{section}"
        link_rels.append((rid, "hyperlink", f"https://example.com/section/{section + 1}"))
        note_id = section + 1
        footnotes.append(
            f'<w:footnote w:id="{note_id}"><w:p>{run("Footnote for section " + str(section + 1) + ": " + sentence(base + 9))}</w:p></w:footnote>'
        )
        body.append(
            "<w:p>"
            + run(sentence(base + 8) + " See ")
            + f'<w:hyperlink r:id="{rid}">{run("the section reference")}</w:hyperlink>'
            + f'<w:r><w:rPr><w:rStyle w:val="FootnoteReference"/><w:vertAlign w:val="superscript"/></w:rPr><w:footnoteReference w:id="{note_id}"/></w:r>'
            + "</w:p>"
        )
    document = f'{DECL}<w:document {W} {R}><w:body>{"".join(body)}<w:sectPr/></w:body></w:document>'
    styles = (
        f"{DECL}<w:styles {W}>"
        '<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style>'
        '<w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:basedOn w:val="Normal"/><w:pPr><w:outlineLvl w:val="0"/></w:pPr></w:style>'
        '<w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:basedOn w:val="Normal"/><w:pPr><w:outlineLvl w:val="1"/></w:pPr></w:style>'
        '<w:style w:type="character" w:styleId="FootnoteReference"><w:name w:val="footnote reference"/><w:rPr><w:vertAlign w:val="superscript"/></w:rPr></w:style>'
        "</w:styles>"
    )
    levels_bullet = "".join(
        f'<w:lvl w:ilvl="{level}"><w:start w:val="1"/><w:numFmt w:val="bullet"/><w:lvlText w:val="•"/></w:lvl>' for level in range(2)
    )
    levels_decimal = "".join(
        f'<w:lvl w:ilvl="{level}"><w:start w:val="1"/><w:numFmt w:val="decimal"/><w:lvlText w:val="%{level + 1}."/></w:lvl>' for level in range(2)
    )
    numbering = (
        f"{DECL}<w:numbering {W}>"
        f'<w:abstractNum w:abstractNumId="0">{levels_bullet}</w:abstractNum>'
        f'<w:abstractNum w:abstractNumId="1">{levels_decimal}</w:abstractNum>'
        '<w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num>'
        '<w:num w:numId="2"><w:abstractNumId w:val="1"/></w:num>'
        "</w:numbering>"
    )
    separators = '<w:footnote w:type="separator" w:id="-1"><w:p><w:r><w:separator/></w:r></w:p></w:footnote><w:footnote w:type="continuationSeparator" w:id="0"><w:p><w:r><w:continuationSeparator/></w:r></w:p></w:footnote>'
    footnotes_xml = f'{DECL}<w:footnotes {W}>{separators}{"".join(footnotes)}</w:footnotes>'
    wml = "application/vnd.openxmlformats-officedocument.wordprocessingml"
    return {
        "[Content_Types].xml": content_types(PACKAGE_DEFAULTS, {
            "/word/document.xml": f"{wml}.document.main+xml",
            "/word/styles.xml": f"{wml}.styles+xml",
            "/word/numbering.xml": f"{wml}.numbering+xml",
            "/word/footnotes.xml": f"{wml}.footnotes+xml",
        }),
        "_rels/.rels": relationships([("rId1", "officeDocument", "word/document.xml")]),
        "word/document.xml": document,
        "word/_rels/document.xml.rels": relationships(
            [("rIdStyles", "styles", "styles.xml"), ("rIdNumbering", "numbering", "numbering.xml"), ("rIdFootnotes", "footnotes", "footnotes.xml")]
            + link_rels,
            external={rid for rid, _, _ in link_rels},
        ),
        "word/styles.xml": styles,
        "word/numbering.xml": numbering,
        "word/footnotes.xml": footnotes_xml,
    }


def column_name(index: int) -> str:
    name = ""
    index += 1
    while index:
        index, remainder = divmod(index - 1, 26)
        name = chr(65 + remainder) + name
    return name


def xlsx(scale: int) -> dict[str, str]:
    strings: dict[str, int] = {}

    def shared(text: str) -> int:
        return strings.setdefault(text, len(strings))

    sheets = []
    for sheet in range(2):
        rows = []
        header = ["Region", "Owner", "Quantity", "Unit cost", "Notes"]
        cells = "".join(f'<c r="{column_name(col)}1" t="s"><v>{shared(text)}</v></c>' for col, text in enumerate(header))
        rows.append(f'<row r="1">{cells}</row>')
        for row in range(2, 2 + 10 * scale):
            seed = sheet * 1000 + row
            values = [
                ("s", shared(SUBJECTS[seed % len(SUBJECTS)].title())),
                ("s", shared(f"Owner {seed % 17}")),
                ("n", (seed * 37) % 500),
                ("n", f"{((seed * 13) % 1000) / 8:.3f}"),
                ("s", shared(sentence(seed))),
            ]
            cells = "".join(
                f'<c r="{column_name(col)}{row}"' + (' t="s"' if kind == "s" else "") + f"><v>{value}</v></c>"
                for col, (kind, value) in enumerate(values)
            )
            rows.append(f'<row r="{row}">{cells}</row>')
        sheets.append(
            f'{DECL}<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>{"".join(rows)}</sheetData></worksheet>'
        )
    sml = "application/vnd.openxmlformats-officedocument.spreadsheetml"
    string_items = "".join(f"<si><t>{escape(text)}</t></si>" for text in strings)
    parts = {
        "[Content_Types].xml": content_types(PACKAGE_DEFAULTS, {
            "/xl/workbook.xml": f"{sml}.sheet.main+xml",
            "/xl/sharedStrings.xml": f"{sml}.sharedStrings+xml",
            "/xl/styles.xml": f"{sml}.styles+xml",
            **{f"/xl/worksheets/sheet{i + 1}.xml": f"{sml}.worksheet+xml" for i in range(len(sheets))},
        }),
        "_rels/.rels": relationships([("rId1", "officeDocument", "xl/workbook.xml")]),
        "xl/workbook.xml": (
            f'{DECL}<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" {R}><sheets>'
            + "".join(f'<sheet name="Quarter {i + 1}" sheetId="{i + 1}" r:id="rIdSheet{i + 1}"/>' for i in range(len(sheets)))
            + "</sheets></workbook>"
        ),
        "xl/_rels/workbook.xml.rels": relationships(
            [(f"rIdSheet{i + 1}", "worksheet", f"worksheets/sheet{i + 1}.xml") for i in range(len(sheets))]
            + [("rIdStrings", "sharedStrings", "sharedStrings.xml"), ("rIdStyles", "styles", "styles.xml")]
        ),
        "xl/sharedStrings.xml": f'{DECL}<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="{len(strings)}" uniqueCount="{len(strings)}">{string_items}</sst>',
        "xl/styles.xml": (
            f'{DECL}<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">'
            '<fonts count="1"><font/></fonts><fills count="1"><fill/></fills><borders count="1"><border/></borders>'
            '<cellStyleXfs count="1"><xf/></cellStyleXfs><cellXfs count="1"><xf/></cellXfs></styleSheet>'
        ),
    }
    for index, sheet_xml in enumerate(sheets):
        parts[f"xl/worksheets/sheet{index + 1}.xml"] = sheet_xml
    return parts


def pptx(scale: int) -> dict[str, str]:
    def shape(shape_id: int, placeholder: str, paragraphs: str) -> str:
        return (
            f'<p:sp><p:nvSpPr><p:cNvPr id="{shape_id}" name="{placeholder} {shape_id}"/><p:cNvSpPr/>'
            f'<p:nvPr><p:ph type="{placeholder}"/></p:nvPr></p:nvSpPr><p:spPr/>'
            f"<p:txBody><a:bodyPr/>{paragraphs}</p:txBody></p:sp>"
        )

    def text_para(text: str, level: int = 0, link: str | None = None, bullet: bool = False) -> str:
        props = f'<a:pPr lvl="{level}"><a:buChar char="•"/></a:pPr>' if bullet else ""
        run_props = f'<a:rPr lang="en-US"><a:hlinkClick r:id="{link}"/></a:rPr>' if link else '<a:rPr lang="en-US"/>'
        return f"<a:p>{props}<a:r>{run_props}<a:t>{escape(text)}</a:t></a:r></a:p>"

    def tree(shapes: str) -> str:
        return (
            '<p:cSld><p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>'
            f"<p:grpSpPr/>{shapes}</p:spTree></p:cSld>"
        )

    slide_count = 3 * scale
    pml = "application/vnd.openxmlformats-officedocument.presentationml"
    parts: dict[str, str] = {}
    overrides = {
        "/ppt/presentation.xml": f"{pml}.presentation.main+xml",
        "/ppt/slideMasters/slideMaster1.xml": f"{pml}.slideMaster+xml",
        "/ppt/slideLayouts/slideLayout1.xml": f"{pml}.slideLayout+xml",
        "/ppt/theme/theme1.xml": "application/vnd.openxmlformats-officedocument.theme+xml",
    }
    for index in range(1, slide_count + 1):
        seed = index * 11
        bullets = "".join(text_para(sentence(seed + item), level=1 if item == 2 else 0, bullet=True) for item in range(4))
        bullets += text_para(f"Reference for slide {index}", link="rIdLink")
        parts[f"ppt/slides/slide{index}.xml"] = (
            f"{DECL}<p:sld {A} {P} {R}>"
            + tree(shape(2, "title", text_para(f"Slide {index}: {SUBJECTS[index % len(SUBJECTS)]} review")) + shape(3, "body", bullets))
            + "</p:sld>"
        )
        parts[f"ppt/slides/_rels/slide{index}.xml.rels"] = relationships(
            [("rIdLayout", "slideLayout", "../slideLayouts/slideLayout1.xml"),
             ("rIdNotes", "notesSlide", f"../notesSlides/notesSlide{index}.xml"),
             ("rIdLink", "hyperlink", f"https://example.com/slides/{index}")],
            external={"rIdLink"},
        )
        parts[f"ppt/notesSlides/notesSlide{index}.xml"] = (
            f"{DECL}<p:notes {A} {P} {R}>" + tree(shape(2, "body", text_para("Speaker notes: " + sentence(seed + 7)))) + "</p:notes>"
        )
        parts[f"ppt/notesSlides/_rels/notesSlide{index}.xml.rels"] = relationships([("rIdSlide", "slide", f"../slides/slide{index}.xml")])
        overrides[f"/ppt/slides/slide{index}.xml"] = f"{pml}.slide+xml"
        overrides[f"/ppt/notesSlides/notesSlide{index}.xml"] = f"{pml}.notesSlide+xml"
    parts["[Content_Types].xml"] = content_types(PACKAGE_DEFAULTS, overrides)
    parts["_rels/.rels"] = relationships([("rId1", "officeDocument", "ppt/presentation.xml")])
    parts["ppt/presentation.xml"] = (
        f'{DECL}<p:presentation {A} {P} {R}><p:sldMasterIdLst><p:sldMasterId id="2147483648" r:id="rIdMaster"/></p:sldMasterIdLst><p:sldIdLst>'
        + "".join(f'<p:sldId id="{255 + i}" r:id="rIdSlide{i}"/>' for i in range(1, slide_count + 1))
        + '</p:sldIdLst><p:sldSz cx="9144000" cy="6858000"/><p:notesSz cx="6858000" cy="9144000"/></p:presentation>'
    )
    parts["ppt/_rels/presentation.xml.rels"] = relationships(
        [("rIdMaster", "slideMaster", "slideMasters/slideMaster1.xml"), ("rIdTheme", "theme", "theme/theme1.xml")]
        + [(f"rIdSlide{i}", "slide", f"slides/slide{i}.xml") for i in range(1, slide_count + 1)]
    )
    parts["ppt/slideMasters/slideMaster1.xml"] = (
        f'{DECL}<p:sldMaster {A} {P} {R}>' + tree("") + '<p:sldLayoutIdLst><p:sldLayoutId id="2147483649" r:id="rIdLayout"/></p:sldLayoutIdLst></p:sldMaster>'
    )
    parts["ppt/slideMasters/_rels/slideMaster1.xml.rels"] = relationships(
        [("rIdLayout", "slideLayout", "../slideLayouts/slideLayout1.xml"), ("rIdTheme", "theme", "../theme/theme1.xml")]
    )
    parts["ppt/slideLayouts/slideLayout1.xml"] = f"{DECL}<p:sldLayout {A} {P} {R}>" + tree("") + "</p:sldLayout>"
    parts["ppt/slideLayouts/_rels/slideLayout1.xml.rels"] = relationships([("rIdMaster", "slideMaster", "../slideMasters/slideMaster1.xml")])
    parts["ppt/theme/theme1.xml"] = f'{DECL}<a:theme {A} name="Generated"><a:themeElements/></a:theme>'
    return parts


BUILDERS = {"docx": docx, "xlsx": xlsx, "pptx": pptx}


def write_generated_corpus(directory: Path) -> list[Path]:
    directory.mkdir(parents=True, exist_ok=True)
    paths = []
    for fmt, builder in BUILDERS.items():
        for size, scale in SIZES.items():
            path = directory / f"generated-{size}.{fmt}"
            write_package(path, builder(scale))
            paths.append(path)
    return paths
