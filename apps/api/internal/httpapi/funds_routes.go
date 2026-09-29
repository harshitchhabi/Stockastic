package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"stockastic/api/internal/app"
)

// fundRoutes are the Phase 2 routes: funds, allocation windows, strategy logs, and the organiser's
// qualification, fund and prize screens.
func (s *Server) fundRoutes(me, adm *gin.RouterGroup) {
	me.GET("/funds", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.FundsFor(user(c))) })
	me.GET("/funds/mine", func(c *gin.Context) {
		m, err := s.a.MyFund(user(c))
		if err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(http.StatusOK, m)
	})
	me.PUT("/funds/mine/profile", s.traderOnly, func(c *gin.Context) {
		var r app.ProfileRequest
		if !s.decode(c, &r) {
			return
		}
		if err := s.a.SetFundProfile(user(c), r); err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	me.POST("/funds/:id/allocate", s.traderOnly, func(c *gin.Context) {
		var r app.AllocationRequest
		if !s.decode(c, &r) {
			return
		}
		res, err := s.a.Allocate(user(c), c.Param("id"), r)
		if err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(http.StatusOK, res)
	})
	me.POST("/funds/:id/redeem", s.traderOnly, func(c *gin.Context) {
		var r app.AllocationRequest
		if !s.decode(c, &r) {
			return
		}
		res, err := s.a.Redeem(c.Request.Context(), user(c), c.Param("id"), r)
		if err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(http.StatusOK, res)
	})
	me.GET("/strategy-log", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.MyLogs(user(c))) })
	me.POST("/strategy-log", s.traderOnly, func(c *gin.Context) {
		var r struct {
			Text string `json:"text"`
		}
		if !s.decode(c, &r) {
			return
		}
		if err := s.a.SubmitLog(user(c), r.Text); err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	adm.GET("/qualification", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.Qualification()) })
	adm.POST("/qualification/run", s.act(func(u app.User, b body, _ *gin.Context) error { return s.a.FormFunds(u, b.Pairs) }))
	adm.GET("/funds", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.AdminFunds()) })
	adm.POST("/funds/:id/disqualify", s.act(func(u app.User, _ body, c *gin.Context) error { return s.a.DisqualifyFund(u, c.Param("id")) }))
	adm.POST("/funds/dissolve", s.act(func(u app.User, _ body, _ *gin.Context) error { return s.a.DissolveFunds(u) }))
	adm.POST("/funds/:id/trader", s.act(func(u app.User, b body, c *gin.Context) error {
		return s.a.SetFundTrader(u, c.Param("id"), b.AccountID)
	}))
	adm.GET("/schedule", func(c *gin.Context) { c.JSON(http.StatusOK, s.a.Schedule()) })
	adm.PUT("/schedule", func(c *gin.Context) {
		var r struct {
			Blocks []app.ScheduleBlock `json:"blocks"`
		}
		if !s.decode(c, &r) {
			return
		}
		if err := s.a.SetSchedule(user(c), r.Blocks); err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	adm.POST("/schedule/template", s.act(func(u app.User, _ body, _ *gin.Context) error { return s.a.LoadTemplateSchedule(u) }))
	adm.POST("/event/reset", s.act(func(u app.User, _ body, _ *gin.Context) error { return s.a.ResetEvent(u, false) }))
	adm.POST("/event/start-fresh", s.act(func(u app.User, _ body, _ *gin.Context) error { return s.a.ResetEvent(u, true) }))
	adm.POST("/snapshots/:name", s.act(func(u app.User, _ body, c *gin.Context) error { return s.a.TakeSnapshot(u, c.Param("name")) }))
	adm.GET("/strategy-logs", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"entrants": s.a.LogEntrants()})
	})
}
