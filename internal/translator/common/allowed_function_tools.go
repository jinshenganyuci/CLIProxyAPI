package common

import (
	"strings"

	"github.com/tidwall/gjson"
)

// OpenAIAllowedFunctionTools reads a function allow-list without broadening empty or invalid restrictions.
func OpenAIAllowedFunctionTools(choice gjson.Result) (names map[string]struct{}, mode string, active bool) {
	if !choice.IsObject() || choice.Get("type").String() != "allowed_tools" {
		return nil, "", false
	}
	names = make(map[string]struct{})
	mode = "auto"
	container := choice
	if nested := choice.Get("allowed_tools"); nested.Exists() {
		if !nested.IsObject() {
			return names, "none", true
		}
		container = nested
	}
	modeValue := container.Get("mode")
	if !modeValue.Exists() {
		modeValue = choice.Get("mode")
	}
	if modeValue.Exists() {
		if modeValue.Type != gjson.String {
			return names, "none", true
		}
		mode = strings.ToLower(strings.TrimSpace(modeValue.String()))
		if mode != "auto" && mode != "required" && mode != "any" {
			return names, "none", true
		}
	}
	list := container.Get("tools")
	if !list.IsArray() {
		return names, "none", true
	}
	for _, tool := range list.Array() {
		if kind := tool.Get("type").String(); kind != "" && kind != "function" {
			continue
		}
		name := tool.Get("function.name")
		if !name.Exists() {
			name = tool.Get("name")
		}
		if name.Type == gjson.String && strings.TrimSpace(name.String()) != "" {
			names[strings.TrimSpace(name.String())] = struct{}{}
		}
	}
	return names, mode, true
}
