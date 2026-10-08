package executor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const maxKiroToolNameBytes = 64

type toolNameAliases struct {
	toKiro   map[string]string
	toClient map[string]string
}

func (aliases toolNameAliases) original(name string) string {
	if original, ok := aliases.toClient[name]; ok {
		return original
	}
	return name
}

// aliasKiroToolNames keeps client tool names stable while using Kiro-safe names
// in the current catalogue and structured history. The response uses toClient
// before it leaves this request; no mapping is shared between requests.
func aliasKiroToolNames(body []byte, source sdktranslator.Format) ([]byte, toolNameAliases, error) {
	openAIChat := source.String() == sdktranslator.FormatOpenAI.String()
	namePath := "name"
	if openAIChat {
		namePath = "function.name"
	}
	tools := gjson.GetBytes(body, "tools").Array()
	reserved := make(map[string]struct{}, len(tools))
	longNames := make(map[string]struct{})
	for _, tool := range tools {
		name := tool.Get(namePath).String()
		if len(name) > maxKiroToolNameBytes {
			longNames[name] = struct{}{}
		} else {
			reserved[name] = struct{}{}
		}
	}
	if len(longNames) == 0 {
		return body, toolNameAliases{}, nil
	}

	aliases := toolNameAliases{
		toKiro:   make(map[string]string, len(longNames)),
		toClient: make(map[string]string, len(longNames)),
	}
	ordered := make([]string, 0, len(longNames))
	for name := range longNames {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	for _, name := range ordered {
		assigned := false
		for attempt := 0; attempt <= len(tools)+1; attempt++ {
			alias := kiroToolAliasCandidate(name, attempt)
			if _, exists := reserved[alias]; exists {
				continue
			}
			reserved[alias] = struct{}{}
			aliases.toKiro[name] = alias
			aliases.toClient[alias] = name
			assigned = true
			break
		}
		if !assigned {
			return nil, toolNameAliases{}, fmt.Errorf("cannot assign a unique Kiro tool name")
		}
	}

	var err error
	for index, tool := range tools {
		name := tool.Get(namePath).String()
		alias, ok := aliases.toKiro[name]
		if !ok {
			continue
		}
		body, err = sjson.SetBytes(body, fmt.Sprintf("tools.%d.%s", index, namePath), alias)
		if err != nil {
			return nil, toolNameAliases{}, fmt.Errorf("alias Kiro tool definition: %w", err)
		}
		descriptionPath := "description"
		if openAIChat {
			descriptionPath = "function.description"
		}
		description := tool.Get(descriptionPath).String()
		if !strings.Contains(description, name) {
			description = truncateUTF8("Original tool name: "+name+"\n"+description, maxKiroToolDescriptionBytes)
			body, err = sjson.SetBytes(body, fmt.Sprintf("tools.%d.%s", index, descriptionPath), description)
			if err != nil {
				return nil, toolNameAliases{}, fmt.Errorf("annotate Kiro tool description: %w", err)
			}
		}
	}
	for i, message := range gjson.GetBytes(body, "messages").Array() {
		if message.Get("role").String() != "assistant" {
			continue
		}
		if openAIChat {
			for j, call := range message.Get("tool_calls").Array() {
				if call.Get("type").String() != "function" {
					continue
				}
				if alias, ok := aliases.toKiro[call.Get("function.name").String()]; ok {
					body, err = sjson.SetBytes(body, fmt.Sprintf("messages.%d.tool_calls.%d.function.name", i, j), alias)
					if err != nil {
						return nil, toolNameAliases{}, fmt.Errorf("alias Kiro tool history: %w", err)
					}
				}
			}
			// OpenAI requests can also carry Anthropic-shaped content blocks.
			// They reach the same Kiro history builder as tool_calls.
			for j, block := range message.Get("content").Array() {
				if block.Get("type").String() != "tool_use" {
					continue
				}
				if alias, ok := aliases.toKiro[block.Get("name").String()]; ok {
					body, err = sjson.SetBytes(body, fmt.Sprintf("messages.%d.content.%d.name", i, j), alias)
					if err != nil {
						return nil, toolNameAliases{}, fmt.Errorf("alias Kiro tool history: %w", err)
					}
				}
			}
			continue
		}
		for j, block := range message.Get("content").Array() {
			if block.Get("type").String() != "tool_use" {
				continue
			}
			if alias, ok := aliases.toKiro[block.Get("name").String()]; ok {
				body, err = sjson.SetBytes(body, fmt.Sprintf("messages.%d.content.%d.name", i, j), alias)
				if err != nil {
					return nil, toolNameAliases{}, fmt.Errorf("alias Kiro tool history: %w", err)
				}
			}
		}
	}
	return body, aliases, nil
}

// Aliasing runs after the SDK request translation, so it only sees Claude or
// OpenAI chat shapes. Some CPA SDK versions already shorten Responses tool names
// and reverse them from the original request; aliasing the translated body keeps
// that mapping intact because the plugin restores its own names first.
func prepareKiroRequest(body []byte, model string, source, target sdktranslator.Format) ([]byte, toolNameAliases, error) {
	translated := sdktranslator.TranslateRequest(source, target, model, bytes.Clone(body), true)
	return normalizeKiroRequestWithAliases(translated, source)
}

func kiroToolAliasCandidate(name string, attempt int) string {
	input := name
	if attempt > 0 {
		input += "\x00" + strconv.Itoa(attempt)
	}
	digest := sha256.Sum256([]byte(input))
	return "kiro_" + hex.EncodeToString(digest[:])[:58]
}
