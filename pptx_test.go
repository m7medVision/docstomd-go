package docstomd

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConvertPptxConformance(t *testing.T) {
	decks, err := filepath.Glob(filepath.Join("testdata", "pptx", "*.pptx"))
	if err != nil || len(decks) == 0 {
		t.Fatalf("no pptx fixtures: %v", err)
	}
	for _, deck := range decks {
		name := strings.TrimSuffix(filepath.Base(deck), ".pptx")
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(deck)
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(strings.TrimSuffix(deck, ".pptx") + ".md")
			if err != nil {
				t.Fatal(err)
			}
			res, err := Convert(context.Background(), bytes.NewReader(data), Options{})
			if err != nil {
				t.Fatal(err)
			}
			if res.Format != FormatPptx {
				t.Errorf("format = %v, want pptx", res.Format)
			}
			if res.Markdown != string(want) {
				t.Errorf("markdown mismatch\n--- got\n%s--- want\n%s", res.Markdown, want)
			}
		})
	}
}

func TestConvertPptxDetectsFormat(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "pptx", "pres.pptx"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Convert(context.Background(), bytes.NewReader(data), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != FormatPptx || !strings.HasPrefix(res.Markdown, "Deck Title Slide\n") {
		t.Errorf("got format %v markdown %q", res.Format, res.Markdown)
	}
}

const (
	pptxNS = `xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" ` +
		`xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"`
	relsHead = `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`
	relType  = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/"
)

func buildDeck(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	files := map[string]string{
		"_rels/.rels":                     relsHead + `<Relationship Id="rId1" Type="` + relType + `officeDocument" Target="ppt/presentation.xml"/></Relationships>`,
		"ppt/presentation.xml":            `<p:presentation ` + pptxNS + `><p:sldIdLst><p:sldId id="256" r:id="rId1"/></p:sldIdLst></p:presentation>`,
		"ppt/_rels/presentation.xml.rels": relsHead + `<Relationship Id="rId1" Type="` + relType + `slide" Target="slides/slide1.xml"/></Relationships>`,
	}
	for name, body := range parts {
		files[name] = body
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func shape(ph, paragraphs string) string {
	return `<p:sp><p:nvSpPr><p:cNvPr id="2" name=""/><p:cNvSpPr/><p:nvPr>` + ph + `</p:nvPr></p:nvSpPr><p:spPr/>` +
		`<p:txBody><a:bodyPr/>` + paragraphs + `</p:txBody></p:sp>`
}

func slideXML(root string, shapes ...string) string {
	return `<p:` + root + ` ` + pptxNS + `><p:cSld><p:spTree>` + strings.Join(shapes, "") + `</p:spTree></p:cSld></p:` + root + `>`
}

func convertDeck(t *testing.T, parts map[string]string) string {
	t.Helper()
	res, err := Convert(context.Background(), bytes.NewReader(buildDeck(t, parts)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	return res.Markdown
}

func TestConvertPptxExcludesPlaceholderFurniture(t *testing.T) {
	got := convertDeck(t, map[string]string{
		"ppt/slides/slide1.xml": slideXML("sld",
			shape(`<p:ph type="title"/>`, `<a:p><a:r><a:t>Agenda</a:t></a:r></a:p>`),
			shape(`<p:ph type="dt" idx="10"/>`, `<a:p><a:r><a:t>2026-09-14</a:t></a:r></a:p>`),
			shape(`<p:ph type="ftr" idx="11"/>`, `<a:p><a:r><a:t>Confidential</a:t></a:r></a:p>`),
			shape(`<p:ph type="sldNum" idx="12"/>`, `<a:p><a:fld id="{1}" type="slidenum"><a:t>1</a:t></a:fld></a:p>`),
			shape(`<p:ph idx="1"/>`, `<a:p><a:r><a:t>Body text</a:t></a:r></a:p>`),
		),
		"ppt/slides/_rels/slide1.xml.rels": relsHead +
			`<Relationship Id="rId1" Type="` + relType + `slideLayout" Target="../slideLayouts/slideLayout1.xml"/>` +
			`<Relationship Id="rId2" Type="` + relType + `notesSlide" Target="../notesSlides/notesSlide1.xml"/></Relationships>`,
		"ppt/slideLayouts/slideLayout1.xml": slideXML("sldLayout",
			shape(`<p:ph type="title"/>`, `<a:p><a:r><a:t>Click to edit title</a:t></a:r></a:p>`),
			shape(`<p:ph idx="1"/>`, `<a:p><a:r><a:t>Layout prompt text</a:t></a:r></a:p>`),
		),
		"ppt/slideLayouts/_rels/slideLayout1.xml.rels": relsHead +
			`<Relationship Id="rId1" Type="` + relType + `slideMaster" Target="../slideMasters/slideMaster1.xml"/></Relationships>`,
		"ppt/slideMasters/slideMaster1.xml": slideXML("sldMaster",
			shape(`<p:ph type="ftr"/>`, `<a:p><a:r><a:t>Master footer</a:t></a:r></a:p>`),
		),
		"ppt/notesSlides/notesSlide1.xml": slideXML("notes",
			shape(`<p:ph type="sldImg"/>`, `<a:p><a:r><a:t>Slide image</a:t></a:r></a:p>`),
			shape(`<p:ph type="body" idx="1"/>`, `<a:p><a:r><a:t>Say hello</a:t></a:r></a:p>`),
			shape(`<p:ph type="sldNum" idx="5"/>`, `<a:p><a:r><a:t>1</a:t></a:r></a:p>`),
		),
	})
	want := "## Agenda\n\nBody text\n\n> Say hello\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestConvertPptxCascade(t *testing.T) {
	got := convertDeck(t, map[string]string{
		"ppt/presentation.xml": `<p:presentation ` + pptxNS + `><p:sldIdLst><p:sldId id="256" r:id="rId1"/></p:sldIdLst>` +
			`<p:defaultTextStyle><a:lvl1pPr><a:defRPr strike="sngStrike"/></a:lvl1pPr></p:defaultTextStyle></p:presentation>`,
		"ppt/slides/slide1.xml": slideXML("sld",
			shape(`<p:ph type="title"/>`, `<a:p><a:r><a:rPr b="1"/><a:t>Bold</a:t></a:r><a:r><a:t> title</a:t></a:r></a:p>`),
			shape(`<p:ph type="body" idx="7"/>`,
				`<a:lstStyle><a:lvl1pPr><a:defRPr i="0"/></a:lvl1pPr></a:lstStyle>`+
					`<a:p><a:r><a:t>shape turns italic off</a:t></a:r></a:p>`+
					`<a:p><a:pPr><a:defRPr strike="noStrike"/></a:pPr><a:r><a:rPr i="1"/><a:t>run turns it on</a:t></a:r></a:p>`),
			`<p:sp><p:nvSpPr><p:cNvPr id="9" name=""/><p:cNvSpPr txBox="1"/><p:nvPr/></p:nvSpPr><p:spPr/><p:txBody><a:bodyPr/>`+
				`<a:p><a:r><a:t>other style</a:t></a:r></a:p></p:txBody></p:sp>`,
		),
		"ppt/slides/_rels/slide1.xml.rels": relsHead +
			`<Relationship Id="rId1" Type="` + relType + `slideLayout" Target="../slideLayouts/slideLayout1.xml"/></Relationships>`,
		"ppt/slideLayouts/slideLayout1.xml": slideXML("sldLayout",
			shape(`<p:ph type="body" idx="7"/>`, `<a:lstStyle><a:lvl1pPr><a:buNone/><a:defRPr b="1"/></a:lvl1pPr></a:lstStyle>`),
		),
		"ppt/slideLayouts/_rels/slideLayout1.xml.rels": relsHead +
			`<Relationship Id="rId1" Type="` + relType + `slideMaster" Target="../slideMasters/slideMaster1.xml"/></Relationships>`,
		"ppt/slideMasters/slideMaster1.xml": `<p:sldMaster ` + pptxNS + `><p:cSld><p:spTree/></p:cSld><p:txStyles>` +
			`<p:titleStyle><a:lvl1pPr><a:defRPr b="1" strike="noStrike"/></a:lvl1pPr></p:titleStyle>` +
			`<p:bodyStyle><a:lvl1pPr><a:buChar char="•"/><a:defRPr i="1"/></a:lvl1pPr></p:bodyStyle>` +
			`<p:otherStyle><a:lvl1pPr><a:defRPr i="1"/></a:lvl1pPr></p:otherStyle></p:txStyles></p:sldMaster>`,
	})
	want := "## Bold title\n\n~~**shape turns italic off**~~\n\n***run turns it on***\n\n~~*other style*~~\n"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestConvertPptxErrors(t *testing.T) {
	noSlides := buildDeck(t, map[string]string{
		"ppt/presentation.xml": `<p:presentation ` + pptxNS + `/>`,
	})
	_, err := Convert(context.Background(), bytes.NewReader(noSlides), Options{Format: FormatPptx})
	if got := ErrorCodeOf(err); got != CodeMalformed {
		t.Errorf("no slide list: code %q (%v), want malformed", got, err)
	}
	unreadable := buildDeck(t, map[string]string{"ppt/slides/slide1.xml": `<p:notASlide ` + pptxNS + `/>`})
	_, err = Convert(context.Background(), bytes.NewReader(unreadable), Options{Format: FormatPptx})
	if got := ErrorCodeOf(err); got != CodeMalformed {
		t.Errorf("no readable slide: code %q (%v), want malformed", got, err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if _, err := zw.Create("docProps/app.xml"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Convert(context.Background(), bytes.NewReader(buf.Bytes()), Options{Format: FormatPptx})
	if got := ErrorCodeOf(err); got != CodeMissingPart {
		t.Errorf("no presentation part: code %q (%v), want missingPart", got, err)
	}
}

func TestConvertPptxFrames(t *testing.T) {
	cell := func(attrs, text string) string {
		return `<a:tc` + attrs + `><a:txBody><a:bodyPr/><a:p><a:r><a:t>` + text + `</a:t></a:r></a:p></a:txBody></a:tc>`
	}
	frame := func(data string) string {
		return `<p:graphicFrame><p:nvGraphicFramePr><p:cNvPr id="4" name=""/><p:cNvGraphicFramePr/><p:nvPr/></p:nvGraphicFramePr>` +
			`<a:graphic><a:graphicData>` + data + `</a:graphicData></a:graphic></p:graphicFrame>`
	}
	got := convertDeck(t, map[string]string{
		"ppt/slides/slide1.xml": slideXML("sld",
			shape("", `<a:p><a:pPr><a:buAutoNum type="alphaLcParenR" startAt="2"/></a:pPr><a:r><a:t>second</a:t></a:r></a:p>`+
				`<a:p><a:pPr><a:buAutoNum type="alphaLcParenR" startAt="2"/></a:pPr><a:r><a:t>third</a:t></a:r></a:p>`+
				`<a:p><a:pPr><a:buAutoNum type="arabicPeriod"/></a:pPr><a:r><a:t>one</a:t></a:r></a:p>`),
			frame(`<a:tbl><a:tblPr firstRow="1"/><a:tr>`+cell(` gridSpan="2"`, "Span")+cell(` hMerge="1"`, "")+`</a:tr>`+
				`<a:tr>`+cell("", "a")+cell("", "b")+`</a:tr></a:tbl>`),
			frame(`<c:chart xmlns:c="http://schemas.openxmlformats.org/drawingml/2006/chart" r:id="rId3"/>`),
			frame(`<dgm:relIds xmlns:dgm="http://schemas.openxmlformats.org/drawingml/2006/diagram" r:dm="rId4"/>`),
			`<p:pic><p:nvPicPr><p:cNvPr id="7" name="" descr="A logo"/></p:nvPicPr><p:blipFill><a:blip r:embed="rId5"/></p:blipFill></p:pic>`,
			`<p:pic><p:nvPicPr><p:cNvPr id="8" name="" descr="Remote"/></p:nvPicPr><p:blipFill><a:blip r:link="rId6"/></p:blipFill></p:pic>`,
		),
		"ppt/slides/_rels/slide1.xml.rels": relsHead +
			`<Relationship Id="rId3" Type="` + relType + `chart" Target="../charts/chart1.xml"/>` +
			`<Relationship Id="rId4" Type="` + relType + `diagramData" Target="../diagrams/data1.xml"/>` +
			`<Relationship Id="rId5" Type="` + relType + `image" Target="../media/image1.png"/>` +
			`<Relationship Id="rId6" Type="` + relType + `image" Target="https://example.com/r.png" TargetMode="External"/></Relationships>`,
		"ppt/charts/chart1.xml": `<c:chartSpace xmlns:c="http://schemas.openxmlformats.org/drawingml/2006/chart" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><c:chart>` +
			`<c:title><c:tx><c:rich><a:p><a:r><a:t>Sales</a:t></a:r></a:p></c:rich></c:tx></c:title><c:plotArea><c:barChart><c:ser>` +
			`<c:tx><c:strRef><c:f>Sheet1!$B$1</c:f><c:strCache><c:pt idx="0"><c:v>2026</c:v></c:pt></c:strCache></c:strRef></c:tx>` +
			`<c:cat><c:strRef><c:strCache><c:pt idx="0"><c:v>Q1</c:v></c:pt><c:pt idx="1"><c:v>Q2</c:v></c:pt></c:strCache></c:strRef></c:cat>` +
			`<c:val><c:numRef><c:numCache><c:pt idx="0"><c:v>5</c:v></c:pt></c:numCache></c:numRef></c:val>` +
			`</c:ser></c:barChart></c:plotArea></c:chart></c:chartSpace>`,
		"ppt/diagrams/data1.xml": `<dgm:dataModel xmlns:dgm="http://schemas.openxmlformats.org/drawingml/2006/diagram"><dgm:ptLst>` +
			`<dgm:pt><dgm:t>Plan</dgm:t></dgm:pt><dgm:pt/><dgm:pt><dgm:t>Ship</dgm:t></dgm:pt></dgm:ptLst></dgm:dataModel>`,
		"ppt/media/image1.png": "png",
	})
	want := "- b) second\n- c) third\n\n4. one\n\n" +
		"| Span |  |\n| --- | --- |\n| a | b |\n\n" +
		"**Sales**\n\n|  | 2026 |\n| --- | --- |\n| Q1 | 5 |\n| Q2 |  |\n\n" +
		"- Plan\n- Ship\n\nA logo\n\n![Remote](https://example.com/r.png)\n"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}
