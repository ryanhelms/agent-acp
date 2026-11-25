package runtime

import (
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"strings"

	model "agent-acp.local/sdks/go/model"
)

func promptBlocks(request *model.SendRequest, caps *model.Capabilities) ([]map[string]string, string, error) {
	if len(request.Parts) == 0 {
		return []map[string]string{{"type": "text", "text": request.Text}}, request.Text, nil
	}
	if request.Text != "" {
		return nil, "", errors.New("send requires either text or ordered prompt parts")
	}
	blocks := make([]map[string]string, 0, len(request.Parts))
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
