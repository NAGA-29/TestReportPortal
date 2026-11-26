package main

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
)

// updateApp: PUT /api/apps/:app
// Replaces the app's name, package name and description (all fields as in POST /api/apps).
func (s *server) updateApp(c echo.Context) error {
	key, err := id(c, "app")
	if err != nil {
		return err
	}
	var v struct {
		Name        string `json:"name"`
		Package     string `json:"package_name"`
		Description string `json:"description"`
	}
	if err = decode(c, &v); err != nil {
		return err
	}
	if strings.TrimSpace(v.Name) == "" {
		return echo.NewHTTPError(400, "アプリ名は必須です")
	}
	b, err := object(c.Request().Context(), s.db, `UPDATE apps SET name=$2, package_name=$3, description=$4, updated_at=now()
		WHERE id=$1 RETURNING to_jsonb(apps)`, key, strings.TrimSpace(v.Name), strings.TrimSpace(v.Package), v.Description)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(404, "アプリが見つかりません")
	}
	if err != nil {
		return err
	}
	return c.JSONBlob(200, b)
}
