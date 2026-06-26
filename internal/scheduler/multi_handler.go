package scheduler

import "time"

type MultiHandler struct {
	handlers []Handler
}

func NewMultiHandler(handlers ...Handler) *MultiHandler {
	return &MultiHandler{handlers: handlers}
}

func (h *MultiHandler) Handle(job Job, triggeredAt time.Time) error {
	for _, handler := range h.handlers {
		if handler == nil {
			continue
		}
		if err := handler.Handle(job, triggeredAt); err != nil {
			return err
		}
	}
	return nil
}
