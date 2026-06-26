package scheduler

import (
	"fmt"
	"strings"
)

func ValidateMessageDeliveryPayload(payload map[string]any) error {
	if !hasMessageDeliveryContent(payload) {
		return nil
	}

	value, ok := payload["replyMessageID"]
	if !ok {
		return fmt.Errorf("payload.replyMessageID must be set explicitly for scheduled messages: use the original message id to reply in topic/thread, or set it to an empty string to send a new group message")
	}
	if _, ok := value.(string); !ok {
		return fmt.Errorf("payload.replyMessageID must be a string: use the original message id to reply in topic/thread, or set it to an empty string to send a new group message")
	}
	return nil
}

func hasMessageDeliveryContent(payload map[string]any) bool {
	if len(payload) == 0 {
		return false
	}
	for _, key := range []string{"promptText", "promptFile", "notifyText"} {
		value, ok := payload[key]
		if !ok {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(value)) != "" {
			return true
		}
	}
	return false
}
