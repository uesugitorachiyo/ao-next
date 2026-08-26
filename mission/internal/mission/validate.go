package mission

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type ContractValidation struct {
	Schema    string   `json:"schema"`
	Path      string   `json:"path"`
	Status    string   `json:"status"`
	Contract  string   `json:"contract"`
	Blockers  []string `json:"blockers"`
	ReadOnly  bool     `json:"read_only"`
	Executes  bool     `json:"executes_work"`
	Approves  bool     `json:"approves_work"`
	Mutates   bool     `json:"mutates_repositories"`
	Generated string   `json:"generated_at_utc"`
}

var (
	objectiveWorkflowMissionIDPattern = regexp.MustCompile(`^mission-[0-9a-f]{16}$`)
	objectiveWorkflowTimestampPattern = regexp.MustCompile(
		`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$`,
	)
)

func validateNoDuplicateJSONKeys(body []byte) error {
	if _, err := decodeExactJSON(body); err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return fmt.Errorf("duplicate JSON key: %w", err)
		}
		return err
	}
	return nil
}

func ValidateContractFile(path string) (ContractValidation, error) {
	result := ContractValidation{
		Schema:    "ao.mission.contract-validation.v0.1",
		Path:      path,
		Status:    "ready",
		Blockers:  []string{},
		ReadOnly:  true,
		Generated: now(nil),
	}
	body, err := os.ReadFile(path)
	if err != nil {
		result.Status = "blocked"
		result.Blockers = append(result.Blockers, err.Error())
		return result, err
	}
	if err := ValidatePublicSafeText(string(body)); err != nil {
		result.Status = "blocked"
		result.Blockers = append(result.Blockers, err.Error())
		return result, err
	}
	if err := validateNoDuplicateJSONKeys(body); err != nil {
		result.Status = "blocked"
		result.Blockers = append(result.Blockers, err.Error())
		return result, err
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		result.Status = "blocked"
		result.Blockers = append(result.Blockers, "invalid JSON")
		return result, err
	}
	schema, err := contractDiscriminator(doc)
	if err != nil {
		result.Status = "blocked"
		result.Blockers = append(result.Blockers, err.Error())
		return result, err
	}
	result.Contract = schema
	required, propertyTypes := contractRules(schema)
	for _, field := range required {
		if _, ok := doc[field]; !ok {
			result.Status = "blocked"
			result.Blockers = append(result.Blockers, field+" is required")
		}
	}
	for field, want := range propertyTypes {
		value, ok := doc[field]
		if !ok {
			continue
		}
		if !jsonTypeMatches(value, want) {
			result.Status = "blocked"
			result.Blockers = append(result.Blockers, fmt.Sprintf("%s must be %s", field, want))
		}
	}
	if intrinsic, blockers := validateIntrinsicCorrelationContract(body, schema); intrinsic {
		if len(blockers) > 0 {
			result.Status = "blocked"
			result.Blockers = append(result.Blockers, blockers...)
		}
	} else if blockers := validateAgainstSchemaFile(path, doc, schema); len(blockers) > 0 {
		result.Status = "blocked"
		result.Blockers = append(result.Blockers, blockers...)
	}
	if result.Status != "ready" {
		return result, fmt.Errorf(strings.Join(result.Blockers, "; "))
	}
	return result, nil
}

func contractDiscriminator(doc map[string]any) (string, error) {
	selected := ""
	for _, field := range []string{"schema", "schema_version", "contract_version"} {
		raw, present := doc[field]
		if !present {
			continue
		}
		value, ok := raw.(string)
		if !ok {
			return "", fmt.Errorf("contract discriminator field %s must be a string", field)
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return "", fmt.Errorf("contract discriminator field %s must be a non-empty string", field)
		}
		if selected != "" && selected != value {
			return "", fmt.Errorf("contract discriminator fields conflict")
		}
		selected = value
	}
	if selected == "" {
		return "", fmt.Errorf("schema, schema_version, or contract_version is required")
	}
	return selected, nil
}

func validateIntrinsicCorrelationContract(body []byte, schema string) (bool, []string) {
	switch schema {
	case "ao.next.execution-journal-prefix.v1":
		if _, err := parseAONextJournalPrefix(body); err != nil {
			return true, []string{err.Error()}
		}
		return true, nil
	case CorrelationChainSchema:
		var chain CorrelationChain
		if err := json.Unmarshal(body, &chain); err != nil {
			return true, []string{err.Error()}
		}
		if err := validateCorrelationChainContract(chain); err != nil {
			return true, []string{err.Error()}
		}
		return true, nil
	case CorrelationChainReferenceSchema:
		var reference CorrelationChainReference
		if err := json.Unmarshal(body, &reference); err != nil {
			return true, []string{err.Error()}
		}
		record := Record{
			MissionID:     reference.MissionID,
			CorrelationID: reference.CorrelationID,
		}
		if err := validateCorrelationChainReference(reference, record); err != nil {
			return true, []string{err.Error()}
		}
		return true, nil
	case ObjectiveWorkflowSchema:
		var contract ObjectiveWorkflowContract
		if err := json.Unmarshal(body, &contract); err != nil {
			return true, []string{fmt.Sprintf("workflow contract: %v", err)}
		}
		if err := validateObjectiveWorkflowContract(contract); err != nil {
			return true, []string{err.Error()}
		}
		return true, nil
	case RecordSchema:
		var record Record
		if err := decodeRecordBytes(body, &record); err != nil {
			return true, []string{err.Error()}
		}
		return true, nil
	default:
		return false, nil
	}
}

func validateCorrelationChainContract(chain CorrelationChain) error {
	switch {
	case chain.Schema != CorrelationChainSchema:
		return fmt.Errorf("correlation chain schema must be %s", CorrelationChainSchema)
	case strings.TrimSpace(chain.MissionID) == "":
		return fmt.Errorf("correlation chain mission_id is required")
	case !correlationIDPattern.MatchString(chain.CorrelationID):
		return fmt.Errorf("correlation chain correlation_id is invalid")
	case len(chain.Entries) == 0:
		return fmt.Errorf("correlation chain entries are required")
	case chain.SafeToExecute || chain.ExecutesWork || chain.ApprovesWork ||
		chain.MutatesRepositories || chain.WidensPolicy || chain.PublishesArtifacts:
		return fmt.Errorf("correlation chain must not claim execution, approval, mutation, policy, or publication authority")
	}
	roles := make(map[string]struct{}, len(chain.Entries))
	for _, entry := range chain.Entries {
		if err := validateCorrelationEntryShape(entry); err != nil {
			return fmt.Errorf("entry %q: %w", entry.Role, err)
		}
		if _, duplicate := roles[entry.Role]; duplicate {
			return fmt.Errorf("duplicate artifact role %q", entry.Role)
		}
		roles[entry.Role] = struct{}{}
	}
	for _, entry := range chain.Entries {
		if entry.BindingMode != CorrelationBindingDigestLink {
			continue
		}
		if _, present := roles[entry.ParentRole]; !present {
			return fmt.Errorf("entry %q parent role %q is missing", entry.Role, entry.ParentRole)
		}
	}
	return validateCorrelationParentGraph(chainParentRoles(chain.Entries))
}

func validateObjectiveWorkflowSemantics(doc map[string]any) []string {
	routingClass := stringFromAny(doc["routing_class"])
	var acceptance, route, next string
	var stages []ObjectiveWorkflowStage
	switch routingClass {
	case "pending_blueprint":
		acceptance = "pending_blueprint"
		route = "ao-blueprint"
		next = "send objective to AO Blueprint for requirements and acceptance"
		stages = objectiveStages("required", "conditional", "conditional")
	case "complex":
		acceptance = "accepted"
		route = "ao-atlas"
		next = "send accepted objective to AO Atlas for workgraph sequencing"
		stages = objectiveStages("omitted", "required", "conditional")
	case "reduced":
		acceptance = "accepted"
		route = "ao-foundry"
		next = "send accepted reduced objective directly to AO Foundry"
		stages = objectiveStages("omitted", "omitted", "required")
	default:
		return []string{"routing_class is unsupported"}
	}
	blockers := []string{}
	if stringFromAny(doc["acceptance_status"]) != acceptance {
		blockers = append(blockers, "acceptance_status does not match routing_class")
	}
	if stringFromAny(doc["initial_route"]) != route {
		blockers = append(blockers, "initial_route does not match routing_class")
	}
	if stringFromAny(doc["exact_next_action"]) != next {
		blockers = append(blockers, "exact_next_action does not match routing_class")
	}
	rawStages, _ := doc["stages"].([]any)
	if len(rawStages) != len(stages) {
		blockers = append(blockers, "stages do not contain the required workflow sequence")
	} else {
		for i, expected := range stages {
			stage, _ := rawStages[i].(map[string]any)
			if stringFromAny(stage["name"]) != expected.Name ||
				stringFromAny(stage["status"]) != expected.Status ||
				stringFromAny(stage["reason"]) != expected.Reason {
				blockers = append(blockers, fmt.Sprintf("stages.%d does not match routing_class", i))
			}
		}
	}
	missionID := stringFromAny(doc["mission_id"])
	rawCommands, _ := doc["lifecycle_commands"].([]any)
	expectedCommands := objectiveLifecycleCommands(missionID)
	if len(rawCommands) != len(expectedCommands) {
		blockers = append(blockers, "lifecycle_commands do not contain the required command sequence")
	} else {
		for i, expected := range expectedCommands {
			if stringFromAny(rawCommands[i]) != expected {
				blockers = append(blockers, fmt.Sprintf("lifecycle_commands.%d does not bind mission_id", i))
			}
		}
	}
	return blockers
}

func validateRecordWorkflowContract(record Record) error {
	if record.SourceRecordStatus != "" || record.TerminalProjectionStatus != "" ||
		record.TerminalProjectionReadOnly || record.EffectiveOperatorStatus != "" {
		return fmt.Errorf("durable Mission record must not contain terminal projection fields")
	}
	contract := record.WorkflowContract
	if contract == nil {
		return validateRecordCorrelationState(record)
	}
	if err := validateObjectiveWorkflowContract(*contract); err != nil {
		return err
	}
	switch {
	case record.CorrelationID == "" || contract.CorrelationID != record.CorrelationID:
		return fmt.Errorf("workflow contract correlation_id does not match mission record")
	case contract.MissionID != record.MissionID:
		return fmt.Errorf("workflow contract mission_id does not match mission record")
	case record.ObjectiveDigest != DigestObjective(record.Objective) &&
		(!record.ObjectiveRedacted || !strings.Contains(record.Objective, "<local-path-redacted>")):
		return fmt.Errorf("mission record objective_digest does not match objective")
	case contract.ObjectiveDigest != record.ObjectiveDigest:
		return fmt.Errorf("workflow contract objective_digest does not match mission record")
	}
	return validateRecordCorrelationState(record)
}

func validateObjectiveWorkflowContract(contract ObjectiveWorkflowContract) error {
	switch {
	case contract.Schema != ObjectiveWorkflowSchema:
		return fmt.Errorf("workflow contract schema must be %s", ObjectiveWorkflowSchema)
	case contract.Status != "ready":
		return fmt.Errorf("workflow contract status must be ready")
	case !objectiveWorkflowMissionIDPattern.MatchString(contract.MissionID):
		return fmt.Errorf("workflow contract mission_id is invalid")
	case !correlationIDPattern.MatchString(contract.CorrelationID):
		return fmt.Errorf("workflow contract correlation_id is invalid")
	case !validSHA256Digest(contract.ObjectiveDigest):
		return fmt.Errorf("workflow contract objective_digest is invalid")
	case !objectiveWorkflowTimestampPattern.MatchString(contract.GeneratedAtUTC):
		return fmt.Errorf("workflow contract generated_at_utc is invalid")
	case contract.SafeToExecute || contract.ExecutesWork || contract.ApprovesWork || contract.MutatesRepositories:
		return fmt.Errorf("workflow contract must not claim execution, approval, or mutation authority")
	}
	body, err := json.Marshal(contract)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return err
	}
	if blockers := validateObjectiveWorkflowSemantics(doc); len(blockers) > 0 {
		return fmt.Errorf("workflow contract is inconsistent: %s", strings.Join(blockers, "; "))
	}
	return nil
}

func contractRules(schema string) ([]string, map[string]string) {
	return requiredFieldsForContract(schema), propertyTypesForContract(schema)
}

func requiredFieldsForContract(schema string) []string {
	switch schema {
	case "ao.next.execution-journal-prefix.v1":
		return nil
	case CorrelationChainSchema:
		return []string{
			"schema", "mission_id", "correlation_id", "entries",
			"safe_to_execute", "executes_work", "approves_work",
			"mutates_repositories", "widens_policy", "publishes_artifacts",
		}
	case CorrelationChainReferenceSchema:
		return []string{
			"schema", "mission_id", "correlation_id", "chain_digest",
			"reference_digest", "entries", "safe_to_execute", "executes_work",
			"approves_work", "mutates_repositories", "widens_policy",
			"publishes_artifacts",
		}
	case ObjectiveWorkflowSchema:
		return []string{
			"schema", "status", "mission_id", "correlation_id", "objective_digest",
			"routing_class", "acceptance_status", "initial_route", "stages",
			"lifecycle_commands", "exact_next_action", "safe_to_execute",
			"executes_work", "approves_work", "mutates_repositories", "generated_at_utc",
		}
	case RecordSchema:
		return []string{"schema", "mission_id", "objective_digest", "status", "created_at_utc", "current_route"}
	case SnapshotSchema:
		return []string{"schema", "mission_id", "highest_proven_live_class", "next_denied_class", "safe_to_execute", "exact_next_action", "generated_at_utc"}
	case RouteSchema:
		return []string{"schema", "mission_id", "route", "reason", "safe_to_execute"}
	case SchedulerReadbackSchema:
		return []string{"schema", "mission_id", "status", "scheduler", "event_loop"}
	case TelegramCommandSchema:
		return []string{"schema", "chat_id", "command", "role"}
	case A2AAgentCardSchema:
		return []string{"schema", "name", "protocol_version", "description", "endpoint", "methods", "capabilities", "mutation_authority"}
	case A2ATaskSchema:
		return []string{"schema", "task_id", "method", "status"}
	case ArtifactRefSchema:
		return []string{"schema", "ref"}
	default:
		return nil
	}
}

func propertyTypesForContract(schema string) map[string]string {
	switch schema {
	case "ao.next.execution-journal-prefix.v1":
		return nil
	case CorrelationChainSchema:
		return map[string]string{
			"schema": "string", "mission_id": "string", "correlation_id": "string",
			"entries": "array", "safe_to_execute": "boolean", "executes_work": "boolean",
			"approves_work": "boolean", "mutates_repositories": "boolean",
			"widens_policy": "boolean", "publishes_artifacts": "boolean",
		}
	case CorrelationChainReferenceSchema:
		return map[string]string{
			"schema": "string", "mission_id": "string", "correlation_id": "string",
			"chain_digest": "string", "reference_digest": "string", "entries": "array",
			"safe_to_execute": "boolean", "executes_work": "boolean",
			"approves_work": "boolean", "mutates_repositories": "boolean",
			"widens_policy": "boolean", "publishes_artifacts": "boolean",
		}
	case ObjectiveWorkflowSchema:
		return map[string]string{
			"schema": "string", "status": "string", "mission_id": "string",
			"correlation_id": "string", "objective_digest": "string",
			"routing_class": "string", "acceptance_status": "string",
			"initial_route": "string", "stages": "array",
			"lifecycle_commands": "array", "exact_next_action": "string",
			"safe_to_execute": "boolean", "executes_work": "boolean",
			"approves_work": "boolean", "mutates_repositories": "boolean",
			"generated_at_utc": "string",
		}
	case RecordSchema:
		return map[string]string{"schema": "string", "mission_id": "string", "objective": "string", "objective_digest": "string", "status": "string", "created_at_utc": "string", "updated_at_utc": "string", "current_route": "string", "current_phase": "string", "blockers": "array", "exact_next_action": "string", "artifact_refs": "array", "steps": "array"}
	case SnapshotSchema:
		return map[string]string{"schema": "string", "mission_id": "string", "highest_proven_live_class": "string", "next_denied_class": "string", "safe_to_execute": "boolean", "exact_next_action": "string", "generated_at_utc": "string"}
	case RouteSchema:
		return map[string]string{"schema": "string", "mission_id": "string", "route": "string", "reason": "string", "safe_to_execute": "boolean"}
	case SchedulerReadbackSchema:
		return map[string]string{"schema": "string", "mission_id": "string", "status": "string", "scheduler": "string", "event_loop": "boolean"}
	case TelegramCommandSchema:
		return map[string]string{"schema": "string", "chat_id": "string", "command": "string", "role": "string"}
	case A2AAgentCardSchema:
		return map[string]string{"schema": "string", "name": "string", "protocol_version": "string", "description": "string", "endpoint": "string", "methods": "array", "capabilities": "array", "mutation_authority": "boolean"}
	case A2ATaskSchema:
		return map[string]string{"schema": "string", "task_id": "string", "method": "string", "status": "string", "mutation_authority": "boolean"}
	case ArtifactRefSchema:
		return map[string]string{"schema": "string", "ref": "string", "content_ref": "string", "digest": "string", "kind": "string"}
	default:
		return nil
	}
}

func validateAgainstSchemaFile(path string, doc map[string]any, schema string) []string {
	name := contractFileName(schema)
	candidates := []string{
		filepath.Join("docs", "contracts", name),
		filepath.Join("..", "..", "docs", "contracts", name),
		filepath.Join(filepath.Dir(path), "..", "..", "docs", "contracts", name),
	}
	schemaPath := ""
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			schemaPath = candidate
			break
		}
	}
	if schemaPath == "" {
		if knownContractRequiresSchemaDefinition(schema) {
			return []string{fmt.Sprintf("schema definition unavailable for known contract %s", schema)}
		}
		return nil
	}
	body, err := os.ReadFile(schemaPath)
	if err != nil {
		if knownContractRequiresSchemaDefinition(schema) {
			return []string{fmt.Sprintf("schema definition unavailable for known contract %s: %v", schema, err)}
		}
		return nil
	}
	var schemaDoc map[string]any
	if err := json.Unmarshal(body, &schemaDoc); err != nil {
		if knownContractRequiresSchemaDefinition(schema) {
			return []string{fmt.Sprintf("schema definition is invalid for known contract %s: %v", schema, err)}
		}
		return nil
	}
	return validateJSONSchemaNode(doc, schemaDoc, "")
}

func knownContractRequiresSchemaDefinition(schema string) bool {
	return strings.HasPrefix(schema, "ao.mission.") ||
		schema == "ao.command.mission-status.v0.1"
}

func validateJSONSchemaNode(value any, schema map[string]any, path string) []string {
	blockers := []string{}
	label := strings.TrimSuffix(path, ".")
	if label == "" {
		label = "document"
	}
	if want, _ := schema["type"].(string); want != "" && !jsonTypeMatches(value, want) {
		return []string{fmt.Sprintf("%s must be %s", label, want)}
	}
	if allOf, ok := schema["allOf"].([]any); ok {
		for _, raw := range allOf {
			childSchema, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			blockers = append(blockers, validateJSONSchemaNode(value, childSchema, path)...)
		}
	}
	if anyOf, ok := schema["anyOf"].([]any); ok {
		matches := 0
		for _, raw := range anyOf {
			childSchema, ok := raw.(map[string]any)
			if ok && len(validateJSONSchemaNode(value, childSchema, path)) == 0 {
				matches++
			}
		}
		if matches == 0 {
			blockers = append(blockers, fmt.Sprintf("%s must match at least one allowed schema", label))
		}
	}
	if oneOf, ok := schema["oneOf"].([]any); ok {
		matches := 0
		for _, raw := range oneOf {
			childSchema, ok := raw.(map[string]any)
			if ok && len(validateJSONSchemaNode(value, childSchema, path)) == 0 {
				matches++
			}
		}
		if matches != 1 {
			blockers = append(blockers, fmt.Sprintf("%s must match exactly one allowed schema", label))
		}
	}
	if notSchema, ok := schema["not"].(map[string]any); ok &&
		len(validateJSONSchemaNode(value, notSchema, path)) == 0 {
		blockers = append(blockers, fmt.Sprintf("%s matches a forbidden schema", label))
	}
	if ifSchema, ok := schema["if"].(map[string]any); ok {
		branch := "else"
		if len(validateJSONSchemaNode(value, ifSchema, path)) == 0 {
			branch = "then"
		}
		if branchSchema, ok := schema[branch].(map[string]any); ok {
			blockers = append(blockers, validateJSONSchemaNode(value, branchSchema, path)...)
		}
	}
	if constValue, ok := schema["const"]; ok && value != constValue {
		blockers = append(blockers, fmt.Sprintf("%s must equal %v", label, constValue))
	}
	if allowed, ok := schema["enum"].([]any); ok {
		found := false
		for _, candidate := range allowed {
			if value == candidate {
				found = true
				break
			}
		}
		if !found {
			blockers = append(blockers, fmt.Sprintf("%s has unsupported value", label))
		}
	}
	switch typed := value.(type) {
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		if required, ok := schema["required"].([]any); ok {
			for _, item := range required {
				field, _ := item.(string)
				if field == "" {
					continue
				}
				if _, exists := typed[field]; !exists {
					blockers = append(blockers, path+field+" is required")
				}
			}
		}
		if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
			for field := range typed {
				if _, exists := props[field]; !exists {
					blockers = append(blockers, path+field+" is not allowed")
				}
			}
		}
		for field, child := range typed {
			childSchema, ok := props[field].(map[string]any)
			if !ok {
				continue
			}
			blockers = append(blockers, validateJSONSchemaNode(child, childSchema, path+field+".")...)
		}
	case []any:
		if min, ok := schema["minItems"].(float64); ok && len(typed) < int(min) {
			blockers = append(blockers, fmt.Sprintf("%s must contain at least %d items", label, int(min)))
		}
		if max, ok := schema["maxItems"].(float64); ok && len(typed) > int(max) {
			blockers = append(blockers, fmt.Sprintf("%s must contain at most %d items", label, int(max)))
		}
		if itemSchema, ok := schema["items"].(map[string]any); ok {
			for i, item := range typed {
				blockers = append(blockers, validateJSONSchemaNode(item, itemSchema, fmt.Sprintf("%s%d.", path, i))...)
			}
		}
	case string:
		if min, ok := schema["minLength"].(float64); ok && len(typed) < int(min) {
			blockers = append(blockers, fmt.Sprintf("%s is too short", label))
		}
		if max, ok := schema["maxLength"].(float64); ok && len(typed) > int(max) {
			blockers = append(blockers, fmt.Sprintf("%s is too long", label))
		}
		if pattern, ok := schema["pattern"].(string); ok {
			re, err := regexp.Compile(pattern)
			if err == nil && !re.MatchString(typed) {
				blockers = append(blockers, fmt.Sprintf("%s does not match required pattern", label))
			}
		}
	}
	return blockers
}

func contractFileName(schema string) string {
	if schema == RecordSchema {
		return "mission-record-v0.1.schema.json"
	}
	if schema == "ao.command.mission-status.v0.1" {
		return "command-status-v0.1.schema.json"
	}
	name := strings.TrimPrefix(schema, "ao.mission.")
	name = strings.ReplaceAll(name, ".v", "-v")
	return name + ".schema.json"
}

func jsonTypeMatches(value any, want string) bool {
	switch want {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "integer":
		f, ok := value.(float64)
		return ok && f == float64(int(f))
	case "number":
		_, ok := value.(float64)
		return ok
	default:
		return true
	}
}
