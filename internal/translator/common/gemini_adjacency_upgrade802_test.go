package common

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestUpgrade802GeminiToolAdjacencyKeepsModelSignatureBoundaries(t *testing.T) {
	firstModel := []byte(`{"role":"model","parts":[{"text":"signed first","thought":true,"thoughtSignature":"signature-a"},{"functionCall":{"id":"a","name":"read","args":{"author":"source"}}}]}`)
	secondModel := []byte(`{"role":"model","parts":[{"text":"signed second","thought":true,"thoughtSignature":"signature-b"},{"functionCall":{"id":"b","name":"write","args":{}}}]}`)
	contents := [][]byte{
		firstModel,
		[]byte(`{"role":"user","parts":[{"text":"first reminder"}]}`),
		[]byte(`{"role":"user","parts":[{"text":"first trailing"},{"functionResponse":{"id":"a","name":"read","response":{"result":false,"count":9007199254740993}}}]}`),
		secondModel,
		[]byte(`{"role":"user","parts":[{"text":"second reminder"}]}`),
		[]byte(`{"role":"user","parts":[{"functionResponse":{"id":"b","name":"write","response":{"result":null}}}]}`),
	}
	out := MergeAdjacentGeminiUserContents(SplitGeminiFunctionResponseTurns(contents))
	if len(out) != 6 {
		t.Fatalf("unexpected model/result/reminder layout: %s", JoinRawArray(out))
	}
	if !bytes.Equal(out[0], firstModel) || !bytes.Equal(out[3], secondModel) {
		t.Fatal("signature-bearing model turns changed or merged across a tool boundary")
	}
	for _, tc := range []struct {
		index int
		id    string
		value string
	}{{1, "a", "false"}, {4, "b", "null"}} {
		if id := gjson.GetBytes(out[tc.index], "parts.0.functionResponse.id").String(); id != tc.id {
			t.Fatalf("result id=%q, want adjacent call %q", id, tc.id)
		}
		if value := gjson.GetBytes(out[tc.index], "parts.0.functionResponse.response.result").Raw; value != tc.value {
			t.Fatalf("result value=%q, want %q", value, tc.value)
		}
	}
	if n := gjson.GetBytes(out[1], "parts.0.functionResponse.response.count").Raw; n != "9007199254740993" {
		t.Fatalf("large integer changed: %s", n)
	}
	if gjson.GetBytes(out[2], "parts.0.text").String() != "first reminder" || gjson.GetBytes(out[2], "parts.1.text").String() != "first trailing" || gjson.GetBytes(out[5], "parts.0.text").String() != "second reminder" {
		t.Fatalf("reminders crossed a model boundary: %s", JoinRawArray(out))
	}
	secondPass := MergeAdjacentGeminiUserContents(SplitGeminiFunctionResponseTurns(out))
	if !bytes.Equal(JoinRawArray(secondPass), JoinRawArray(out)) {
		t.Fatal("normalization is not idempotent")
	}
}
