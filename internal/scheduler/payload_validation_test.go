package scheduler

import "testing"

func TestValidateMessageDeliveryPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload map[string]any
		wantErr bool
	}{
		{
			name:    "non message payload allowed",
			payload: map[string]any{"foo": "bar"},
			wantErr: false,
		},
		{
			name:    "prompt text requires reply target field",
			payload: map[string]any{"promptText": "check alert"},
			wantErr: true,
		},
		{
			name:    "notify text requires reply target field",
			payload: map[string]any{"notifyText": "hello"},
			wantErr: true,
		},
		{
			name:    "prompt file requires reply target field",
			payload: map[string]any{"promptFile": "data/scheduler/prompts/x.md"},
			wantErr: true,
		},
		{
			name:    "explicit topic reply allowed",
			payload: map[string]any{"promptText": "check alert", "replyMessageID": "om_xxx"},
			wantErr: false,
		},
		{
			name:    "explicit group send allowed",
			payload: map[string]any{"promptText": "check alert", "replyMessageID": ""},
			wantErr: false,
		},
		{
			name:    "reply target must be string",
			payload: map[string]any{"promptText": "check alert", "replyMessageID": 123},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateMessageDeliveryPayload(tt.payload)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateMessageDeliveryPayload(%v) error = %v, wantErr %v", tt.payload, err, tt.wantErr)
			}
		})
	}
}
