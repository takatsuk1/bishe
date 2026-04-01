package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const mysqlMCPServerName = "mysql-mcp"
const mysqlMCPVersion = "1.0.0"

func main() {
	dsn := getFlagValue("--dsn", os.Args)
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		panic("dsn is required")
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		panic(err)
	}

	srv := server.NewMCPServer(mysqlMCPServerName, mysqlMCPVersion)
	srv.AddTool(mcp.Tool{
		Name:        "mysql_exec",
		Description: "Execute SQL against MySQL",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"sql": map[string]any{
					"type":        "string",
					"description": "SQL statement to execute",
				},
			},
			Required: []string{"sql"},
		},
	}, func(arguments map[string]interface{}) (*mcp.CallToolResult, error) {
		return handleMySQLExec(db, arguments)
	})

	if err := server.ServeStdio(srv); err != nil {
		panic(err)
	}
}

func getFlagValue(name string, args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func handleMySQLExec(db *sql.DB, arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	sqlText, _ := arguments["sql"].(string)
	sqlText = strings.TrimSpace(sqlText)
	if sqlText == "" {
		return mcp.NewToolResultError("sql is required"), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	payload, err := executeSQL(ctx, db, sqlText)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	return mcp.NewToolResultText(string(data)), nil
}

func executeSQL(ctx context.Context, db *sql.DB, sqlText string) (map[string]any, error) {
	rows, err := db.QueryContext(ctx, sqlText)
	if err != nil {
		res, execErr := db.ExecContext(ctx, sqlText)
		if execErr != nil {
			return nil, execErr
		}
		rowsAffected, _ := res.RowsAffected()
		lastInsertID, _ := res.LastInsertId()
		return map[string]any{
			"type":           "exec",
			"rows_affected":  rowsAffected,
			"last_insert_id": lastInsertID,
		}, nil
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	resultRows := make([]map[string]any, 0)
	for rows.Next() {
		values := make([]any, len(cols))
		valuePtrs := make([]any, len(cols))
		for i := range values {
			valuePtrs[i] = &values[i]
		}
		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			row[col] = normalizeSQLValue(values[i])
		}
		resultRows = append(resultRows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return map[string]any{
		"type":    "rows",
		"columns": cols,
		"rows":    resultRows,
	}, nil
}

func normalizeSQLValue(value any) any {
	switch v := value.(type) {
	case nil:
		return nil
	case []byte:
		return string(v)
	case time.Time:
		return v.Format(time.RFC3339)
	default:
		return v
	}
}
