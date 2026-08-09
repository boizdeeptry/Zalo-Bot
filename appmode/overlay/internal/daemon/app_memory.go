package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"agentdc/internal/store"
)

const appMemoryRequestLimit = 16 << 10

func (a *api) handleMemoryOverview(w http.ResponseWriter, r *http.Request) {
	pinned, ok := a.appMemoryPinnedFilter(w, r)
	if !ok {
		return
	}
	overview, err := a.st.AppMemoryOverview(r.URL.Query().Get("q"), pinned)
	if err != nil {
		a.writeAppMemoryError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, overview)
}

func (a *api) handleMemoryThreadGet(w http.ResponseWriter, r *http.Request) {
	detail, err := a.st.AppThreadMemories(r.PathValue("tid"))
	if err != nil {
		a.writeAppMemoryError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, detail)
}

func (a *api) handleMemoryThreadPost(w http.ResponseWriter, r *http.Request) {
	var input store.AppThreadMemoryInput
	if !a.decodeAppMemoryJSON(w, r, &input) {
		return
	}
	memory, err := a.st.CreateAppThreadMemory(r.PathValue("tid"), input)
	if err != nil {
		a.writeAppMemoryError(w, err)
		return
	}
	a.writeJSON(w, http.StatusCreated, memory)
}

func (a *api) handleMemoryThreadPut(w http.ResponseWriter, r *http.Request) {
	id, ok := a.appMemoryID(w, r.PathValue("id"))
	if !ok {
		return
	}
	var input store.AppThreadMemoryInput
	if !a.decodeAppMemoryJSON(w, r, &input) {
		return
	}
	memory, err := a.st.UpdateAppThreadMemory(r.PathValue("tid"), id, input)
	if err != nil {
		a.writeAppMemoryError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, memory)
}

func (a *api) handleMemoryThreadDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := a.appMemoryID(w, r.PathValue("id"))
	if !ok {
		return
	}
	if err := a.st.DeleteAppThreadMemory(r.PathValue("tid"), id); err != nil {
		a.writeAppMemoryError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) handleMemoryLessonsGet(w http.ResponseWriter, r *http.Request) {
	pinned, ok := a.appMemoryPinnedFilter(w, r)
	if !ok {
		return
	}
	lessons, err := a.st.AppLessons(r.URL.Query().Get("q"), pinned)
	if err != nil {
		a.writeAppMemoryError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, lessons)
}

func (a *api) handleMemoryLessonPost(w http.ResponseWriter, r *http.Request) {
	var input store.AppLessonInput
	if !a.decodeAppMemoryJSON(w, r, &input) {
		return
	}
	lesson, err := a.st.CreateAppLesson(input)
	if err != nil {
		a.writeAppMemoryError(w, err)
		return
	}
	a.writeJSON(w, http.StatusCreated, lesson)
}

func (a *api) handleMemoryLessonPut(w http.ResponseWriter, r *http.Request) {
	id, ok := a.appMemoryID(w, r.PathValue("id"))
	if !ok {
		return
	}
	var input store.AppLessonInput
	if !a.decodeAppMemoryJSON(w, r, &input) {
		return
	}
	lesson, err := a.st.UpdateAppLesson(id, input)
	if err != nil {
		a.writeAppMemoryError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, lesson)
}

func (a *api) handleMemoryLessonDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := a.appMemoryID(w, r.PathValue("id"))
	if !ok {
		return
	}
	if err := a.st.DeleteAppLesson(id); err != nil {
		a.writeAppMemoryError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) decodeAppMemoryJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, appMemoryRequestLimit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var sizeError *http.MaxBytesError
		if errors.As(err, &sizeError) {
			a.writeErr(w, http.StatusRequestEntityTooLarge, "dữ liệu memory vượt quá giới hạn")
		} else {
			a.writeErr(w, http.StatusBadRequest, "dữ liệu JSON không hợp lệ")
		}
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		a.writeErr(w, http.StatusBadRequest, "dữ liệu JSON không hợp lệ")
		return false
	}
	return true
}

func (a *api) appMemoryID(w http.ResponseWriter, raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		a.writeErr(w, http.StatusBadRequest, "ID memory không hợp lệ")
		return 0, false
	}
	return id, true
}

func (a *api) appMemoryPinnedFilter(w http.ResponseWriter, r *http.Request) (bool, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("pinned"))
	if raw == "" {
		return false, true
	}
	pinned, err := strconv.ParseBool(raw)
	if err != nil {
		a.writeErr(w, http.StatusBadRequest, "bộ lọc pinned không hợp lệ")
		return false, false
	}
	return pinned, true
}

func (a *api) writeAppMemoryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrAppMemoryInvalid):
		a.writeErr(w, http.StatusBadRequest, "dữ liệu memory không hợp lệ")
	case errors.Is(err, store.ErrAppMemoryNotFound):
		a.writeErr(w, http.StatusNotFound, "không tìm thấy memory")
	case errors.Is(err, store.ErrAppMemoryPinLimit):
		a.writeErr(w, http.StatusConflict, "đã đạt giới hạn nội dung được ghim")
	default:
		a.logger.Error("memory request failed", "error", fmt.Sprintf("%T", err))
		a.writeErr(w, http.StatusInternalServerError, "không xử lý được memory")
	}
}
