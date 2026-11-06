package probe

import (
	"errors"
	"pocket-agent/internal/media"
	"pocket-agent/internal/storage"
)

func validEffort(value string) bool {
	switch value {
	case "low", "medium", "high", "max", "xhigh":
		return true
	}
	return false
}

func validateImages(images []media.Image) error {
	if len(images) > 3 {
		return errors.New("at most three images per message")
	}
	seen := map[string]bool{}
	for _, image := range images {
		if !storage.ValidID(image.ID) || seen[image.ID] || image.MIMEType != "image/png" && image.MIMEType != "image/jpeg" || image.Size < 1 || image.Size > 4<<20 || image.Width < 1 || image.Height < 1 || image.Width > 4096 || image.Height > 4096 || int64(image.Width)*int64(image.Height) > 12_000_000 || image.DataURL != "" {
			return errors.New("invalid image reference")
		}
		seen[image.ID] = true
	}
	return nil
}

func persistentMessageValid(message Message) bool {
	if message.Role != "user" && message.Role != "assistant" && message.Role != "tool" {
		return false
	}
	if len(message.Images) > 0 && message.Role != "user" {
		return false
	}
	if validateImages(message.Images) != nil {
		return false
	}
	return message.ReasoningContent == "" || message.Role == "assistant" && len(message.ReasoningContent) <= 256<<10
}

func messageExtended(message Message) bool {
	return len(message.Images) > 0 || message.ReasoningContent != ""
}
