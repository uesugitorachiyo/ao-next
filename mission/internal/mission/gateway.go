package mission

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"
)

type TelegramConfig struct {
	Schema       string            `json:"schema"`
	TokenEnv     string            `json:"token_env"`
	AllowedChats map[string]string `json:"allowed_chats"`
}

type TelegramUpdateReplay struct {
	Schema        string                  `json:"schema"`
	CorrelationID string                  `json:"correlation_id,omitempty"`
	Updates       []TelegramUpdateFixture `json:"updates"`
}

type TelegramUpdateFixture struct {
	UpdateID       int    `json:"update_id"`
	ChatID         string `json:"chat_id"`
	Text           string `json:"text"`
	ExpectedStatus string `json:"expected_status"`
	GeneratedAtUTC string `json:"generated_at_utc,omitempty"`
}

type GatewayReadback struct {
	Schema            string   `json:"schema"`
	Gateway           string   `json:"gateway"`
	Status            string   `json:"status"`
	AllowedChatCount  int      `json:"allowed_chat_count,omitempty"`
	Methods           []string `json:"methods,omitempty"`
	Message           string   `json:"message"`
	MutationAuthority bool     `json:"mutation_authority"`
	GeneratedAtUTC    string   `json:"generated_at_utc"`
}

var allowedTelegramCommands = map[string]bool{
	"/status":   true,
	"/next":     true,
	"/continue": true,
	"/pause":    true,
	"/resume":   true,
	"/stop":     true,
	"/approve":  true,
	"/deny":     true,
	"/where":    true,
	"/help":     true,
}

func LoadTelegramConfig(path string) (TelegramConfig, error) {
	var cfg TelegramConfig
	body, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := ValidatePublicSafeText(string(body)); err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		return cfg, err
	}
	if cfg.Schema == "" {
		cfg.Schema = "ao.mission.telegram-config.v0.1"
	}
	if cfg.AllowedChats == nil {
		cfg.AllowedChats = map[string]string{}
	}
	return cfg, nil
}

func TelegramConfigReadback(cfg TelegramConfig) GatewayReadback {
	return GatewayReadback{
		Schema:            "ao.mission.gateway-readback.v0.1",
		Gateway:           "telegram",
		Status:            "configured_intent_only",
		AllowedChatCount:  len(cfg.AllowedChats),
		Message:           "telegram gateway records intents and readbacks only; token values are read from environment and never printed",
		MutationAuthority: false,
		GeneratedAtUTC:    now(nil),
	}
}

func LoadTelegramCommandMatrix(path string) (TelegramCommandMatrix, error) {
	var matrix TelegramCommandMatrix
	body, err := os.ReadFile(path)
	if err != nil {
		return matrix, err
	}
	if err := ValidatePublicSafeText(string(body)); err != nil {
		return matrix, err
	}
	if err := json.Unmarshal(body, &matrix); err != nil {
		return matrix, err
	}
	return matrix, nil
}

func ReplayTelegramCommandMatrix(path string, allowlist map[string]string) (GatewayReplayReadback, error) {
	matrix, err := LoadTelegramCommandMatrix(path)
	if err != nil {
		return GatewayReplayReadback{}, err
	}
	readback := GatewayReplayReadback{
		Schema:            "ao.mission.telegram-replay-readback.v0.1",
		Gateway:           "telegram",
		Status:            "ready",
		Results:           []GatewayReplayResult{},
		MutationAuthority: false,
		ExecutesWork:      false,
		ApprovesWork:      false,
		GeneratedAtUTC:    now(nil),
	}
	for _, tc := range matrix.Commands {
		chatID := chatIDForRole(tc.Role)
		rb := HandleTelegramCommand(TelegramCommand{ChatID: chatID, Command: tc.Command, Role: tc.Role}, allowlist)
		result := GatewayReplayResult{Command: tc.Command, ExpectedStatus: tc.ExpectedStatus, ActualStatus: rb.Status, MutationAuthority: rb.MutationAuthority}
		readback.Results = append(readback.Results, result)
		if rb.MutationAuthority {
			readback.MutationAuthority = true
		}
		countGatewayStatus(&readback, rb.Status)
		if tc.ExpectedStatus != "" && rb.Status != tc.ExpectedStatus {
			readback.Status = "blocked"
		}
	}
	readback.Total = len(readback.Results)
	return readback, nil
}

func ReplayTelegramUpdates(path string, allowlist map[string]string) (GatewayReplayReadback, error) {
	var fixture TelegramUpdateReplay
	body, err := os.ReadFile(path)
	if err != nil {
		return GatewayReplayReadback{}, err
	}
	if err := ValidatePublicSafeText(string(body)); err != nil {
		return GatewayReplayReadback{}, err
	}
	if err := json.Unmarshal(body, &fixture); err != nil {
		return GatewayReplayReadback{}, err
	}
	readback := GatewayReplayReadback{
		Schema:            "ao.mission.telegram-update-replay-readback.v0.1",
		Gateway:           "telegram",
		Status:            "ready",
		CorrelationID:     strings.TrimSpace(fixture.CorrelationID),
		Results:           []GatewayReplayResult{},
		MutationAuthority: false,
		ExecutesWork:      false,
		ApprovesWork:      false,
		GeneratedAtUTC:    now(nil),
	}
	seenUpdates := map[int]bool{}
	for _, update := range fixture.Updates {
		if update.UpdateID != 0 {
			if seenUpdates[update.UpdateID] {
				readback.Duplicates++
				result := GatewayReplayResult{Command: update.Text, ExpectedStatus: update.ExpectedStatus, ActualStatus: "duplicate_ignored", MutationAuthority: false}
				readback.Results = append(readback.Results, result)
				if update.ExpectedStatus != "" && result.ActualStatus != update.ExpectedStatus {
					readback.Status = "blocked"
				}
				continue
			}
			seenUpdates[update.UpdateID] = true
		}
		role := allowlist[update.ChatID]
		rb := HandleTelegramCommand(TelegramCommand{ChatID: update.ChatID, Command: update.Text, Role: role}, allowlist)
		result := GatewayReplayResult{Command: update.Text, ExpectedStatus: update.ExpectedStatus, ActualStatus: rb.Status, MutationAuthority: rb.MutationAuthority}
		readback.Results = append(readback.Results, result)
		countReplayFreshness(&readback, update.GeneratedAtUTC)
		if rb.MutationAuthority {
			readback.MutationAuthority = true
		}
		countGatewayStatus(&readback, rb.Status)
		if update.ExpectedStatus != "" && rb.Status != update.ExpectedStatus {
			readback.Status = "blocked"
		}
	}
	readback.Total = len(readback.Results)
	finalizeReplayFreshness(&readback)
	return readback, nil
}

func ReplayTelegramWebhookFixture(path string, allowlist map[string]string) (GatewayReplayReadback, error) {
	readback, err := ReplayTelegramUpdates(path, allowlist)
	if err != nil {
		return GatewayReplayReadback{}, err
	}
	readback.Schema = "ao.mission.telegram-webhook-replay-readback.v0.1"
	readback.Gateway = "telegram_webhook"
	return readback, nil
}

func ReplayA2AHTTPFixture(path string) (GatewayReplayReadback, error) {
	var fixture struct {
		Schema        string `json:"schema"`
		CorrelationID string `json:"correlation_id,omitempty"`
		Requests      []struct {
			JSONRPC        string         `json:"jsonrpc"`
			ID             string         `json:"id"`
			Method         string         `json:"method"`
			Params         map[string]any `json:"params"`
			ExpectedStatus string         `json:"expected_status"`
		} `json:"requests"`
		MutationAuthority bool `json:"mutation_authority"`
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return GatewayReplayReadback{}, err
	}
	if err := ValidatePublicSafeText(string(body)); err != nil {
		return GatewayReplayReadback{}, err
	}
	if err := json.Unmarshal(body, &fixture); err != nil {
		return GatewayReplayReadback{}, err
	}
	server := httptest.NewServer(A2AHandler())
	defer server.Close()
	readback := GatewayReplayReadback{
		Schema:            "ao.mission.a2a-http-replay-readback.v0.1",
		Gateway:           "a2a",
		Status:            "ready",
		CorrelationID:     strings.TrimSpace(fixture.CorrelationID),
		Results:           []GatewayReplayResult{},
		MutationAuthority: false,
		ExecutesWork:      false,
		ApprovesWork:      false,
		GeneratedAtUTC:    now(nil),
	}
	for _, req := range fixture.Requests {
		payload, _ := json.Marshal(req)
		resp, err := http.Post(server.URL+"/", "application/json", bytes.NewReader(payload))
		if err != nil {
			return GatewayReplayReadback{}, err
		}
		var rpc A2AJSONRPCResponse
		err = json.NewDecoder(resp.Body).Decode(&rpc)
		_ = resp.Body.Close()
		if err != nil {
			return GatewayReplayReadback{}, err
		}
		responseID := fmt.Sprint(rpc.ID)
		result := GatewayReplayResult{RequestID: req.ID, ResponseID: responseID, Method: req.Method, ExpectedStatus: req.ExpectedStatus, ActualStatus: rpc.Result.Status, MutationAuthority: rpc.Result.MutationAuthority}
		readback.Results = append(readback.Results, result)
		if rpc.Result.MutationAuthority {
			readback.MutationAuthority = true
		}
		countGatewayStatus(&readback, rpc.Result.Status)
		if req.ID != "" && responseID != req.ID {
			readback.Status = "blocked"
		}
		if req.ExpectedStatus != "" && rpc.Result.Status != req.ExpectedStatus {
			readback.Status = "blocked"
		}
	}
	if fixture.MutationAuthority {
		return GatewayReplayReadback{}, fmt.Errorf("A2A HTTP fixture must not claim mutation authority")
	}
	readback.Total = len(readback.Results)
	return readback, nil
}

func BuildGatewayIntentLedger(missionID string, readbacks ...GatewayReplayReadback) GatewayIntentLedger {
	ledger := GatewayIntentLedger{
		Schema:            "ao.mission.gateway-intent-ledger.v0.1",
		MissionID:         missionID,
		Status:            "ready",
		Intents:           []GatewayIntentRecord{},
		MutationAuthority: false,
		ExecutesWork:      false,
		ApprovesWork:      false,
		GeneratedAtUTC:    now(nil),
	}
	for _, readback := range readbacks {
		if readback.Status == "blocked" {
			ledger.Status = "blocked"
		}
		for _, result := range readback.Results {
			record := GatewayIntentRecord{
				Schema:            "ao.mission.gateway-intent.v0.1",
				MissionID:         missionID,
				Gateway:           readback.Gateway,
				Command:           result.Command,
				Method:            result.Method,
				Status:            result.ActualStatus,
				ExpectedStatus:    result.ExpectedStatus,
				MutationAuthority: false,
				ExecutesWork:      false,
				ApprovesWork:      false,
				GeneratedAtUTC:    now(nil),
			}
			ledger.Intents = append(ledger.Intents, record)
			switch result.ActualStatus {
			case "intent_recorded":
				ledger.IntentRecorded++
			case "denied":
				ledger.Denied++
			case "invalid":
				ledger.Invalid++
			}
		}
	}
	ledger.Total = len(ledger.Intents)
	return ledger
}

func ReplayA2ATaskLifecycle(path string) (A2ATaskLifecycleReadback, error) {
	var fixture struct {
		Schema string    `json:"schema"`
		Tasks  []A2ATask `json:"tasks"`
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return A2ATaskLifecycleReadback{}, err
	}
	if err := ValidatePublicSafeText(string(body)); err != nil {
		return A2ATaskLifecycleReadback{}, err
	}
	if err := json.Unmarshal(body, &fixture); err != nil {
		return A2ATaskLifecycleReadback{}, err
	}
	readback := A2ATaskLifecycleReadback{
		Schema:            "ao.mission.a2a-task-lifecycle-readback.v0.1",
		Status:            "ready",
		Tasks:             append([]A2ATask(nil), fixture.Tasks...),
		MutationAuthority: false,
		ExecutesWork:      false,
		ApprovesWork:      false,
		GeneratedAtUTC:    now(nil),
	}
	for i, task := range fixture.Tasks {
		if task.Schema != A2ATaskSchema {
			return A2ATaskLifecycleReadback{}, fmt.Errorf("A2A lifecycle task %d schema must be %s", i, A2ATaskSchema)
		}
		if task.MutationAuthority {
			return A2ATaskLifecycleReadback{}, fmt.Errorf("A2A lifecycle task %d must not claim mutation authority", i)
		}
		switch task.Status {
		case "intent_recorded":
			readback.IntentRecorded++
		case "cancel_requested":
			readback.CancelRequested++
		case "cancelled":
			readback.Cancelled++
		case "resume_requested":
			readback.ResumeRequested++
		case "resumed":
			readback.Resumed++
		case "artifact_readback":
			readback.ArtifactReadbacks++
		default:
			return A2ATaskLifecycleReadback{}, fmt.Errorf("A2A lifecycle task %d has unsupported status %q", i, task.Status)
		}
	}
	readback.Total = len(fixture.Tasks)
	return readback, nil
}

func chatIDForRole(role string) string {
	switch role {
	case "admin":
		return "1001"
	case "none":
		return "9999"
	default:
		return "1002"
	}
}

func countGatewayStatus(readback *GatewayReplayReadback, status string) {
	switch status {
	case "intent_recorded":
		readback.IntentRecorded++
	case "denied":
		readback.Denied++
	case "invalid":
		readback.Invalid++
	}
}

func countReplayFreshness(readback *GatewayReplayReadback, generatedAtUTC string) {
	if strings.TrimSpace(generatedAtUTC) == "" {
		readback.UnknownFreshness++
		return
	}
	generated, err := time.Parse(time.RFC3339, generatedAtUTC)
	if err != nil {
		readback.UnknownFreshness++
		return
	}
	if generated.Before(time.Now().UTC().Add(-24 * time.Hour)) {
		readback.Stale++
		return
	}
	readback.Fresh++
}

func finalizeReplayFreshness(readback *GatewayReplayReadback) {
	if readback.Fresh == 0 && readback.Stale == 0 && readback.UnknownFreshness == 0 {
		return
	}
	switch {
	case readback.Stale > 0:
		readback.FreshnessStatus = "stale"
	case readback.UnknownFreshness > 0:
		readback.FreshnessStatus = "unknown"
	default:
		readback.FreshnessStatus = "fresh"
	}
}

func HandleTelegramCommand(cmd TelegramCommand, allowlist map[string]string) TelegramReadback {
	if cmd.Schema == "" {
		cmd.Schema = TelegramCommandSchema
	}
	role, ok := allowlist[cmd.ChatID]
	if !ok {
		return TelegramReadback{Schema: TelegramReadbackSchema, Status: "denied", Message: "chat id is not allowlisted", MutationAuthority: false}
	}
	if role != "admin" && (cmd.Command == "/approve" || cmd.Command == "/continue") {
		return TelegramReadback{Schema: TelegramReadbackSchema, Status: "denied", Message: "role cannot request this intent", MutationAuthority: false}
	}
	if !strings.HasPrefix(cmd.Command, "/") {
		return TelegramReadback{Schema: TelegramReadbackSchema, Status: "invalid", Message: "telegram command must start with slash", MutationAuthority: false}
	}
	if !allowedTelegramCommands[cmd.Command] {
		return TelegramReadback{Schema: TelegramReadbackSchema, Status: "invalid", Message: "telegram command is not supported", MutationAuthority: false}
	}
	return TelegramReadback{Schema: TelegramReadbackSchema, Status: "intent_recorded", Message: "telegram gateway records intents and readbacks only", MutationAuthority: false}
}

func AgentCard() A2AAgentCard {
	return A2AAgentCard{
		Schema:          A2AAgentCardSchema,
		Name:            "ao-mission",
		ProtocolVersion: "a2a-local-fixture-v0.1",
		Description:     "AO Mission local gateway for intent and readback requests only",
		Endpoint:        "/",
		Methods:         []string{"mission.start", "mission.status", "mission.next", "mission.continue", "mission.pause", "mission.resume", "mission.cancel", "mission.artifacts", "mission.governance_snapshot"},
		Capabilities:    []string{"streaming=false", "push_notifications=false", "mutation_authority=false"},
		CapabilitiesDetail: map[string]bool{
			"streaming":                false,
			"push_notifications":       false,
			"state_transition_history": true,
			"artifact_readbacks":       true,
		},
		Skills: []A2AAgentSkill{
			{ID: "mission-readback", Name: "Mission readback", Description: "Return mission status, route, next action, and governance snapshot readbacks.", Tags: []string{"mission", "readback", "intent-only"}},
			{ID: "mission-continuation-intent", Name: "Mission continuation intent", Description: "Record a continuation request without granting execution authority.", Tags: []string{"mission", "continuation", "intent-only"}},
			{ID: "mission-artifact-readback", Name: "Mission artifact readback", Description: "Return artifact references and digest-bound readback pointers.", Tags: []string{"artifacts", "readback", "no-mutation"}},
		},
		MutationAuthority: false,
	}
}
func A2ATaskFor(method string) A2ATask {
	return A2ATask{Schema: A2ATaskSchema, TaskID: "task-" + strings.ReplaceAll(method, ".", "-"), Method: method, Status: "intent_recorded", MutationAuthority: false}
}
func A2ATaskForParams(method string, params map[string]any) A2ATask {
	task := A2ATaskFor(method)
	if !stringSliceContains(AgentCard().Methods, method) {
		task.Status = "invalid"
		return task
	}
	switch method {
	case "mission.start":
		if strings.TrimSpace(stringParam(params, "objective")) == "" {
			task.Status = "invalid"
		}
	case "mission.status", "mission.next", "mission.continue", "mission.pause", "mission.resume", "mission.cancel", "mission.artifacts", "mission.governance_snapshot":
		if strings.TrimSpace(stringParam(params, "mission_id")) == "" {
			task.Status = "invalid"
		}
	}
	if task.Status == "intent_recorded" && method == "mission.artifacts" {
		missionID := strings.TrimSpace(stringParam(params, "mission_id"))
		task.ArtifactRefs = []ArtifactRef{{
			Schema: ArtifactRefSchema,
			Ref:    "missions/" + missionID + "/artifact-manifest.json",
			Kind:   "mission_artifact_readback",
		}}
	}
	return task
}

func A2AHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/agent-card.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(AgentCard())
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(GatewayReadback{Schema: "ao.mission.gateway-readback.v0.1", Gateway: "a2a", Status: "ready", Methods: AgentCard().Methods, Message: "A2A local gateway is intent/readback only", MutationAuthority: false, GeneratedAtUTC: now(nil)})
			return
		}
		var req struct {
			JSONRPC string         `json:"jsonrpc"`
			ID      any            `json:"id"`
			Method  string         `json:"method"`
			Params  map[string]any `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "" {
			req.Method = "mission.status"
		}
		w.Header().Set("Content-Type", "application/json")
		task := A2ATaskForParams(req.Method, req.Params)
		if req.JSONRPC == "2.0" || req.ID != nil {
			_ = json.NewEncoder(w).Encode(A2AJSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: task})
			return
		}
		_ = json.NewEncoder(w).Encode(task)
	})
	return mux
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func stringParam(params map[string]any, key string) string {
	if params == nil {
		return ""
	}
	value, _ := params[key].(string)
	return value
}
