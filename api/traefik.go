package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type traefikView struct {
	Config      any
	Message     string
	Error       string
	CurrentUser string
}

func (a *App) handleTraefikPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.render(w, "traefik.tmpl", traefikView{
		Config:      a.currentConfig(),
		Message:     r.URL.Query().Get("msg"),
		Error:       r.URL.Query().Get("err"),
		CurrentUser: a.currentUsername(r),
	})
}

func (a *App) handleAPITraefikLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	tailQuery := strings.TrimSpace(r.URL.Query().Get("tail"))
	tail := 500
	if strings.EqualFold(tailQuery, "all") {
		tail = -1
	} else if tailQuery != "" {
		tail, _ = strconv.Atoi(tailQuery)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	logs, err := a.docker.ContainerLogs(ctx, "traefik", tail)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"logs": logs})
}

func (a *App) handleAPITraefikCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var input struct {
		Command string `json:"command"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		apiError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	argv, err := splitCommandLine(input.Command)
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	result, err := a.docker.ExecContainer(ctx, "traefik", argv)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func splitCommandLine(input string) ([]string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, errString("command is required")
	}
	var args []string
	var current strings.Builder
	var quote rune
	escaped := false
	for _, r := range input {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteRune(r)
	}
	if escaped {
		current.WriteRune('\\')
	}
	if quote != 0 {
		return nil, errString("unterminated quote in command")
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	if len(args) == 0 {
		return nil, errString("command is required")
	}
	return args, nil
}
