package detect

import (
	"github.com/m7medVision/docstomd-go/internal/pdf/parse"
)

type pageAnalysis struct {
	textOperatorCount       int
	hasImages               bool
	hasTemplateImage        bool
	imageCount              int
	uniqueTextChars         int
	uniqueAlphanumChars     int
	hasVectorText           bool
	hasIdentityHNoToUnicode bool
	hasOnlyType3Fonts       bool
	fontChangeCount         int
	hasDecodableTextFonts   bool
}

func (a pageAnalysis) looksLikeScan() bool {
	return a.imageCount <= 1 && a.textOperatorCount < 50 &&
		a.uniqueAlphanumChars < 10 &&
		!(a.hasDecodableTextFonts && a.textOperatorCount >= 10)
}

func analyzePageContent(doc *parse.Document, pageRef parse.Ref) pageAnalysis {
	textOps := 0
	hasImages := false
	imageCount := 0
	pathOps := 0
	fontChanges := 0
	uniqueChars := map[byte]bool{}
	usedFontIDs := map[parse.Ref]bool{}
	fontMap := map[parse.Ref]*fontInfo{}

	resourceScopes := doc.PageResources(pageRef)

	for _, contentRef := range doc.PageContents(pageRef) {
		obj, err := doc.GetObject(contentRef.Num)
		if err != nil {
			continue
		}
		stm, ok := obj.(*parse.Stream)
		if !ok {
			continue
		}
		content, _ := doc.StreamData(stm)
		fontNames := map[string]bool{}
		ops, paths, fonts := scanContent(content, uniqueChars, fontNames)
		textOps += ops
		pathOps += paths
		fontChanges += fonts
		resolveFontNamesScoped(doc, resourceScopes, fontNames, usedFontIDs)
	}

	visited := map[parse.Ref]bool{}
	for _, resources := range resourceScopes {
		collectFontsFromResources(doc, resources, fontMap)
		ops, imgs, paths, fonts := scanXObjects(doc, resources, visited, uniqueChars, usedFontIDs, fontMap)
		textOps += ops
		imageCount += imgs
		pathOps += paths
		fontChanges += fonts
		hasImages = hasImages || imgs > 0
	}

	foundImages, _, hasTemplateImage := analyzePageImages(doc, pageRef)
	if foundImages {
		hasImages = true
	}

	alphanum := 0
	for b := range uniqueChars {
		if (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') {
			alphanum++
		}
	}

	hasVectorText := pathOps >= 1000 && pathOps > textOps*200 && alphanum < 30
	hasIdentityH := textOps > 0 && usedFontsHaveIdentityHNoToUnicode(doc, usedFontIDs, fontMap)
	hasOnlyType3 := textOps > 0 && usedFontsAreOnlyType3(usedFontIDs, fontMap)
	hasDecodable := textOps > 0 && usedFontsHaveDecodableText(doc, usedFontIDs, fontMap)

	return pageAnalysis{
		textOperatorCount:       textOps,
		hasImages:               hasImages,
		hasTemplateImage:        hasTemplateImage,
		imageCount:              imageCount,
		uniqueTextChars:         len(uniqueChars),
		uniqueAlphanumChars:     alphanum,
		hasVectorText:           hasVectorText,
		hasIdentityHNoToUnicode: hasIdentityH,
		hasOnlyType3Fonts:       hasOnlyType3,
		fontChangeCount:         fontChanges,
		hasDecodableTextFonts:   hasDecodable,
	}
}

func scanContent(content []byte, uniqueChars map[byte]bool, usedFontNames map[string]bool) (int, int, int) {
	textOps := 0
	pathOps := 0
	fontChanges := 0

	isWordStart := func(pos int) bool {
		return pos == 0 || isPDFSpace(content[pos-1])
	}
	isWordEnd := func(pos int) bool {
		return pos+1 >= len(content) || isPDFSpace(content[pos+1])
	}

	operandFloor := 0
	for i := 0; i < len(content); i++ {
		b := content[i]
		if b == 'T' && i+1 < len(content) {
			next := content[i+1]
			if next == 'j' || next == 'J' {
				if (i+2 >= len(content) || isPDFSpace(content[i+2])) &&
					precedingOperandCloser(content, i, operandFloor) {
					textOps++
					collectTextCharsBefore(content, i, uniqueChars, operandFloor)
					operandFloor = i
				}
			} else if next == 'f' {
				if i+2 >= len(content) ||
					isPDFSpace(content[i+2]) ||
					content[i+2] == '[' ||
					content[i+2] == '(' ||
					content[i+2] == '<' ||
					content[i+2] == '/' {
					if name, ok := extractFontNameBeforeTf(content, i, operandFloor); ok {
						usedFontNames[name] = true
						fontChanges++
						operandFloor = i
					}
				}
			}
		}

		switch b {
		case 'm', 'l', 'c', 'h', 'f', 'S', 's', 'B', 'F':
			if isWordStart(i) && isWordEnd(i) {
				pathOps++
			}
		case 'r':
			if i+1 < len(content) && content[i+1] == 'e' && isWordStart(i) &&
				(i+2 >= len(content) || isPDFSpace(content[i+2])) {
				pathOps++
			}
		}
		if b == 'f' && i+1 < len(content) && content[i+1] == '*' && isWordStart(i) &&
			(i+2 >= len(content) || isPDFSpace(content[i+2])) {
			pathOps++
		}
	}

	return textOps, pathOps, fontChanges
}

func isPDFSpace(c byte) bool {
	return c == 0x09 || c == 0x0A || c == 0x0C || c == 0x0D || c == 0x20
}

func precedingOperandCloser(content []byte, opPos, floor int) bool {
	for j := opPos; j > floor; {
		j--
		if !isPDFSpace(content[j]) {
			return content[j] == ')' || content[j] == '>' || content[j] == ']'
		}
	}
	return false
}

func extractFontNameBeforeTf(content []byte, tfPos, floor int) (string, bool) {
	j := tfPos
	for j > floor && isPDFSpace(content[j-1]) {
		j--
	}
	for j > floor && (isDigit(content[j-1]) || content[j-1] == '.' || content[j-1] == '-') {
		j--
	}
	for j > floor && isPDFSpace(content[j-1]) {
		j--
	}
	nameEnd := j
	for j > floor && content[j-1] != '/' {
		if isPDFSpace(content[j-1]) || content[j-1] == '(' || content[j-1] == ')' {
			return "", false
		}
		j--
	}
	if j <= floor || content[j-1] != '/' {
		return "", false
	}
	if j < nameEnd {
		return string(content[j:nameEnd]), true
	}
	return "", false
}

func collectTextCharsBefore(content []byte, opPos int, uniqueChars map[byte]bool, floor int) {
	j := opPos
	for j > floor {
		j--
		if !isPDFSpace(content[j]) {
			break
		}
	}
	if j == floor {
		return
	}
	closing := content[j]
	switch closing {
	case ')':
		depth := 1
		k := j
		for k > floor && depth > 0 {
			k--
			switch content[k] {
			case ')':
				if k == 0 || content[k-1] != '\\' {
					depth++
				}
			case '(':
				if k == 0 || content[k-1] != '\\' {
					depth--
				}
			}
		}
		if depth == 0 && k+1 < j {
			for _, ch := range content[k+1 : j] {
				if !isPDFSpace(ch) {
					uniqueChars[ch] = true
				}
			}
		}
	case '>':
		k := j
		for k > floor {
			k--
			if content[k] == '<' {
				break
			}
		}
		if content[k] == '<' && k+1 < j {
			collectHexChars(content[k+1:j], uniqueChars)
		}
	case ']':
		k := j
		for k > floor {
			k--
			if content[k] == '[' {
				break
			}
		}
		if content[k] == '[' {
			m := k + 1
			for m < j {
				if content[m] == '(' {
					start := m + 1
					depth := 1
					m++
					for m < j && depth > 0 {
						switch content[m] {
						case ')':
							if content[m-1] != '\\' {
								depth--
							}
						case '(':
							if content[m-1] != '\\' {
								depth++
							}
						}
						if depth > 0 {
							m++
						}
					}
					for _, ch := range content[start:m] {
						if !isPDFSpace(ch) {
							uniqueChars[ch] = true
						}
					}
				} else if content[m] == '<' {
					hexStart := m + 1
					m++
					for m < j && content[m] != '>' {
						m++
					}
					collectHexChars(content[hexStart:m], uniqueChars)
				}
				m++
			}
		}
	}
}

func collectHexChars(hexBytes []byte, uniqueChars map[byte]bool) {
	var clean []byte
	for _, b := range hexBytes {
		if !isPDFSpace(b) {
			clean = append(clean, b)
		}
	}
	for i := 0; i+1 < len(clean); i += 2 {
		hi := hexVal(clean[i])
		lo := hexVal(clean[i+1])
		if hi >= 0 && lo >= 0 {
			b := byte(hi<<4 | lo)
			if b != 0 && b != ' ' && b != '\t' && b != '\n' {
				uniqueChars[b] = true
			}
		}
	}
}

func hexVal(b byte) int {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0')
	case b >= 'a' && b <= 'f':
		return int(b-'a') + 10
	case b >= 'A' && b <= 'F':
		return int(b-'A') + 10
	}
	return -1
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

const templateImageThreshold = 500_000

func analyzePageImages(doc *parse.Document, pageRef parse.Ref) (bool, uint64, bool) {
	hasImages := false
	var totalArea uint64
	hasTemplateImage := false
	visited := map[parse.Ref]bool{}

	for _, resources := range doc.OwnPageResources(pageRef) {
		collectImagesFromResources(doc, resources, &hasImages, &totalArea, &hasTemplateImage, templateImageThreshold, visited)
		if patternObj, ok := resources["Pattern"]; ok {
			if patternDict, ok := dictOf(doc, patternObj); ok {
				for _, value := range patternDict {
					patRef, ok := value.(parse.Ref)
					if !ok {
						continue
					}
					if visited[patRef] {
						continue
					}
					visited[patRef] = true
					patObj, err := doc.GetObject(patRef.Num)
					if err != nil {
						continue
					}
					if stm, ok := patObj.(*parse.Stream); ok {
						if resObj, ok2 := stm.Dict["Resources"]; ok2 {
							if res, ok3 := dictOf(doc, resObj); ok3 {
								collectImagesFromResources(doc, res, &hasImages, &totalArea, &hasTemplateImage, templateImageThreshold, visited)
							}
						}
					}
				}
			}
		}
	}

	if !hasTemplateImage && totalArea >= templateImageThreshold*4 {
		hasTemplateImage = true
	}
	return hasImages, totalArea, hasTemplateImage
}

func dictOf(doc *parse.Document, obj any) (map[string]any, bool) {
	switch v := obj.(type) {
	case map[string]any:
		return v, true
	case parse.Ref:
		resolved, err := doc.GetObject(v.Num)
		if err != nil {
			return nil, false
		}
		if d, ok := resolved.(map[string]any); ok {
			return d, true
		}
	}
	return nil, false
}

func collectImagesFromResources(doc *parse.Document, resources map[string]any, hasImages *bool, totalArea *uint64, hasTemplateImage *bool, threshold uint64, visited map[parse.Ref]bool) {
	xobjDict, ok := dictOf(doc, resources["XObject"])
	if !ok {
		return
	}
	for _, value := range xobjDict {
		xobjRef, ok := value.(parse.Ref)
		if !ok {
			continue
		}
		if visited[xobjRef] {
			continue
		}
		visited[xobjRef] = true
		obj, err := doc.GetObject(xobjRef.Num)
		if err != nil {
			continue
		}
		stm, ok := obj.(*parse.Stream)
		if !ok {
			continue
		}
		subtype, _ := stm.Dict["Subtype"].(parse.Name)
		switch subtype {
		case "Image":
			*hasImages = true
			width, _ := stm.Dict["Width"].(int64)
			height, _ := stm.Dict["Height"].(int64)
			area := uint64(width) * uint64(height)
			*totalArea += area
			if area >= threshold {
				*hasTemplateImage = true
			}
		case "Form":
			if resObj, ok := stm.Dict["Resources"]; ok {
				if res, ok := dictOf(doc, resObj); ok {
					collectImagesFromResources(doc, res, hasImages, totalArea, hasTemplateImage, threshold, visited)
				}
			}
		}
	}
}

func scanXObjects(doc *parse.Document, resources map[string]any, visited map[parse.Ref]bool, uniqueChars map[byte]bool, usedFontIDs map[parse.Ref]bool, fontMap map[parse.Ref]*fontInfo) (int, int, int, int) {
	textOps := 0
	imageCount := 0
	pathOps := 0
	fontChanges := 0

	xobjDict, ok := dictOf(doc, resources["XObject"])
	if !ok {
		return 0, 0, 0, 0
	}
	for _, value := range xobjDict {
		objRef, ok := value.(parse.Ref)
		if !ok {
			continue
		}
		if visited[objRef] {
			continue
		}
		visited[objRef] = true
		obj, err := doc.GetObject(objRef.Num)
		if err != nil {
			continue
		}
		stm, ok := obj.(*parse.Stream)
		if !ok {
			continue
		}
		subtype, _ := stm.Dict["Subtype"].(parse.Name)
		switch subtype {
		case "Form":
			content, _ := doc.StreamData(stm)
			fontNames := map[string]bool{}
			ops, paths, fonts := scanContent(content, uniqueChars, fontNames)
			textOps += ops
			pathOps += paths
			fontChanges += fonts
			if res, ok := dictOf(doc, stm.Dict["Resources"]); ok {
				resolveFontNamesUnscoped(doc, res, fontNames, usedFontIDs)
				collectFontsFromResources(doc, res, fontMap)
				ops2, imgs2, paths2, fonts2 := scanXObjects(doc, res, visited, uniqueChars, usedFontIDs, fontMap)
				textOps += ops2
				imageCount += imgs2
				pathOps += paths2
				fontChanges += fonts2
			}
		case "Image":
			imageCount++
		}
	}
	return textOps, imageCount, pathOps, fontChanges
}
