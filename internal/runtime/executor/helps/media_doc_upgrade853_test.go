package helps

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestV853MediaFinalizerRetainsOriginalPresenceAndPreciseDoc(t *testing.T) {
	var original bytes.Buffer
	writer := multipart.NewWriter(&original)
	if errWrite := writer.WriteField("size", "caller-size"); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	models := []config.PayloadModelRule{{Name: "image-model"}}
	cfg := &config.Config{Payload: config.PayloadConfig{
		Default:  []config.PayloadRule{{Models: models, Params: map[string]any{"size": "default-size"}}},
		Override: []config.PayloadRule{{Models: models, Params: map[string]any{"max_output_tokens": 17}}},
		Filter:   []config.PayloadFilterRule{{Models: models, Params: []string{"metadata.drop"}}},
	}}
	req := cliproxyexecutor.Request{Model: "image-model", Payload: original.Bytes()}
	opts := cliproxyexecutor.Options{Headers: http.Header{"Content-Type": {writer.FormDataContentType()}}}
	finalize, errFinalize := NewMediaPayloadFinalizer(cfg, "codex", "image-model", "codex", req, opts)
	if errFinalize != nil {
		t.Fatal(errFinalize)
	}
	doc := ojson.NewDocString(`{"size":"translated-size","precision":9007199254740993,"max_output_tokens":3,"metadata":{}}`)
	if errSet := doc.Set("metadata.drop", "built-in"); errSet != nil {
		t.Fatal(errSet)
	}
	finalize.FinalizeDoc(doc)
	out := doc.Bytes()
	if ojson.GetBytes(out, "size").String() != "translated-size" || ojson.GetBytes(out, "max_output_tokens").Int() != 17 || ojson.GetBytes(out, "metadata.drop").Exists() || ojson.GetBytes(out, "precision").Raw != "9007199254740993" {
		t.Fatalf("media finalization lost original presence, precision, or final rules: %s", out)
	}
}
