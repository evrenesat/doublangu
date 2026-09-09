// Prompt version library endpoints: owner-only reads and saves of the five
// fixed immutable prompt version streams. There is no PUT, DELETE, or
// save-and-activate endpoint; pinning a version happens explicitly through
// the profile editor, and saving a version never activates anything.
package httpapi

import (
	"errors"
	"net/http"

	"doublangu/internal/prompts"
	"doublangu/internal/store"
)

// PromptLibraryHandler serves the prompt version library.
type PromptLibraryHandler struct {
	prompts *prompts.Store
	csrf    CSRFVerifier
}

// NewPromptLibraryHandler returns the prompt library handler for db.
func NewPromptLibraryHandler(db *store.DB, csrf CSRFVerifier) *PromptLibraryHandler {
	return &PromptLibraryHandler{prompts: prompts.NewStore(db), csrf: csrf}
}

// promptVersionResponse is the owner-visible saved version row. It matches
// the AnalysisPromptVersion schema: immutable identity, owner label, exact
// instruction bytes, and the content hash over those bytes.
type promptVersionResponse struct {
	ID              string `json:"id"`
	PromptType      string `json:"prompt_type"`
	Version         int    `json:"version"`
	Label           string `json:"label"`
	InstructionText string `json:"instruction_text"`
	ContentHash     string `json:"content_hash"`
	CreatedAt       string `json:"created_at"`
}

type promptVersionListResponse struct {
	PromptType string                  `json:"prompt_type"`
	Versions   []promptVersionResponse `json:"versions"`
}

type promptVersionInput struct {
	InstructionText string `json:"instruction_text"`
	Label           string `json:"label"`
}

// ServePromptVersions handles GET and POST on
// /api/v1/analysis/prompts/{prompt_type}/versions.
func (h *PromptLibraryHandler) ServePromptVersions(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	promptType := prompts.PromptType(r.PathValue("prompt_type"))
	if !promptType.Valid() {
		WriteError(w, http.StatusNotFound, "unknown prompt type", ErrCodeNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		versions, err := h.prompts.List(r.Context(), promptType)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "prompt versions unavailable", ErrCodeInternal)
			return
		}
		response := make([]promptVersionResponse, 0, len(versions))
		for _, version := range versions {
			response = append(response, newPromptVersionResponse(version))
		}
		WriteOK(w, promptVersionListResponse{PromptType: string(promptType), Versions: response})
	case http.MethodPost:
		if h.csrf == nil || h.csrf.VerifyRequest(r) != nil {
			WriteError(w, http.StatusForbidden, "csrf token is missing or invalid", ErrCodeCSRF)
			return
		}
		var input promptVersionInput
		if err := decodeJSONObject(w, r, &input); err != nil {
			WriteError(w, http.StatusBadRequest, "prompt version body must be one JSON object with instruction_text and optional label", ErrCodeValidation)
			return
		}
		instruction := prompts.NormalizeInstruction(input.InstructionText)
		if err := prompts.ValidateInstruction(instruction); err != nil {
			WriteError(w, http.StatusBadRequest, err.Error(), ErrCodeValidation)
			return
		}
		if _, err := prompts.ValidateLabel(input.Label); err != nil {
			WriteError(w, http.StatusBadRequest, err.Error(), ErrCodeValidation)
			return
		}
		version, err := h.prompts.Save(r.Context(), promptType, instruction, input.Label)
		if err != nil {
			if errors.Is(err, prompts.ErrUnknownPromptType) {
				WriteError(w, http.StatusNotFound, "unknown prompt type", ErrCodeNotFound)
				return
			}
			WriteError(w, http.StatusInternalServerError, "prompt version could not be saved", ErrCodeInternal)
			return
		}
		WriteJSON(w, http.StatusCreated, map[string]any{
			"prompt_type": string(promptType),
			"version":     newPromptVersionResponse(*version),
		})
	default:
		w.Header().Set("Allow", "GET, POST")
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
	}
}

func newPromptVersionResponse(version prompts.Version) promptVersionResponse {
	return promptVersionResponse{
		ID: version.ID, PromptType: string(version.PromptType), Version: version.Version,
		Label: version.Label, InstructionText: version.InstructionText,
		ContentHash: version.ContentHash, CreatedAt: version.CreatedAt,
	}
}
