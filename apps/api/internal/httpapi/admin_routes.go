package httpapi

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"stockastic/api/internal/app"
)

// privilegeRoutes are the organiser's extra powers: removing a team, messaging one, opening and closing
// registration, reading every trade, exporting results, and running the automatic news.
func (s *Server) privilegeRoutes(adm *gin.RouterGroup) {
	adm.POST("/accounts/:id/eject", s.act(func(u app.User, _ body, c *gin.Context) error { return s.a.Eject(u, c.Param("id")) }))
	adm.POST("/accounts/:id/readmit", s.act(func(u app.User, _ body, c *gin.Context) error { return s.a.Readmit(u, c.Param("id")) }))
	adm.POST("/accounts/:id/message", s.act(func(u app.User, b body, c *gin.Context) error { return s.a.Message(u, c.Param("id"), b.Text) }))

	adm.GET("/settings", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"signupOpen": s.a.SignupOpen(), "signupCode": s.a.SignupCode()})
	})
	adm.POST("/settings/signup-code", func(c *gin.Context) {
		var r struct {
			Code string `json:"code"`
		}
		if !s.decode(c, &r) {
			return
		}
		if err := s.a.SetSignupCode(user(c), r.Code); err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	adm.POST("/settings/signup", func(c *gin.Context) {
		var r struct {
			Open bool `json:"open"`
		}
		if !s.decode(c, &r) {
			return
		}
		if err := s.a.SetSignup(user(c), r.Open); err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	adm.GET("/trades", func(c *gin.Context) {
		limit, _ := strconv.Atoi(c.Query("limit"))
		c.JSON(http.StatusOK, s.a.AdminTrades(c.Query("account"), c.Query("symbol"), limit))
	})
	adm.GET("/export/trades.csv", func(c *gin.Context) {
		c.Header("Content-Type", "text/csv; charset=utf-8")
		c.Header("Content-Disposition", `attachment; filename="trades.csv"`)
		_ = s.a.WriteTradesCSV(c.Writer)
	})
	adm.GET("/export/accounts.csv", func(c *gin.Context) {
		c.Header("Content-Type", "text/csv; charset=utf-8")
		c.Header("Content-Disposition", `attachment; filename="accounts.csv"`)
		_ = s.a.WriteAccountsCSV(c.Writer)
	})

	adm.POST("/sim/news-mode", func(c *gin.Context) {
		var r struct {
			Manual bool `json:"manual"`
		}
		if !s.decode(c, &r) {
			return
		}
		if err := s.a.SetNewsManual(user(c), r.Manual); err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	adm.POST("/sim/shift", func(c *gin.Context) {
		var r struct {
			Minutes float64 `json:"minutes"`
		}
		if !s.decode(c, &r) {
			return
		}
		if err := s.a.ShiftNews(user(c), r.Minutes); err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	adm.POST("/sim/release-overdue", func(c *gin.Context) {
		n, err := s.a.ReleaseOverdueNews(user(c))
		if err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "released": n})
	})
	adm.GET("/schedule/check", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.CheckSchedule()) })
	adm.POST("/sim/:id/skip", func(c *gin.Context) {
		var r struct {
			Skip bool `json:"skip"`
		}
		if !s.decode(c, &r) {
			return
		}
		if err := s.a.SkipNewsItem(user(c), c.Param("id"), r.Skip); err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	adm.POST("/sim/:id/edit", func(c *gin.Context) {
		var r struct {
			Headline string   `json:"headline"`
			AtMinute *float64 `json:"atMinute"`
		}
		if !s.decode(c, &r) {
			return
		}
		if err := s.a.EditNewsItem(user(c), c.Param("id"), r.Headline, r.AtMinute); err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
}
