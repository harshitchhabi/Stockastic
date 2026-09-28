package httpapi

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"stockastic/api/internal/app"
)

// Team members (see app/members.go): teammates join with their team's code and get their own login; only the
// team's trader may trade or move the team's money.

func login(c *gin.Context) app.Login { return c.MustGet(ctxLogin).(app.Login) }

// memberDetail notes which teammate did something, for the activity record ("" for the team leader).
func memberDetail(l app.Login) string {
	if l.Member != nil {
		return "teammate " + l.Member.Name
	}
	return ""
}

// traderOnly lets a request through only from the login that trades for the team. It is checked on the server,
// not just by hiding buttons.
func (s *Server) traderOnly(c *gin.Context) {
	l := login(c)
	if !s.a.CanTrade(l) {
		who := s.a.AccountFor(l).TraderName
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": app.ErrNotTeamTrader.Error(),
			"message": "Only " + who + " can buy, sell or move money for your team. You can watch everything from here."})
		return
	}
	c.Next()
}

type joinRequest struct {
	TeamCode  string `json:"teamCode"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Password  string `json:"password"`
	EventCode string `json:"eventCode"`
}

// join registers a teammate with the code their team leader shared.
func (s *Server) join(c *gin.Context) {
	var in joinRequest
	if !s.decode(c, &in) {
		return
	}
	if s.googleOnly(c) {
		return
	}
	if err := s.a.CheckSignupCode(in.EventCode); err != nil {
		s.fail(c, err)
		return
	}
	if ok, wait := s.lim.signup.allow(c.ClientIP(), time.Now()); !ok {
		tooMany(c, wait, "too_many_signups", "Too many sign-ups from this connection. Wait a moment.")
		return
	}
	l, err := s.a.JoinTeam(in.TeamCode, in.Name, in.Email, in.Password, in.EventCode)
	if err != nil {
		s.fail(c, err)
		return
	}
	s.track(c, l.Team.ID, "signup", memberDetail(l))
	s.issue(c, l)
}

func (s *Server) teamRoutes(me *gin.RouterGroup) {
	me.GET("/team", func(c *gin.Context) {
		v, err := s.a.MyTeam(login(c))
		if err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(http.StatusOK, v)
	})
	me.POST("/team/code", func(c *gin.Context) {
		code, err := s.a.NewTeamCode(login(c))
		if err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"joinCode": code})
	})
	me.POST("/team/trader", func(c *gin.Context) {
		var b struct {
			MemberID string `json:"memberId"`
		}
		if !s.decode(c, &b) {
			return
		}
		if err := s.a.SetTrader(login(c), b.MemberID); err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
}

func (s *Server) adminTeamRoutes(adm *gin.RouterGroup) {
	adm.GET("/accounts/:id/members", func(c *gin.Context) {
		v, err := s.a.TeamMembers(c.Param("id"))
		if err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(http.StatusOK, v)
	})
	adm.POST("/accounts/:id/trader", s.act(func(u app.User, b body, c *gin.Context) error {
		return s.a.SetTraderAdmin(u, b.Reason, c.Param("id"), b.MemberID)
	}))
	adm.POST("/accounts/:id/team-code", s.act(func(u app.User, b body, c *gin.Context) error {
		return s.a.NewTeamCodeAdmin(u, b.Reason, c.Param("id"))
	}))
	adm.POST("/accounts/:id/members/:mid/remove", s.act(func(u app.User, b body, c *gin.Context) error {
		return s.a.RemoveMember(u, b.Reason, c.Param("id"), c.Param("mid"))
	}))
	adm.POST("/accounts/:id/members/:mid/sign-out", s.act(func(u app.User, b body, c *gin.Context) error {
		return s.a.SignOutMember(u, b.Reason, c.Param("id"), c.Param("mid"))
	}))
}

// googleOnly refuses a registration by email and password when only Google may register people, and says so.
func (s *Server) googleOnly(c *gin.Context) bool {
	if !s.opt.GoogleOnlySignup || s.opt.Google == nil {
		return false
	}
	c.JSON(http.StatusForbidden, gin.H{"error": "google_only", "message": "Register with your college Google account: use the Google button."})
	return true
}
