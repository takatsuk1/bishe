package orchestrator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegration_FullWorkflowAPIFlow(t *testing.T) {
	api := NewAgentWorkflowAPI(nil)
	handler := api.Handler()

	t.Run("get all agents workflow", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/agent-workflows", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)

		var response AgentWorkflowsResponse
		err := json.Unmarshal(rec.Body.Bytes(), &response)
		require.NoError(t, err)

		assert.Equal(t, "v1", response.APIVersion)
		assert.NotEmpty(t, response.Timestamp)
		assert.Len(t, response.Agents, 4)

		for _, agent := range response.Agents {
			assert.NotEmpty(t, agent.ID)
			assert.NotEmpty(t, agent.Name)
			assert.NotEmpty(t, agent.Type)
			assert.NotEmpty(t, agent.Description)
			assert.NotEmpty(t, agent.Version)
			assert.NotEmpty(t, agent.Nodes)
			assert.NotEmpty(t, agent.Edges)
			assert.NotEmpty(t, agent.Configuration.InputSchema)
			assert.NotEmpty(t, agent.Configuration.OutputSchema)
		}
	})

	t.Run("get single agent workflow", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/agent-workflows/host", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)

		var response struct {
			APIVersion string              `json:"apiVersion"`
			Timestamp  string              `json:"timestamp"`
			Agent      AgentWorkflowDetail `json:"agent"`
		}
		err := json.Unmarshal(rec.Body.Bytes(), &response)
		require.NoError(t, err)

		assert.Equal(t, "v1", response.APIVersion)
		assert.Equal(t, "host", response.Agent.ID)
		assert.Equal(t, "Host Agent", response.Agent.Name)
	})

	t.Run("verify nodes and edges consistency", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/agent-workflows/deepresearch", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)

		var response struct {
			Agent AgentWorkflowDetail `json:"agent"`
		}
		err := json.Unmarshal(rec.Body.Bytes(), &response)
		require.NoError(t, err)

		nodeIDs := make(map[string]bool)
		for _, node := range response.Agent.Nodes {
			nodeIDs[node.ID] = true
		}

		for _, edge := range response.Agent.Edges {
			assert.True(t, nodeIDs[edge.From], "edge from node %s should exist", edge.From)
			assert.True(t, nodeIDs[edge.To], "edge to node %s should exist", edge.To)
		}

		assert.Equal(t, response.Agent.ExecutionOrder.StartNodeID, response.Agent.Nodes[0].ID)
	})
}

func TestIntegration_AuthenticationFlow(t *testing.T) {
	authProvider := &mockAuthProvider{validToken: "test-api-key"}
	api := NewAgentWorkflowAPI(authProvider)
	handler := api.Handler()

	t.Run("request without token should succeed (optional auth)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/agent-workflows", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("request with valid token should succeed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/agent-workflows", nil)
		req.Header.Set("Authorization", "Bearer test-api-key")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("request with invalid token should fail", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/agent-workflows", nil)
		req.Header.Set("Authorization", "Bearer invalid-key")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("token in query parameter", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/agent-workflows?token=test-api-key", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

func TestIntegration_RateLimiting(t *testing.T) {
	api := NewAgentWorkflowAPI(nil)
	api.rateLimiter = NewRateLimiter(5, time.Minute)
	handler := api.Handler()

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/agent-workflows", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/agent-workflows", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	t.Run("different client should not be rate limited", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/agent-workflows", nil)
		req.RemoteAddr = "10.0.0.2:1234"
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

func TestIntegration_Caching(t *testing.T) {
	api := NewAgentWorkflowAPI(nil)
	api.cache.ttl = 200 * time.Millisecond
	handler := api.Handler()

	req1 := httptest.NewRequest(http.MethodGet, "/v1/agent-workflows", nil)
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)

	require.Equal(t, http.StatusOK, rec1.Code)

	var response1 AgentWorkflowsResponse
	err := json.Unmarshal(rec1.Body.Bytes(), &response1)
	require.NoError(t, err)

	req2 := httptest.NewRequest(http.MethodGet, "/v1/agent-workflows", nil)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	require.Equal(t, http.StatusOK, rec2.Code)

	var response2 AgentWorkflowsResponse
	err = json.Unmarshal(rec2.Body.Bytes(), &response2)
	require.NoError(t, err)

	assert.Equal(t, response1.Timestamp, response2.Timestamp, "cached response should have same timestamp")

	cachedData := api.cache.Get()
	require.NotNil(t, cachedData, "cache should have data after first request")

	time.Sleep(250 * time.Millisecond)

	cachedDataAfterExpiry := api.cache.Get()
	assert.Nil(t, cachedDataAfterExpiry, "cache should be expired after ttl")

	req3 := httptest.NewRequest(http.MethodGet, "/v1/agent-workflows", nil)
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)

	require.Equal(t, http.StatusOK, rec3.Code)

	newCachedData := api.cache.Get()
	require.NotNil(t, newCachedData, "cache should have new data after expiry and new request")
}

func TestIntegration_ResponseTime(t *testing.T) {
	api := NewAgentWorkflowAPI(nil)
	handler := api.Handler()

	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/agent-workflows", nil)
		rec := httptest.NewRecorder()

		start := time.Now()
		handler.ServeHTTP(rec, req)
		elapsed := time.Since(start)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Less(t, elapsed.Milliseconds(), int64(200), "response time should be under 200ms")
	}
}

func TestIntegration_AllAgentsDataIntegrity(t *testing.T) {
	api := NewAgentWorkflowAPI(nil)
	handler := api.Handler()

	req := httptest.NewRequest(http.MethodGet, "/v1/agent-workflows", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var response AgentWorkflowsResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	require.NoError(t, err)

	for _, agent := range response.Agents {
		t.Run(agent.ID+"_data_integrity", func(t *testing.T) {
			assert.NotEmpty(t, agent.ID, "ID should not be empty")
			assert.NotEmpty(t, agent.Name, "Name should not be empty")
			assert.NotEmpty(t, agent.Type, "Type should not be empty")
			assert.NotEmpty(t, agent.Version, "Version should not be empty")
			assert.NotEmpty(t, agent.Description, "Description should not be empty")

			assert.Greater(t, agent.Configuration.Timeout, 0, "Timeout should be positive")
			assert.Greater(t, agent.Configuration.RetryPolicy.MaxAttempts, 0, "MaxAttempts should be positive")

			assert.NotEmpty(t, agent.Nodes, "Nodes should not be empty")
			assert.NotEmpty(t, agent.Edges, "Edges should not be empty")

			hasStart := false
			hasEnd := false
			for _, node := range agent.Nodes {
				if node.Type == "start" {
					hasStart = true
				}
				if node.Type == "end" {
					hasEnd = true
				}
			}
			assert.True(t, hasStart, "should have a start node")
			assert.True(t, hasEnd, "should have an end node")

			assert.Equal(t, agent.ExecutionOrder.StartNodeID, "start", "StartNodeID should be 'start'")
		})
	}
}

func TestIntegration_ErrorHandling(t *testing.T) {
	api := NewAgentWorkflowAPI(nil)
	handler := api.Handler()

	t.Run("invalid method", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/agent-workflows", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)

		var response ErrorResponse
		err := json.Unmarshal(rec.Body.Bytes(), &response)
		require.NoError(t, err)
		assert.Equal(t, ErrCodeInvalidRequest, response.Error.Code)
	})

	t.Run("agent not found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/agent-workflows/nonexistent", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code)

		var response ErrorResponse
		err := json.Unmarshal(rec.Body.Bytes(), &response)
		require.NoError(t, err)
		assert.Equal(t, ErrCodeAgentNotFound, response.Error.Code)
		assert.Contains(t, response.Error.Message, "not found")
	})
}
