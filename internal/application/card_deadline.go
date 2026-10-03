package application

import (
	"context"
	database "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	"time"
)

// freezeCardDeadline 委托 database.FreezeDeadline；卡时长截止的冻结语义归属 card 家族。
func (s AutoStopper) freezeCardDeadline(ctx context.Context, id uint64, now time.Time) (bool, error) {
	return database.FreezeDeadline(ctx, s.UserDB, id, now)
}
