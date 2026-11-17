package common

import (
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
)

func TestRetainedJSONValueKeepsNumericTokensUpgrade875(t *testing.T) {
	const want = `{"fraction":0.123456789012345678901,"integer":9007199254740993,"nested":[1e+30,false,null]}`
	source := []byte(want)
	retained := RetainedJSONValue(ojson.ParseBytes(source))
	for index := range source {
		source[index] = 'x'
	}
	encoded, errMarshal := json.Marshal(retained)
	if errMarshal != nil || string(encoded) != want {
		t.Fatalf("retained JSON = %s, %v; want %s", encoded, errMarshal, want)
	}
	doc := ojson.NewDocString(`{}`)
	if errSet := doc.Set("value", retained); errSet != nil {
		t.Fatal(errSet)
	}
	if got := ojson.GetBytesNoCopy(doc.Bytes(), "value").Raw; got != want {
		t.Fatalf("document numeric tokens = %s, want %s", got, want)
	}
}
