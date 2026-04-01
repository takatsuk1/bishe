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

type mockAuthProvider struct {
	validToken string
}

func (m *mockAuthProvider) ValidateToken(token string) (bool, error) {
	return token == m.validToken, nil
}

func TestRateLimiter_Allow(t *testing.T) {
	rl := NewRateLimiter(3, time.Minute)

	assert.True(t, rl.Allow("client1"))
	assert.True(t, rl.Allow("client1"))
	assert.True(t, rl.Allow("client1"))
	assert.False(t, rl.Allow("client1"))

	assert.True(t, rl.Allow("client2"))
	assert.True(t, rl.Allow("client2"))
	assert.True(t, rl.Allow("client2"))
	assert.False(t, rl.Allow("client2"))
}

func TestRateLimiter_WindowExpiry(t *testing.T) {
	rl := NewRateLimiter(1, 100*time.Millisecond)

	assert.True(t, rl.Allow("client1"))
	assert.False(t, rl.Allow("client1"))

	time.Sleep(150 * time.Millisecond)

	assert.True(t, rl.Allow("client1"))
}

func TestAgentWorkflowCache(t *testing.T) {
	cache := &AgentWorkflowCache{
		ttl: 100 * time.Millisecond,
	}

	assert.Nil(t, cache.Get())

	data := &AgentWorkflowsResponse{
		APIVersion: "v1",
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
	}
	cache.Set(data)

	cached := cache.Get()
	require.NotNil(t, cached)
	assert.Equal(t, "v1", cached.APIVersion)

	time.Sleep(150 * time.Millisecond)

	assert.Nil(t, cache.Get())
}

func TestAgentWorkflowAPI_HandleAgentWorkflows_Success(t *testing.T) {
	api := NewAgentWorkflowAPI(nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/agent-workflows", nil)
	rec := httptest.NewRecorder()

	api.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var response AgentWorkflowsResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.Equal(t, "v1", response.APIVersion)
	assert.Len(t, response.Agents, 4)

	agentIDs := make([]string, len(response.Agents))
	for i, agent := range response.Agents {
		agentIDs[i] = agent.ID
	}
	assert.ElementsMatch(t, []string{"host", "deepresearch", "urlreader", "lbshelper"}, agentIDs)
}

func TestAgentWorkflowAPI_HandleAgentWorkflows_MethodNotAllowed(t *testing.T) {
	api := NewAgentWorkflowAPI(nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/agent-workflows", nil)
	rec := httptest.NewRecorder()

	api.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestAgentWorkflowAPI_HandleAgentWorkflows_WithAuth(t *testing.T) {
	authProvider := &mockAuthProvider{validToken: "valid-token"}
	api := NewAgentWorkflowAPI(authProvider)

	t.Run("valid token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/agent-workflows", nil)
		req.Header.Set("Authorization", "Bearer valid-token")
		rec := httptest.NewRecorder()

		api.Handler().ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("invalid token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/agent-workflows", nil)
		req.Header.Set("Authorization", "Bearer invalid-token")
		rec := httptest.NewRecorder()

		api.Handler().ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnauthorized, rec.Code)

		var response ErrorResponse
		err := json.Unmarshal(rec.Body.Bytes(), &response)
		require.NoError(t, err)
		assert.Equal(t, ErrCodeUnauthorized, response.Error.Code)
	})
}

func TestAgentWorkflowAPI_HandleAgentWorkflowByID_Success(t *testing.T) {
	api := NewAgentWorkflowAPI(nil)

	tests := []struct {
		agentID   string
		name      string
		nodeCount int
	}{
		{"host", "Host Agent", 7},
		{"deepresearch", "Deep Research Agent", 5},
		{"urlreader", "URL Reader Agent", 6},
		{"lbshelper", "LBS Helper Agent", 6},
	}

	for _, tt := range tests {
		t.Run(tt.agentID, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/agent-workflows/"+tt.agentID, nil)
			rec := httptest.NewRecorder()

			api.Handler().ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)

			var response struct {
				APIVersion string              `json:"apiVersion"`
				Agent      AgentWorkflowDetail `json:"agent"`
			}
			err := json.Unmarshal(rec.Body.Bytes(), &response)
			require.NoError(t, err)

			assert.Equal(t, "v1", response.APIVersion)
			assert.Equal(t, tt.agentID, response.Agent.ID)
			assert.Equal(t, tt.name, response.Agent.Name)
			assert.Len(t, response.Agent.Nodes, tt.nodeCount)
		})
	}
}

func TestAgentWorkflowAPI_HandleAgentWorkflowByID_NotFound(t *testing.T) {
	api := NewAgentWorkflowAPI(nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/agent-workflows/unknown", nil)
	rec := httptest.NewRecorder()

	api.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)

	var response ErrorResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	require.NoError(t, err)
	assert.Equal(t, ErrCodeAgentNotFound, response.Error.Code)
}

func TestAgentWorkflowAPI_RateLimit(t *testing.T) {
	api := NewAgentWorkflowAPI(nil)
	api.rateLimiter = NewRateLimiter(2, time.Minute)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/agent-workflows", nil)
		req.RemoteAddr = "192.168.1.1:1234"
		rec := httptest.NewRecorder()

		api.Handler().ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/agent-workflows", nil)
	req.RemoteAddr = "192.168.1.1:1234"
	rec := httptest.NewRecorder()

	api.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	var response ErrorResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	require.NoError(t, err)
	assert.Equal(t, ErrCodeRateLimitExceeded, response.Error.Code)
}

func TestBuildHostAgentWorkflow(t *testing.T) {
	workflow := buildHostAgentWorkflow()

	assert.Equal(t, "host", workflow.ID)
	assert.Equal(t, "Host Agent", workflow.Name)
	assert.Equal(t, "orchestrator", workflow.Type)
	assert.Len(t, workflow.Dependencies, 3)
	assert.Len(t, workflow.Nodes, 7)
	assert.Len(t, workflow.Edges, 7)
	assert.Equal(t, "start", workflow.ExecutionOrder.StartNodeID)
}

func TestBuildDeepResearchAgentWorkflow(t *testing.T) {
	workflow := buildDeepResearchAgentWorkflow()

	assert.Equal(t, "deepresearch", workflow.ID)
	assert.Equal(t, "Deep Research Agent", workflow.Name)
	assert.Equal(t, "worker", workflow.Type)
	assert.Len(t, workflow.Dependencies, 1)
	assert.Len(t, workflow.Nodes, 5)
	assert.Len(t, workflow.Edges, 4)
}

func TestBuildURLReaderAgentWorkflow(t *testing.T) {
	workflow := buildURLReaderAgentWorkflow()

	assert.Equal(t, "urlreader", workflow.ID)
	assert.Equal(t, "URL Reader Agent", workflow.Name)
	assert.Equal(t, "worker", workflow.Type)
	assert.Len(t, workflow.Nodes, 6)
	assert.Len(t, workflow.Edges, 5)
}

func TestBuildLBSHelperAgentWorkflow(t *testing.T) {
	workflow := buildLBSHelperAgentWorkflow()

	assert.Equal(t, "lbshelper", workflow.ID)
	assert.Equal(t, "LBS Helper Agent", workflow.Name)
	assert.Equal(t, "worker", workflow.Type)
	assert.Len(t, workflow.Nodes, 6)
	assert.Len(t, workflow.Edges, 6)
	assert.NotNil(t, workflow.ExecutionOrder.Parallel)
}

func TestAgentWorkflowDetail_Configuration(t *testing.T) {
	workflow := buildHostAgentWorkflow()

	assert.Equal(t, 600, workflow.Configuration.Timeout)
	assert.Equal(t, 3, workflow.Configuration.RetryPolicy.MaxAttempts)
	assert.NotNil(t, workflow.Configuration.InputSchema)
	assert.NotNil(t, workflow.Configuration.OutputSchema)
	assert.Len(t, workflow.Configuration.EnvironmentVars, 2)
}

func TestAgentWorkflowDetail_Metadata(t *testing.T) {
	workflow := buildHostAgentWorkflow()

	assert.NotEmpty(t, workflow.Metadata.CreatedAt)
	assert.NotEmpty(t, workflow.Metadata.UpdatedAt)
	assert.Equal(t, "system", workflow.Metadata.Author)
	assert.NotEmpty(t, workflow.Metadata.Tags)
	assert.NotEmpty(t, workflow.Metadata.Labels)
}

func TestAPIVersion(t *testing.T) {
	api := NewAgentWorkflowAPI(nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/agent-workflows", nil)
	rec := httptest.NewRecorder()

	api.Handler().ServeHTTP(rec, req)

	assert.Equal(t, "v1", rec.Header().Get("X-API-Version"))
}
