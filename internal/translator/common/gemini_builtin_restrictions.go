package common

import "github.com/MoeForge/moego/ojson"

// RestrictGeminiBuiltinTools prevents built-in tools from bypassing explicit function-only choices.
func RestrictGeminiBuiltinTools(payload []byte, toolsPath, configPath string) []byte {
	doc := ojson.NewDoc(payload)
	RestrictGeminiBuiltinToolsDoc(doc, toolsPath, configPath)
	return doc.Bytes()
}

// RestrictGeminiBuiltinToolsDoc applies function-only restrictions in place.
func RestrictGeminiBuiltinToolsDoc(doc *ojson.Doc, toolsPath, configPath string) {
	if doc == nil {
		return
	}
	if doc.Get(configPath+".mode").String() == "NONE" {
		_ = doc.Delete(toolsPath)
		return
	}
	if len(doc.Get(configPath+".allowedFunctionNames").Array()) == 0 {
		return
	}
	tools := doc.Get(toolsPath)
	if !tools.IsArray() {
		return
	}
	var retained [][]byte
	for _, tool := range tools.Array() {
		if declarations := tool.Get("functionDeclarations"); declarations.IsArray() {
			node := ojson.NewDocString(`{}`)
			_ = node.SetRaw("functionDeclarations", declarations.Raw)
			retained = append(retained, node.Bytes())
		}
	}
	if len(retained) == 0 {
		_ = doc.Delete(toolsPath)
		return
	}
	_ = doc.SetRawBytes(toolsPath, JoinRawArray(retained))
}
