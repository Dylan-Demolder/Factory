package config

import "strings"

// The five seats a project runs through. Roles are fixed — what changes is
// which agent fills a seat and, with role_models, which model it runs on.
const (
	RoleInterviewer = "interviewer"
	RolePlanner     = "planner"
	RoleBuilder     = "builder"
	RoleReviewer    = "reviewer"
	RoleModerator   = "moderator"
)

// RoleNames lists the seats in pipeline order.
var RoleNames = []string{RoleInterviewer, RolePlanner, RoleBuilder, RoleReviewer, RoleModerator}

// IsRole reports whether s names a seat.
func IsRole(s string) bool {
	for _, r := range RoleNames {
		if s == r {
			return true
		}
	}
	return false
}

// Role returns the agent filling a seat, or "" if role is not one.
//
// The point of going through this rather than reading Roles directly is that
// a seat and an agent are separate things: one agent can hold several seats,
// and role_models lets two seats that share an agent run different models.
func (c *Config) Role(role string) string {
	switch role {
	case RoleInterviewer:
		return c.Roles.Interviewer
	case RolePlanner:
		return c.Roles.Planner
	case RoleBuilder:
		return c.Roles.Builder
	case RoleReviewer:
		return c.Roles.Reviewer
	case RoleModerator:
		return c.Roles.Moderator
	}
	return ""
}

// RoleModel returns the model override for a seat, or "" when the seat should
// use its agent's own model.
func (c *Config) RoleModel(role string) string {
	if c.RoleModels == nil {
		return ""
	}
	return c.RoleModels[role]
}

// EffectiveModel is the model a seat will actually run: its override if it
// has one, otherwise its agent's model. Used by the editors for display.
func (c *Config) EffectiveModel(role string) string {
	if m := c.RoleModel(role); m != "" {
		return m
	}
	if a, ok := c.Agents[c.Role(role)]; ok {
		return a.Model
	}
	return ""
}

// wantsModelPlaceholder reports whether a command agent's args would actually
// receive {{model}}. Without it, a model override is silently ignored — which
// is exactly the kind of configuration that "looks right" and does nothing.
func wantsModelPlaceholder(args []string) bool {
	for _, a := range args {
		if strings.Contains(a, "{{model}}") {
			return true
		}
	}
	return false
}
