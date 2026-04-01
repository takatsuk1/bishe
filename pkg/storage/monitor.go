package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func normalizePage(page, pageSize int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

func (s *MySQLStorage) CreateMonitorRun(ctx context.Context, run *MonitorRun) error {
	if run == nil {
		return fmt.Errorf("monitor run is nil")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO monitor_run
		(run_id, workflow_id, user_id, source_agent_id, task_id, status, started_at, current_node_id, error_message, alert_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		run.RunID, run.WorkflowID, run.UserID, nullString(run.SourceAgentID), nullString(run.TaskID), run.Status,
		run.StartedAt, nullString(run.CurrentNodeID), nullString(run.ErrorMessage), run.AlertCount,
	)
	if err != nil {
		return fmt.Errorf("insert monitor_run: %w", err)
	}
	return nil
}

func (s *MySQLStorage) FinishMonitorRun(ctx context.Context, runID string, status string, finishedAt time.Time, durationMs int64, errorMessage string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE monitor_run
		SET status = ?, finished_at = ?, duration_ms = ?, error_message = ?, updated_at = NOW()
		WHERE run_id = ?
	`, status, finishedAt, durationMs, nullString(errorMessage), runID)
	if err != nil {
		return fmt.Errorf("update monitor_run finish: %w", err)
	}
	return nil
}

func (s *MySQLStorage) UpdateMonitorRunCurrentNode(ctx context.Context, runID, nodeID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE monitor_run
		SET current_node_id = ?, updated_at = NOW()
		WHERE run_id = ?
	`, nullString(nodeID), runID)
	if err != nil {
		return fmt.Errorf("update monitor_run current node: %w", err)
	}
	return nil
}

func (s *MySQLStorage) IncreaseMonitorRunAlertCount(ctx context.Context, runID string, delta int) error {
	if delta <= 0 {
		delta = 1
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE monitor_run
		SET alert_count = alert_count + ?, updated_at = NOW()
		WHERE run_id = ?
	`, delta, runID)
	if err != nil {
		return fmt.Errorf("increment monitor_run alert_count: %w", err)
	}
	return nil
}

func (s *MySQLStorage) CreateMonitorEvent(ctx context.Context, event *MonitorEvent) error {
	if event == nil {
		return fmt.Errorf("monitor event is nil")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO monitor_event
		(event_id, run_id, task_id, workflow_id, user_id, agent_id, node_id, event_type, status, message, input_snapshot, output_snapshot, error_message, duration_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		event.EventID, event.RunID, nullString(event.TaskID), event.WorkflowID, event.UserID,
		nullString(event.AgentID), nullString(event.NodeID), event.EventType, event.Status,
		nullString(event.Message), nullString(event.InputSnapshot), nullString(event.OutputSnapshot),
		nullString(event.ErrorMessage), event.DurationMs,
	)
	if err != nil {
		return fmt.Errorf("insert monitor_event: %w", err)
	}
	return nil
}

func (s *MySQLStorage) CreateMonitorAlert(ctx context.Context, alert *MonitorAlert) error {
	if alert == nil {
		return fmt.Errorf("monitor alert is nil")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO monitor_alert
		(alert_id, run_id, workflow_id, agent_id, node_id, alert_type, severity, title, content, status, triggered_at, resolved_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		alert.AlertID, alert.RunID, alert.WorkflowID, nullString(alert.AgentID), nullString(alert.NodeID),
		alert.AlertType, alert.Severity, alert.Title, nullString(alert.Content), alert.Status,
		alert.TriggeredAt, nullableTime(alert.ResolvedAt),
	)
	if err != nil {
		return fmt.Errorf("insert monitor_alert: %w", err)
	}
	return nil
}

func (s *MySQLStorage) ListMonitorRuns(ctx context.Context, q MonitorRunQuery) ([]MonitorRun, int64, error) {
	page, pageSize := normalizePage(q.Page, q.PageSize)
	offset := (page - 1) * pageSize

	where := []string{"1=1"}
	args := make([]any, 0, 8)
	if strings.TrimSpace(q.UserID) != "" {
		where = append(where, "user_id = ?")
		args = append(args, q.UserID)
	}
	if strings.TrimSpace(q.WorkflowID) != "" {
		where = append(where, "workflow_id = ?")
		args = append(args, q.WorkflowID)
	}
	if strings.TrimSpace(q.TaskID) != "" {
		where = append(where, "(task_id = ? OR task_id LIKE ?)")
		args = append(args, q.TaskID, "%:"+q.TaskID+":%")
	}
	if strings.TrimSpace(q.Status) != "" {
		where = append(where, "status = ?")
		args = append(args, q.Status)
	}

	whereSQL := strings.Join(where, " AND ")

	var total int64
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM monitor_run WHERE %s", whereSQL)
	if err := s.db.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count monitor_run: %w", err)
	}

	querySQL := fmt.Sprintf(`
		SELECT id, run_id, workflow_id, user_id, IFNULL(source_agent_id,''), IFNULL(task_id,''), status,
		       started_at, finished_at, duration_ms, IFNULL(current_node_id,''), IFNULL(error_message,''),
		       alert_count, created_at, updated_at
		FROM monitor_run
		WHERE %s
		ORDER BY started_at DESC
		LIMIT ? OFFSET ?
	`, whereSQL)
	queryArgs := append(args, pageSize, offset)
	rows, err := s.db.QueryContext(ctx, querySQL, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("query monitor_run: %w", err)
	}
	defer rows.Close()

	out := make([]MonitorRun, 0, pageSize)
	for rows.Next() {
		var item MonitorRun
		var finishedAt sql.NullTime
		if err := rows.Scan(
			&item.ID, &item.RunID, &item.WorkflowID, &item.UserID, &item.SourceAgentID, &item.TaskID,
			&item.Status, &item.StartedAt, &finishedAt, &item.DurationMs, &item.CurrentNodeID,
			&item.ErrorMessage, &item.AlertCount, &item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan monitor_run: %w", err)
		}
		if finishedAt.Valid {
			t := finishedAt.Time
			item.FinishedAt = &t
		}
		out = append(out, item)
	}

	return out, total, nil
}

func (s *MySQLStorage) GetMonitorRun(ctx context.Context, runID, userID string) (*MonitorRun, error) {
	query := `
		SELECT id, run_id, workflow_id, user_id, IFNULL(source_agent_id,''), IFNULL(task_id,''), status,
		       started_at, finished_at, duration_ms, IFNULL(current_node_id,''), IFNULL(error_message,''),
		       alert_count, created_at, updated_at
		FROM monitor_run
		WHERE run_id = ?
	`
	args := []any{runID}
	if strings.TrimSpace(userID) != "" {
		query += " AND (user_id = ? OR user_id = '' OR user_id IS NULL)"
		args = append(args, userID)
	}
	var item MonitorRun
	var finishedAt sql.NullTime
	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&item.ID, &item.RunID, &item.WorkflowID, &item.UserID, &item.SourceAgentID, &item.TaskID,
		&item.Status, &item.StartedAt, &finishedAt, &item.DurationMs, &item.CurrentNodeID,
		&item.ErrorMessage, &item.AlertCount, &item.CreatedAt, &item.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("monitor run not found: %s", runID)
		}
		return nil, fmt.Errorf("query monitor_run by run_id: %w", err)
	}
	if finishedAt.Valid {
		t := finishedAt.Time
		item.FinishedAt = &t
	}
	return &item, nil
}

func (s *MySQLStorage) ListMonitorRunFamily(ctx context.Context, runID, userID string, limit int) ([]MonitorRun, error) {
	root, err := s.GetMonitorRun(ctx, runID, userID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	out := []MonitorRun{*root}
	rootTaskID := strings.TrimSpace(root.TaskID)
	if rootTaskID == "" {
		return out, nil
	}

	query := `
		SELECT id, run_id, workflow_id, user_id, IFNULL(source_agent_id,''), IFNULL(task_id,''), status,
		       started_at, finished_at, duration_ms, IFNULL(current_node_id,''), IFNULL(error_message,''),
		       alert_count, created_at, updated_at
		FROM monitor_run
		WHERE (user_id = ? OR user_id = '' OR user_id IS NULL)
		  AND run_id <> ?
		  AND task_id LIKE ?
		ORDER BY started_at ASC
		LIMIT ?
	`
	likePattern := "%:" + rootTaskID + ":%"
	rows, err := s.db.QueryContext(ctx, query, root.UserID, root.RunID, likePattern, limit)
	if err != nil {
		return nil, fmt.Errorf("query monitor_run family: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var item MonitorRun
		var finishedAt sql.NullTime
		if err := rows.Scan(
			&item.ID, &item.RunID, &item.WorkflowID, &item.UserID, &item.SourceAgentID, &item.TaskID,
			&item.Status, &item.StartedAt, &finishedAt, &item.DurationMs, &item.CurrentNodeID,
			&item.ErrorMessage, &item.AlertCount, &item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan monitor_run family: %w", err)
		}
		if finishedAt.Valid {
			t := finishedAt.Time
			item.FinishedAt = &t
		}
		out = append(out, item)
	}

	return out, nil
}

func (s *MySQLStorage) ListMonitorEvents(ctx context.Context, q MonitorEventQuery) ([]MonitorEvent, int64, error) {
	page, pageSize := normalizePage(q.Page, q.PageSize)
	offset := (page - 1) * pageSize

	var total int64
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM monitor_event WHERE run_id = ?", q.RunID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count monitor_event: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, event_id, run_id, IFNULL(task_id,''), workflow_id, user_id, IFNULL(agent_id,''), IFNULL(node_id,''),
		       event_type, status, IFNULL(message,''), IFNULL(input_snapshot,''), IFNULL(output_snapshot,''),
		       IFNULL(error_message,''), duration_ms, created_at
		FROM monitor_event
		WHERE run_id = ?
		ORDER BY created_at ASC
		LIMIT ? OFFSET ?
	`, q.RunID, pageSize, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("query monitor_event: %w", err)
	}
	defer rows.Close()

	out := make([]MonitorEvent, 0, pageSize)
	for rows.Next() {
		var item MonitorEvent
		if err := rows.Scan(
			&item.ID, &item.EventID, &item.RunID, &item.TaskID, &item.WorkflowID, &item.UserID,
			&item.AgentID, &item.NodeID, &item.EventType, &item.Status, &item.Message,
			&item.InputSnapshot, &item.OutputSnapshot, &item.ErrorMessage, &item.DurationMs, &item.CreatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan monitor_event: %w", err)
		}
		out = append(out, item)
	}

	return out, total, nil
}

func (s *MySQLStorage) ListMonitorAlerts(ctx context.Context, q MonitorAlertQuery) ([]MonitorAlert, int64, error) {
	page, pageSize := normalizePage(q.Page, q.PageSize)
	offset := (page - 1) * pageSize

	where := []string{"1=1"}
	args := make([]any, 0, 8)
	if strings.TrimSpace(q.UserID) != "" {
		where = append(where, "run_id IN (SELECT run_id FROM monitor_run WHERE user_id = ?)")
		args = append(args, q.UserID)
	}
	if strings.TrimSpace(q.RunID) != "" {
		where = append(where, "run_id = ?")
		args = append(args, q.RunID)
	}
	if strings.TrimSpace(q.WorkflowID) != "" {
		where = append(where, "workflow_id = ?")
		args = append(args, q.WorkflowID)
	}
	if strings.TrimSpace(q.Status) != "" {
		where = append(where, "status = ?")
		args = append(args, q.Status)
	}
	whereSQL := strings.Join(where, " AND ")

	var total int64
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM monitor_alert WHERE %s", whereSQL)
	if err := s.db.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count monitor_alert: %w", err)
	}

	querySQL := fmt.Sprintf(`
		SELECT id, alert_id, run_id, workflow_id, IFNULL(agent_id,''), IFNULL(node_id,''), alert_type,
		       severity, title, IFNULL(content,''), status, triggered_at, resolved_at, created_at
		FROM monitor_alert
		WHERE %s
		ORDER BY triggered_at DESC
		LIMIT ? OFFSET ?
	`, whereSQL)
	queryArgs := append(args, pageSize, offset)
	rows, err := s.db.QueryContext(ctx, querySQL, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("query monitor_alert: %w", err)
	}
	defer rows.Close()

	out := make([]MonitorAlert, 0, pageSize)
	for rows.Next() {
		var item MonitorAlert
		var resolvedAt sql.NullTime
		if err := rows.Scan(
			&item.ID, &item.AlertID, &item.RunID, &item.WorkflowID, &item.AgentID, &item.NodeID,
			&item.AlertType, &item.Severity, &item.Title, &item.Content, &item.Status,
			&item.TriggeredAt, &resolvedAt, &item.CreatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan monitor_alert: %w", err)
		}
		if resolvedAt.Valid {
			t := resolvedAt.Time
			item.ResolvedAt = &t
		}
		out = append(out, item)
	}

	return out, total, nil
}

func (s *MySQLStorage) GetMonitorAlert(ctx context.Context, alertID string) (*MonitorAlert, error) {
	var item MonitorAlert
	var resolvedAt sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT id, alert_id, run_id, workflow_id, IFNULL(agent_id,''), IFNULL(node_id,''), alert_type,
		       severity, title, IFNULL(content,''), status, triggered_at, resolved_at, created_at
		FROM monitor_alert
		WHERE alert_id = ?
	`, alertID).Scan(
		&item.ID, &item.AlertID, &item.RunID, &item.WorkflowID, &item.AgentID, &item.NodeID,
		&item.AlertType, &item.Severity, &item.Title, &item.Content, &item.Status,
		&item.TriggeredAt, &resolvedAt, &item.CreatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("monitor alert not found: %s", alertID)
		}
		return nil, fmt.Errorf("query monitor_alert by alert_id: %w", err)
	}
	if resolvedAt.Valid {
		t := resolvedAt.Time
		item.ResolvedAt = &t
	}
	return &item, nil
}

func (s *MySQLStorage) UpdateMonitorAlertStatus(ctx context.Context, alertID string, status string) error {
	status = strings.TrimSpace(strings.ToLower(status))
	if status != "open" && status != "acknowledged" && status != "resolved" {
		return fmt.Errorf("unsupported alert status: %s", status)
	}
	if status == "resolved" {
		_, err := s.db.ExecContext(ctx, `
			UPDATE monitor_alert
			SET status = ?, resolved_at = NOW()
			WHERE alert_id = ?
		`, status, alertID)
		if err != nil {
			return fmt.Errorf("update monitor_alert status: %w", err)
		}
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE monitor_alert
		SET status = ?, resolved_at = NULL
		WHERE alert_id = ?
	`, status, alertID)
	if err != nil {
		return fmt.Errorf("update monitor_alert status: %w", err)
	}
	return nil
}

func (s *MySQLStorage) BuildMonitorOverview(ctx context.Context, userID string, recentLimit int) (MonitorOverview, error) {
	if recentLimit <= 0 {
		recentLimit = 10
	}
	if recentLimit > 100 {
		recentLimit = 100
	}

	overview := MonitorOverview{}
	where := ""
	args := make([]any, 0, 2)
	if strings.TrimSpace(userID) != "" {
		where = " WHERE user_id = ?"
		args = append(args, userID)
	}

	countSQL := fmt.Sprintf(`
		SELECT
			COUNT(*) AS total_runs,
			SUM(CASE WHEN status = 'succeeded' THEN 1 ELSE 0 END) AS succeeded_runs,
			SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END) AS failed_runs,
			AVG(duration_ms) AS avg_duration
		FROM monitor_run%s
	`, where)
	var succeeded sql.NullInt64
	var failed sql.NullInt64
	var avgDuration sql.NullFloat64
	if err := s.db.QueryRowContext(ctx, countSQL, args...).Scan(&overview.TotalRuns, &succeeded, &failed, &avgDuration); err != nil {
		return overview, fmt.Errorf("build monitor overview runs: %w", err)
	}
	if succeeded.Valid {
		overview.SucceededRuns = succeeded.Int64
	}
	if failed.Valid {
		overview.FailedRuns = failed.Int64
	}
	if avgDuration.Valid {
		overview.AverageDurationMs = int64(avgDuration.Float64)
	}
	if overview.TotalRuns > 0 {
		overview.SuccessRate = float64(overview.SucceededRuns) / float64(overview.TotalRuns)
	}

	alertCountSQL := "SELECT COUNT(*) FROM monitor_alert"
	alertArgs := make([]any, 0, 1)
	if strings.TrimSpace(userID) != "" {
		alertCountSQL = "SELECT COUNT(*) FROM monitor_alert WHERE run_id IN (SELECT run_id FROM monitor_run WHERE user_id = ?)"
		alertArgs = append(alertArgs, userID)
	}
	if err := s.db.QueryRowContext(ctx, alertCountSQL, alertArgs...).Scan(&overview.AlertTotal); err != nil {
		return overview, fmt.Errorf("build monitor overview alerts: %w", err)
	}

	recentSQL := fmt.Sprintf(`
		SELECT id, run_id, workflow_id, user_id, IFNULL(source_agent_id,''), IFNULL(task_id,''), status,
		       started_at, finished_at, duration_ms, IFNULL(current_node_id,''), IFNULL(error_message,''),
		       alert_count, created_at, updated_at
		FROM monitor_run%s
		ORDER BY started_at DESC
		LIMIT ?
	`, where)
	recentArgs := append(args, recentLimit)
	rows, err := s.db.QueryContext(ctx, recentSQL, recentArgs...)
	if err != nil {
		return overview, fmt.Errorf("build monitor overview recent runs: %w", err)
	}
	defer rows.Close()

	overview.RecentRuns = make([]MonitorRun, 0, recentLimit)
	for rows.Next() {
		var item MonitorRun
		var finishedAt sql.NullTime
		if err := rows.Scan(
			&item.ID, &item.RunID, &item.WorkflowID, &item.UserID, &item.SourceAgentID, &item.TaskID,
			&item.Status, &item.StartedAt, &finishedAt, &item.DurationMs, &item.CurrentNodeID,
			&item.ErrorMessage, &item.AlertCount, &item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return overview, fmt.Errorf("scan recent monitor_run: %w", err)
		}
		if finishedAt.Valid {
			t := finishedAt.Time
			item.FinishedAt = &t
		}
		overview.RecentRuns = append(overview.RecentRuns, item)
	}

	return overview, nil
}

func (s *MySQLStorage) GetMonitorRunDetail(ctx context.Context, runID, userID string) (*MonitorRunDetail, error) {
	run, err := s.GetMonitorRun(ctx, runID, userID)
	if err != nil {
		return nil, err
	}

	detail := &MonitorRunDetail{
		Run:               *run,
		AlertCount:        int64(run.AlertCount),
		NodeStatusSummary: map[string]int64{},
		LatestError:       run.ErrorMessage,
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT e.status, COUNT(*)
		FROM monitor_event e
		JOIN (
			SELECT node_id, MAX(created_at) AS max_created_at
			FROM monitor_event
			WHERE run_id = ? AND node_id IS NOT NULL AND node_id <> ''
			GROUP BY node_id
		) latest ON latest.node_id = e.node_id AND latest.max_created_at = e.created_at
		WHERE e.run_id = ?
		GROUP BY e.status
	`, runID, runID)
	if err != nil {
		return nil, fmt.Errorf("query node status summary: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("scan node status summary: %w", err)
		}
		detail.NodeStatusSummary[status] = count
	}

	if strings.TrimSpace(detail.LatestError) == "" {
		var latestErr sql.NullString
		err = s.db.QueryRowContext(ctx, `
			SELECT error_message
			FROM monitor_event
			WHERE run_id = ? AND error_message IS NOT NULL AND error_message <> ''
			ORDER BY created_at DESC
			LIMIT 1
		`, runID).Scan(&latestErr)
		if err != nil && err != sql.ErrNoRows {
			return nil, fmt.Errorf("query latest error: %w", err)
		}
		if latestErr.Valid {
			detail.LatestError = latestErr.String
		}
	}

	return detail, nil
}

func nullString(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func nullableTime(v *time.Time) any {
	if v == nil {
		return nil
	}
	return *v
}
