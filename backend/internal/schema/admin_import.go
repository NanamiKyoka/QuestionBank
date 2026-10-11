package schema

type AdminImportRowError struct {
	Row     int    `json:"row"`
	Message string `json:"message"`
}

type UserImportResult struct {
	Status  string                `json:"status"`
	Total   int                   `json:"total"`
	Created int                   `json:"created"`
	Failed  int                   `json:"failed"`
	Errors  []AdminImportRowError `json:"errors"`
}

type TagImportResult struct {
	Status  string                `json:"status"`
	Total   int                   `json:"total"`
	Created int                   `json:"created"`
	Skipped int                   `json:"skipped"`
	Failed  int                   `json:"failed"`
	Errors  []AdminImportRowError `json:"errors"`
}

type KnowledgePointImportPreflight struct {
	SubjectID         uint   `json:"subject_id"`
	SubjectName       string `json:"subject_name"`
	ExistingCount     int64  `json:"existing_count"`
	AffectedQuestions int64  `json:"affected_questions"`
}

type KnowledgePointImportResult struct {
	Status       string                `json:"status"`
	Mode         string                `json:"mode"`
	Total        int                   `json:"total"`
	Created      int                   `json:"created"`
	Skipped      int                   `json:"skipped"`
	Failed       int                   `json:"failed"`
	VectorSynced bool                  `json:"vector_synced"`
	Errors       []AdminImportRowError `json:"errors"`
}
