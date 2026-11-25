package runtime

import (
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"strings"
	"unicode/utf8"

	model "agent-acp.local/sdks/go/model"
)

func promptBlocks(request *model.SendRequest, caps *model.Capabilities) ([]any, string, error) {
	if len(request.Parts) == 0 {
		return []any{map[string]string{"type": "text", "text": request.Text}}, request.Text, nil
	}
	if request.Text != "" {
		return nil, "", errors.New("send requires either text or ordered prompt parts")
	}
	blocks := make([]any, 0, len(request.Parts))
	var preview strings.Builder
	for _, part := range request.Parts {
		if text, ok := part.GetContent().(*model.PromptPart_Text); ok {
			blocks = append(blocks, map[string]string{"type": "text", "text": text.Text})
			preview.WriteString(text.Text)
			continue
		}
		var kind string
		var media *model.PromptMedia
		switch part.GetContent().(type) {
		case *model.PromptPart_Image:
			kind, media = "image", part.GetImage()
			if !caps.GetCanPromptImage() {
				return nil, "", errors.New("adapter does not support image prompts")
			}
		case *model.PromptPart_ResourceLink, *model.PromptPart_Resource:
			block, label, err := PromptResourceBlock(part, caps.GetCanPromptEmbeddedContext())
			if err != nil {
				return nil, "", err
			}
			blocks = append(blocks, block)
			preview.WriteString(label)
			continue
		case *model.PromptPart_Audio:
			kind, media = "audio", part.GetAudio()
			if !caps.GetCanPromptAudio() {
				return nil, "", errors.New("adapter does not support audio prompts")
			}
		default:
			return nil, "", errors.New("prompt part has no supported content")
		}
		mimeType, parameters, err := mime.ParseMediaType(media.GetMimeType())
		if err != nil || len(parameters) != 0 || !strings.HasPrefix(mimeType, kind+"/") || len(media.GetData()) == 0 {
			return nil, "", fmt.Errorf("%s prompt part requires inline bytes and the corresponding MIME type", kind)
		}
		blocks = append(blocks, map[string]string{
			"type": kind, "mimeType": mimeType, "data": base64.StdEncoding.EncodeToString(media.GetData()),
		})
		fmt.Fprintf(&preview, "[ACP %s] %s", kind, mimeType)
	}
	return blocks, preview.String(), nil
}

// PromptResourceBlock returns the ACP content and a compact preview for a
// portable resource part. Callers may retain the same shape in scoped history.
func PromptResourceBlock(part *model.PromptPart, embeddedContext bool) (any, string, error) {
	var uri, mimeType string
	link, resource := part.GetResourceLink(), part.GetResource()
	switch {
	case link != nil && strings.TrimSpace(link.GetName()) != "":
		uri, mimeType = link.GetUri(), link.GetMimeType()
	case resource != nil && embeddedContext:
		uri, mimeType = resource.GetUri(), resource.GetMimeType()
	default:
		return nil, "", errors.New("a resource link requires a name and an embedded resource requires negotiated support")
	}
	address, err := url.Parse(uri)
	if err != nil || !address.IsAbs() {
		return nil, "", errors.New("a prompt resource requires an absolute URI")
	}
	if mimeType != "" {
		if _, _, err := mime.ParseMediaType(mimeType); err != nil {
			return nil, "", errors.New("invalid prompt resource MIME type")
		}
	}
	values := map[string]string{"uri": uri}
	if mimeType != "" {
		values["mimeType"] = mimeType
	}
	if link != nil {
		values["name"], values["type"] = link.Name, "resource_link"
		return values, "[ACP resource link] " + link.Name, nil
	}
	switch body := resource.GetBody().(type) {
	case *model.PromptResource_Text:
		if !utf8.ValidString(body.Text) {
			return nil, "", errors.New("a text resource requires valid UTF-8")
		}
		values["text"] = body.Text
	case *model.PromptResource_Blob:
		values["blob"] = base64.StdEncoding.EncodeToString(body.Blob)
	default:
		return nil, "", errors.New("an embedded resource requires text or blob content")
	}
	return map[string]any{"type": "resource", "resource": values}, "[ACP embedded resource]", nil
}
