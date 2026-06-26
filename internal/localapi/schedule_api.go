package localapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/chenxuan520/agentbot/internal/conversation"
	"github.com/chenxuan520/agentbot/internal/scheduler"
)

type scheduleListItem struct {
	scheduler.Job
	PromptTextResolved string `json:"PromptTextResolved,omitempty"`
	NotifyTextResolved string `json:"NotifyTextResolved,omitempty"`
}

func parseSchedulePayloadMap(payloadText string) (map[string]any, error) {
	payloadText = strings.TrimSpace(payloadText)
	if payloadText == "" {
		return map[string]any{}, nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadText), &payload); err != nil {
		return nil, err
	}
	if payload == nil {
		return map[string]any{}, nil
	}
	return payload, nil
}

func schedulePayloadString(payload map[string]any, key string) string {
	value, ok := payload[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func schedulePayloadContentKind(payload map[string]any) string {
	if schedulePayloadString(payload, "promptText") != "" || schedulePayloadString(payload, "promptFile") != "" {
		return "prompt"
	}
	if schedulePayloadString(payload, "notifyText") != "" {
		return "notify"
	}
	return ""
}

func (s *Server) listScheduleItems(ref conversation.Ref, includeDone bool, limit int) ([]scheduleListItem, error) {
	var (
		jobs []scheduler.Job
		err  error
	)
	if includeDone {
		jobs, err = s.scheduler.List(ref, limit)
	} else {
		jobs, err = s.scheduler.ListActive(ref, limit)
	}
	if err != nil {
		return nil, err
	}
	items := make([]scheduleListItem, 0, len(jobs))
	for _, job := range jobs {
		item, err := s.buildScheduleListItem(job)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Server) buildScheduleListItem(job scheduler.Job) (scheduleListItem, error) {
	item := scheduleListItem{Job: job}
	payload, err := parseSchedulePayloadMap(job.Payload)
	if err != nil {
		return item, err
	}
	item.NotifyTextResolved = schedulePayloadString(payload, "notifyText")
	if promptText := schedulePayloadString(payload, "promptText"); promptText != "" {
		item.PromptTextResolved = promptText
		return item, nil
	}
	promptFile := schedulePayloadString(payload, "promptFile")
	if promptFile == "" || s.prompts == nil {
		return item, nil
	}
	promptText, err := s.prompts.ReadPrompt(promptFile)
	if err != nil {
		return item, err
	}
	item.PromptTextResolved = promptText
	return item, nil
}

func (s *Server) handleScheduleUpdate(c *gin.Context) {
	var body struct {
		JobID   string `json:"jobId"`
		Kind    string `json:"kind"`
		Content string `json:"content"`
	}
	if !bindJSON(c, &body) {
		return
	}
	jobID := strings.TrimSpace(body.JobID)
	if jobID == "" {
		writeStatusError(c, http.StatusBadRequest, fmt.Errorf("jobId is required"))
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		writeStatusError(c, http.StatusBadRequest, fmt.Errorf("content is required"))
		return
	}
	job, err := s.scheduler.Get(jobID)
	if err != nil {
		writeError(c, err)
		return
	}
	if job.Status != scheduler.StatusPending && job.Status != scheduler.StatusRunning {
		writeStatusError(c, http.StatusConflict, fmt.Errorf("only active scheduled jobs can be edited"))
		return
	}
	payload, err := parseSchedulePayloadMap(job.Payload)
	if err != nil {
		writeError(c, err)
		return
	}
	existingKind := schedulePayloadContentKind(payload)
	if existingKind == "" {
		writeStatusError(c, http.StatusConflict, fmt.Errorf("scheduled job does not contain editable prompt or notify content"))
		return
	}
	kind := strings.ToLower(strings.TrimSpace(body.Kind))
	if kind == "" {
		kind = existingKind
	}
	if kind != existingKind {
		writeStatusError(c, http.StatusConflict, fmt.Errorf("content kind mismatch: current job uses %s", existingKind))
		return
	}
	switch kind {
	case "prompt":
		promptFile := schedulePayloadString(payload, "promptFile")
		if promptFile != "" {
			if s.prompts == nil {
				writeStatusError(c, http.StatusServiceUnavailable, fmt.Errorf("prompt store is not configured"))
				return
			}
			if err := s.prompts.WritePromptContent(promptFile, body.Content); err != nil {
				writeError(c, err)
				return
			}
			delete(payload, "promptText")
		} else {
			payload["promptText"] = body.Content
		}
	case "notify":
		payload["notifyText"] = body.Content
	default:
		writeStatusError(c, http.StatusBadRequest, fmt.Errorf("unsupported content kind %q", body.Kind))
		return
	}
	if err := scheduler.ValidateMessageDeliveryPayload(payload); err != nil {
		writeError(c, err)
		return
	}
	if err := s.scheduler.UpdatePayload(job.ID, mustJSON(payload)); err != nil {
		writeError(c, err)
		return
	}
	updated, err := s.scheduler.Get(job.ID)
	if err != nil {
		writeError(c, err)
		return
	}
	item, err := s.buildScheduleListItem(updated)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, item)
}
