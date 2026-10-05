package chat_completions

import "github.com/tidwall/gjson"

type codexCitationPartKey struct {
	itemID       string
	outputIndex  int64
	contentIndex int64
}

type codexCitationPart struct {
	runeOffset int64
	seen       map[string]struct{}
}

// Citation ranges are relative to their content block, regardless of whether
// annotations arrive before text.done or with the final message item.
func codexCitationPartForEvent(p *ConvertCliToOpenAIParams, event gjson.Result, contentIndex int64) *codexCitationPart {
	itemID := event.Get("item_id").String()
	if itemID == "" {
		itemID = event.Get("item.id").String()
	}
	keys := make([]codexCitationPartKey, 0, 2)
	if itemID != "" {
		keys = append(keys, codexCitationPartKey{itemID: itemID, outputIndex: -1, contentIndex: contentIndex})
	}
	if outputIndex := event.Get("output_index"); outputIndex.Exists() {
		keys = append(keys, codexCitationPartKey{outputIndex: outputIndex.Int(), contentIndex: contentIndex})
	}
	if len(keys) == 0 {
		return nil
	}
	if p.citationParts == nil {
		p.citationParts = make(map[codexCitationPartKey]*codexCitationPart)
	}
	var part *codexCitationPart
	for _, key := range keys {
		if part = p.citationParts[key]; part != nil {
			break
		}
	}
	if part == nil {
		part = &codexCitationPart{runeOffset: p.emittedTextRunes, seen: make(map[string]struct{})}
	}
	for _, key := range keys {
		p.citationParts[key] = part
	}
	return part
}

func codexStreamURLCitations(p *ConvertCliToOpenAIParams, event gjson.Result) [][]byte {
	if content := event.Get("item.content"); content.IsArray() {
		var citations [][]byte
		var itemAnnotations []gjson.Result
		for _, path := range []string{"annotation", "annotations", "part.annotations", "item.annotations"} {
			itemAnnotations = append(itemAnnotations, codexAnnotationResults(event.Get(path))...)
		}
		if len(itemAnnotations) > 0 {
			if part := codexCitationPartForEvent(p, event, 0); part != nil {
				citations = append(citations, buildCodexURLCitations(itemAnnotations, part.runeOffset, part.seen)...)
			} else {
				citations = append(citations, buildCodexURLCitations(itemAnnotations, p.emittedTextRunes, p.citationKeys)...)
			}
		}
		for index, block := range content.Array() {
			if block.Get("type").String() != "output_text" {
				continue
			}
			if part := codexCitationPartForEvent(p, event, int64(index)); part != nil {
				citations = append(citations, buildCodexURLCitations(codexAnnotationResults(block.Get("annotations")), part.runeOffset, part.seen)...)
			} else {
				citations = append(citations, buildCodexURLCitations(codexAnnotationResults(block.Get("annotations")), p.emittedTextRunes, p.citationKeys)...)
			}
		}
		return citations
	}
	if index := event.Get("content_index"); index.Exists() {
		if part := codexCitationPartForEvent(p, event, index.Int()); part != nil {
			return buildCodexURLCitations(codexAnnotationsFromEvent(event), part.runeOffset, part.seen)
		}
	}
	// Preserve the legacy fallback for underspecified events without item or
	// content coordinates; production Responses events carry those coordinates.
	return buildCodexURLCitations(codexAnnotationsFromEvent(event), p.emittedTextRunes, p.citationKeys)
}
