package openai

import (
	"testing"

	"github.com/nguyenha935/kiro-cpa-plugin/internal/modelcapabilities"
	"github.com/tidwall/gjson"
)

const testImageDataURL = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUg=="

// kiroPayloadForBlock builds the payload actually sent upstream for a request
// whose only user content is the given block.
func kiroPayloadForBlock(block string) []byte {
	body := []byte(`{"messages":[{"role":"user","content":[` + block + `]}]}`)
	payload, _ := BuildKiroPayloadFromOpenAI(body, "claude-opus-5", "profile", "AI_EDITOR", modelcapabilities.Capability{ModelID: "claude-opus-5"}, "")
	return payload
}

func kiroUserMessage(payload []byte) gjson.Result {
	return gjson.ParseBytes(payload).Get("conversationState.currentMessage.userInputMessage")
}

// validateKiroContentBlocks accepts input_image as a valid image block, but the
// builder had no case for it, so the block was dropped without a word: the
// model answered about an image it never received and the client saw HTTP 200.
// Every spelling the validator admits must produce the same upstream payload.
func TestInputImageReachesUpstreamLikeImageURL(t *testing.T) {
	t.Parallel()

	blocks := map[string]string{
		"image_url":                `{"type":"image_url","image_url":{"url":"` + testImageDataURL + `"}}`,
		"input_image object":       `{"type":"input_image","image_url":{"url":"` + testImageDataURL + `"}}`,
		"input_image bare string":  `{"type":"input_image","image_url":"` + testImageDataURL + `"}`,
		"image_url as bare string": `{"type":"image_url","image_url":"` + testImageDataURL + `"}`,
	}

	for name, block := range blocks {
		images := kiroUserMessage(kiroPayloadForBlock(block)).Get("images").Array()
		if len(images) != 1 {
			t.Fatalf("%s: upstream carried %d images, want 1", name, len(images))
		}
		if format := images[0].Get("format").String(); format != "png" {
			t.Fatalf("%s: image format = %q, want png", name, format)
		}
		if data := images[0].Get("source.bytes").String(); data != "iVBORw0KGgoAAAANSUhEUg==" {
			t.Fatalf("%s: image bytes = %q", name, data)
		}
	}
}

// input_text is accepted by the same validator and dropped by the same switch,
// so the text of such a block must reach the upstream turn.
func TestInputTextReachesUpstreamLikeText(t *testing.T) {
	t.Parallel()

	for _, blockType := range []string{"text", "input_text"} {
		content := kiroUserMessage(kiroPayloadForBlock(`{"type":"` + blockType + `","text":"hello upstream"}`)).Get("content").String()
		if content != "hello upstream" {
			t.Fatalf("block type %q produced content %q, want the block's text", blockType, content)
		}
	}
}

// A remote URL is not inline bytes and Kiro takes bytes only, so it must not
// become an empty or malformed image entry.
func TestNonDataImageURLProducesNoImage(t *testing.T) {
	t.Parallel()

	if images := kiroUserMessage(kiroPayloadForBlock(`{"type":"input_image","image_url":"https://example.com/a.png"}`)).Get("images").Array(); len(images) != 0 {
		t.Fatalf("a remote URL produced %d images, want none", len(images))
	}
}
