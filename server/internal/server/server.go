// Package server собирает корневой маршрутизатор.
//
// В корневом ServeMux регистрируются только префиксы без метода: пересекающиеся
// шаблоны, ни один из которых не специфичнее другого, вызывают panic при
// регистрации. Маршрутизацию внутри kosync делает сам kosync-обработчик, потому
// что шаблоны gin строже {document} в ServeMux.
package server

import (
	"net/http"
)

// Handlers — обработчики слоёв.
type Handlers struct {
	Kosync http.Handler // /users/*, /syncs/*, /healthcheck
	API    http.Handler // /api/*
	Web    http.Handler // всё остальное
}

// New возвращает корневой обработчик.
func New(h Handlers) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/users/", h.Kosync)
	mux.Handle("/syncs/", h.Kosync)
	mux.Handle("/healthcheck", h.Kosync)
	mux.Handle("/healthcheck/", h.Kosync)
	mux.Handle("/api/", h.API)
	mux.Handle("/", h.Web)
	return mux
}
