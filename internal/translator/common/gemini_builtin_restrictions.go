package common

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// RestrictGeminiBuiltinTools prevents built-in tools from bypassing explicit function-only choices.
func RestrictGeminiBuiltinTools(payload []byte, toolsPath, configPath string) []byte {
	if gjson.GetBytes(payload, configPath+".mode").String() == "NONE" {
		out, err := sjson.DeleteBytes(payload, toolsPath)
		if err == nil {
			return out
		}
		return payload
	}
	if len(gjson.GetBytes(payload, configPath+".allowedFunctionNames").Array()) == 0 {
		return payload
	}
	tools := gjson.GetBytes(payload, toolsPath)
	if !tools.IsArray() {
		return payload
	}
	var retained [][]byte
	for _, tool := range tools.Array() {
		if declarations := tool.Get("functionDeclarations"); declarations.IsArray() {
			node, _ := sjson.SetRawBytes([]byte(`{}`), "functionDeclarations", []byte(declarations.Raw))
			retained = append(retained, node)
		}
	}
	if len(retained) == 0 {
		out, _ := sjson.DeleteBytes(payload, toolsPath)
		return out
	}
	out, _ := sjson.SetRawBytes(payload, toolsPath, JoinRawArray(retained))
	return out
}
