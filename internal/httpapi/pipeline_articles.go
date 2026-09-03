package httpapi

import (
	"context"
	"net/http"

	"doublangu/internal/library"
	"doublangu/internal/pipeline"
	"doublangu/internal/reader"
)

// resolvePipelineSnapshot returns the active profile enriched with provider
// types/fingerprints and stage versions, or nil when no profile is active.
func (h *ArticleHandler) resolvePipelineSnapshot(ctx context.Context) (*pipeline.ProfileSnapshot, error) {
	activeID, err := h.profiles.ActiveProfile(ctx)
	if err != nil {
		return nil, err
	}
	if activeID == "" {
		return nil, nil
	}
	profile, err := h.profiles.Get(ctx, activeID)
	if err != nil {
		return nil, err
	}
	bindings, err := h.enrichBindings(ctx, profile.Bindings)
	if err != nil {
		return nil, err
	}
	return &pipeline.ProfileSnapshot{ID: profile.ID, Name: profile.Name, Bindings: bindings}, nil
}

// enrichBindings fills provider type/fingerprint and stage contract/prompt
// versions from the registry and canonicalizes options. It uses the shared
// usableProfileBindings check so article resolution and fresh-run profile
// selection apply exactly the same current-usability rules (enabled provider,
// model still listed, Codex effort still advertised, non-stale catalog) as
// settings activation: a stored profile referencing a disabled/removed
// provider or an unavailable model/effort is rejected before any article or
// job state changes.
func (h *ArticleHandler) enrichBindings(ctx context.Context, bindings []pipeline.BindingSnapshot) ([]pipeline.BindingSnapshot, error) {
	return usableProfileBindings(ctx, h.registry, h.catalog, bindings)
}

// queuePipelineAnalysis queues a pipeline job for an article. Normal requests
// reuse the stored snapshot; fresh requests resolve the named or active
// profile. Legacy articles without a snapshot adopt the active profile once.
func (h *ArticleHandler) queuePipelineAnalysis(w http.ResponseWriter, r *http.Request, id library.ULID, force, fresh bool, profileID string) {
	if !fresh && profileID != "" {
		WriteError(w, http.StatusBadRequest, "profile_id is valid only with fresh:true", ErrCodeValidation)
		return
	}
	hasSnapshot, err := h.store.HasPipelineSnapshot(r.Context(), id)
	if err != nil {
		writeReaderError(w, err)
		return
	}
	var snapshot *pipeline.ProfileSnapshot
	if fresh {
		if profileID != "" {
			if _, err := library.ParseULID(profileID); err != nil {
				WriteError(w, http.StatusBadRequest, "invalid profile id", ErrCodeValidation)
				return
			}
			profile, err := h.profiles.Get(r.Context(), profileID)
			if err != nil {
				writeProfileError(w, err)
				return
			}
			bindings, err := h.enrichBindings(r.Context(), profile.Bindings)
			if err != nil {
				WriteError(w, http.StatusServiceUnavailable, "profile is not usable", ErrCodeAnalysisUnavailable)
				return
			}
			snapshot = &pipeline.ProfileSnapshot{ID: profile.ID, Name: profile.Name, Bindings: bindings}
		} else {
			snapshot, err = h.resolvePipelineSnapshot(r.Context())
			if err != nil || snapshot == nil {
				WriteError(w, http.StatusServiceUnavailable, "no usable analysis profile is active", ErrCodeAnalysisUnavailable)
				return
			}
		}
	} else if !hasSnapshot {
		// Legacy article: adopt the active profile as the one-time fallback.
		snapshot, err = h.resolvePipelineSnapshot(r.Context())
		if err != nil {
			WriteError(w, http.StatusServiceUnavailable, "no usable analysis profile is active", ErrCodeAnalysisUnavailable)
			return
		}
		if snapshot == nil {
			WriteError(w, http.StatusServiceUnavailable, "no usable analysis profile is active", ErrCodeAnalysisUnavailable)
			return
		}
	}
	if _, err := h.store.QueueAnalysisWithProfile(r.Context(), id, force, fresh, snapshot); err != nil {
		writeReaderError(w, err)
		return
	}
	article, err := h.store.GetArticle(r.Context(), id)
	if err != nil {
		writeReaderError(w, err)
		return
	}
	WriteJSON(w, http.StatusAccepted, article)
}

// createPipelineArticle stores the source article and queues its initial
// pipeline job with the active profile when one exists; without an active
// profile it creates the readable article with analysis failed.
func (h *ArticleHandler) createPipelineArticle(w http.ResponseWriter, r *http.Request, article *reader.Article) {
	snapshot, err := h.resolvePipelineSnapshot(r.Context())
	if err != nil || snapshot == nil {
		if err := h.store.CreateArticlePipelineUnavailable(r.Context(), article); err != nil {
			writeReaderError(w, err)
			return
		}
		created, getErr := h.store.GetArticle(r.Context(), article.ID)
		if getErr != nil {
			writeReaderError(w, getErr)
			return
		}
		WriteJSON(w, http.StatusCreated, created)
		return
	}
	if err := h.store.CreateArticleQueuedWithProfile(r.Context(), article, snapshot); err != nil {
		writeReaderError(w, err)
		return
	}
	created, err := h.store.GetArticle(r.Context(), article.ID)
	if err != nil {
		writeReaderError(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, created)
}
