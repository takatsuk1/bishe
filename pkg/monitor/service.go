package monitor

import (
	"ai/pkg/storage"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	defaultSnapshotLimit = 1500
	defaultErrorLimit    = 1000
)

type Service struct {
	store *storage.MySQLStorage
	rules AlertRules
}

func NewService(store *storage.MySQLStorage, rules *AlertRules) *Service {
	if store == nil {
		return nil
	}
	finalRules := DefaultAlertRules()
	if rules != nil {
		if rules.NodeSlowThresholdMs > 0 {
			finalRules.NodeSlowThresholdMs = rules.NodeSlowThresholdMs
		}
		if rules.WorkflowSlowThresholdMs > 0 {
			finalRules.WorkflowSlowThresholdMs = rules.WorkflowSlowThresholdMs
		}
	}
	return &Service{store: store, rules: finalRules}
}

func (s *Service) Rules() AlertRules {
	if s == nil {
		return DefaultAlertRules()
	}
	return s.rules
}

func (s *Service) CreateRun(ctx context.Context, in CreateRunInput) error {
	if s == nil || s.store == nil {
		return nil
	}
	startedAt := in.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	status := string(in.Status)
	if strings.TrimSpace(status) == "" {
		status = string(StatusRunning)
	}
	err := s.store.CreateMonitorRun(ctx, &storage.MonitorRun{
		RunID:         strings.TrimSpace(in.RunID),
		WorkflowID:    strings.TrimSpace(in.WorkflowID),
		UserID:        strings.TrimSpace(in.UserID),
		SourceAgentID: strings.TrimSpace(in.SourceAgentID),
		TaskID:        strings.TrimSpace(in.TaskID),
		Status:        status,
		StartedAt:     startedAt,
		AlertCount:    0,
	})
	if err != nil {
		return err
	}
	return s.AppendEvent(ctx, AppendEventInput{
		RunID:      in.RunID,
		TaskID:     in.TaskID,
		WorkflowID: in.WorkflowID,
		UserID:     in.UserID,
		AgentID:    in.SourceAgentID,
		EventType:  EventTypeWorkflowStarted,
		Status:     StatusRunning,
		Message:    "workflow started",
	})
}

func (s *Service) FinishRun(ctx context.Context, in FinishRunInput) error {
	if s == nil || s.store == nil {
		return nil
	}
	finishedAt := in.FinishedAt
	if finishedAt.IsZero() {
		finishedAt = time.Now()
	}
	status := string(in.Status)
	if strings.TrimSpace(status) == "" {
		status = string(StatusSucceeded)
	}
	if err := s.store.FinishMonitorRun(ctx, in.RunID, status, finishedAt, in.DurationMs, truncateText(in.ErrorMessage, defaultErrorLimit)); err != nil {
		return err
	}

	run, err := s.store.GetMonitorRun(ctx, in.RunID, "")
	if err != nil {
		return nil
	}

	eventType := EventTypeWorkflowFinished
	eventStatus := StatusSucceeded
	message := "workflow finished"
	if in.Status != StatusSucceeded {
		eventType = EventTypeWorkflowFailed
		eventStatus = StatusFailed
		message = "workflow failed"
	}
	_ = s.AppendEvent(ctx, AppendEventInput{
		RunID:        run.RunID,
		TaskID:       run.TaskID,
		WorkflowID:   run.WorkflowID,
		UserID:       run.UserID,
		AgentID:      run.SourceAgentID,
		EventType:    eventType,
		Status:       eventStatus,
		Message:      message,
		ErrorMessage: in.ErrorMessage,
		DurationMs:   in.DurationMs,
	})

	return nil
}

func (s *Service) UpdateCurrentNode(ctx context.Context, runID, nodeID string) error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.UpdateMonitorRunCurrentNode(ctx, runID, nodeID)
}

func (s *Service) AppendEvent(ctx context.Context, in AppendEventInput) error {
	if s == nil || s.store == nil {
		return nil
	}
	eventID := strings.TrimSpace(in.EventID)
	if eventID == "" {
		eventID = fmt.Sprintf("evt_%d", time.Now().UnixNano())
	}
	status := string(in.Status)
	if strings.TrimSpace(status) == "" {
		status = string(StatusRunning)
	}

	return s.store.CreateMonitorEvent(ctx, &storage.MonitorEvent{
		EventID:        eventID,
		RunID:          strings.TrimSpace(in.RunID),
		TaskID:         strings.TrimSpace(in.TaskID),
		WorkflowID:     strings.TrimSpace(in.WorkflowID),
		UserID:         strings.TrimSpace(in.UserID),
		AgentID:        strings.TrimSpace(in.AgentID),
		NodeID:         strings.TrimSpace(in.NodeID),
		EventType:      string(in.EventType),
		Status:         status,
		Message:        truncateText(in.Message, defaultSnapshotLimit),
		InputSnapshot:  summarizeAny(in.InputSnapshot, defaultSnapshotLimit),
		OutputSnapshot: summarizeAny(in.OutputSnapshot, defaultSnapshotLimit),
		ErrorMessage:   truncateText(in.ErrorMessage, defaultErrorLimit),
		DurationMs:     in.DurationMs,
	})
}

func (s *Service) TriggerAlert(ctx context.Context, in TriggerAlertInput) error {
	if s == nil || s.store == nil {
		return nil
	}
	alertID := strings.TrimSpace(in.AlertID)
	if alertID == "" {
		alertID = fmt.Sprintf("alt_%d", time.Now().UnixNano())
	}
	triggeredAt := in.TriggeredAt
	if triggeredAt.IsZero() {
		triggeredAt = time.Now()
	}
	status := strings.TrimSpace(in.Status)
	if status == "" {
		status = "open"
	}
	if err := s.store.CreateMonitorAlert(ctx, &storage.MonitorAlert{
		AlertID:     alertID,
		RunID:       strings.TrimSpace(in.RunID),
		WorkflowID:  strings.TrimSpace(in.WorkflowID),
		AgentID:     strings.TrimSpace(in.AgentID),
		NodeID:      strings.TrimSpace(in.NodeID),
		AlertType:   strings.TrimSpace(in.AlertType),
		Severity:    strings.TrimSpace(in.Severity),
		Title:       truncateText(in.Title, 255),
		Content:     truncateText(in.Content, defaultSnapshotLimit),
		Status:      status,
		TriggeredAt: triggeredAt,
	}); err != nil {
		return err
	}
	_ = s.store.IncreaseMonitorRunAlertCount(ctx, in.RunID, 1)
	_ = s.AppendEvent(ctx, AppendEventInput{
		RunID:        in.RunID,
		TaskID:       in.TaskID,
		WorkflowID:   in.WorkflowID,
		UserID:       in.UserID,
		AgentID:      in.AgentID,
		NodeID:       in.NodeID,
		EventType:    EventTypeAlertTriggered,
		Status:       StatusSucceeded,
		Message:      in.Title,
		ErrorMessage: in.Content,
	})
	return nil
}

func (s *Service) ListRuns(ctx context.Context, in ListRunsInput) ([]storage.MonitorRun, int64, error) {
	if s == nil || s.store == nil {
		return nil, 0, nil
	}
	return s.store.ListMonitorRuns(ctx, storage.MonitorRunQuery{
		UserID:     strings.TrimSpace(in.UserID),
		WorkflowID: strings.TrimSpace(in.WorkflowID),
		TaskID:     strings.TrimSpace(in.TaskID),
		Status:     strings.TrimSpace(in.Status),
		Page:       in.Page,
		PageSize:   in.PageSize,
	})
}

func (s *Service) GetRunDetail(ctx context.Context, runID, userID string) (*storage.MonitorRunDetail, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	return s.store.GetMonitorRunDetail(ctx, strings.TrimSpace(runID), strings.TrimSpace(userID))
}

func (s *Service) ListRunEvents(ctx context.Context, in ListRunEventsInput) ([]storage.MonitorEvent, int64, error) {
	if s == nil || s.store == nil {
		return nil, 0, nil
	}
	return s.store.ListMonitorEvents(ctx, storage.MonitorEventQuery{
		RunID:    strings.TrimSpace(in.RunID),
		Page:     in.Page,
		PageSize: in.PageSize,
	})
}

func (s *Service) ListAlerts(ctx context.Context, in ListAlertsInput) ([]storage.MonitorAlert, int64, error) {
	if s == nil || s.store == nil {
		return nil, 0, nil
	}
	return s.store.ListMonitorAlerts(ctx, storage.MonitorAlertQuery{
		UserID:     strings.TrimSpace(in.UserID),
		RunID:      strings.TrimSpace(in.RunID),
		WorkflowID: strings.TrimSpace(in.WorkflowID),
		Status:     strings.TrimSpace(in.Status),
		Page:       in.Page,
		PageSize:   in.PageSize,
	})
}

func (s *Service) BuildOverview(ctx context.Context, userID string, recentLimit int) (storage.MonitorOverview, error) {
	if s == nil || s.store == nil {
		return storage.MonitorOverview{}, nil
	}
	return s.store.BuildMonitorOverview(ctx, strings.TrimSpace(userID), recentLimit)
}

func (s *Service) GetRunFamily(ctx context.Context, runID, userID string, limit int) (storage.MonitorRunFamily, error) {
	if s == nil || s.store == nil {
		return storage.MonitorRunFamily{}, nil
	}
	runs, err := s.store.ListMonitorRunFamily(ctx, strings.TrimSpace(runID), strings.TrimSpace(userID), limit)
	if err != nil {
		return storage.MonitorRunFamily{}, err
	}
	family := storage.MonitorRunFamily{RootRunID: strings.TrimSpace(runID), Runs: runs}
	if len(runs) > 0 {
		family.RootRunID = runs[0].RunID
	}
	return family, nil
}

func (s *Service) AcknowledgeAlert(ctx context.Context, alertID string) error {
	if s == nil || s.store == nil {
		return nil
	}
	alertID = strings.TrimSpace(alertID)
	if alertID == "" {
		return fmt.Errorf("alert id is required")
	}
	alert, err := s.store.GetMonitorAlert(ctx, alertID)
	if err != nil {
		return err
	}
	current := strings.ToLower(strings.TrimSpace(alert.Status))
	if current == "resolved" {
		return fmt.Errorf("resolved alert cannot be acknowledged")
	}
	return s.store.UpdateMonitorAlertStatus(ctx, alertID, "acknowledged")
}

func (s *Service) ResolveAlert(ctx context.Context, alertID string) error {
	if s == nil || s.store == nil {
		return nil
	}
	alertID = strings.TrimSpace(alertID)
	if alertID == "" {
		return fmt.Errorf("alert id is required")
	}
	if _, err := s.store.GetMonitorAlert(ctx, alertID); err != nil {
		return err
	}
	return s.store.UpdateMonitorAlertStatus(ctx, alertID, "resolved")
}

func truncateText(v string, maxLen int) string {
	trimmed := strings.TrimSpace(v)
	if maxLen <= 0 || len(trimmed) <= maxLen {
		return trimmed
	}
	return trimmed[:maxLen]
}

func summarizeAny(v any, maxLen int) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return truncateText(t, maxLen)
	case []byte:
		return truncateText(string(t), maxLen)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return truncateText(fmt.Sprintf("%v", v), maxLen)
		}
		return truncateText(string(b), maxLen)
	}
}
