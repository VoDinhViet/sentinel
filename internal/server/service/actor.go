package service

import "context"

// Roles, from least to most privileged.
const (
	RoleViewer   = "viewer"   // read dashboards and reports
	RoleOperator = "operator" // viewer + backup now, snapshots, restore
	RoleAdmin    = "admin"    // operator + config, agents, users, audit log
)

var roleRank = map[string]int{RoleViewer: 1, RoleOperator: 2, RoleAdmin: 3}

// ValidRole reports whether role is one of the three known roles.
func ValidRole(role string) bool { _, ok := roleRank[role]; return ok }

// RoleAtLeast reports whether have grants everything need does. Unknown roles grant nothing.
func RoleAtLeast(have, need string) bool {
	return roleRank[have] > 0 && roleRank[have] >= roleRank[need]
}

// Actor is who is performing an operation. UserID 0 is the bootstrap admin
// from server.yaml. The zero Actor means "nobody" (system or agent).
type Actor struct {
	UserID   int64
	Username string
	Role     string
}

type actorKey struct{}

// WithActor returns a context carrying the acting user, which Enqueue and
// SaveConfig record as the requester or author.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom returns the acting user, or the zero Actor.
func ActorFrom(ctx context.Context) Actor {
	a, _ := ctx.Value(actorKey{}).(Actor)
	return a
}
