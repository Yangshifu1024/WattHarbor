package dbutil

import (
	"errors"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

func TestMonthStartIsFirstDayOfCurrentUTCMonth(t *testing.T) {
	got := MonthStart()
	if got.Day() != 1 {
		t.Fatalf("created_month anchor day = %d, want 1 (writing the write day breaks uk_order_no)", got.Day())
	}
	if h, m, s := got.Clock(); h != 0 || m != 0 || s != 0 {
		t.Fatalf("anchor clock = %02d:%02d:%02d, want 00:00:00", h, m, s)
	}
	if got.Location() != time.UTC {
		t.Fatalf("anchor location = %v, want UTC", got.Location())
	}
	now := time.Now().UTC()
	if got.Year() != now.Year() || got.Month() != now.Month() {
		t.Fatalf("anchor = %v, want %04d-%02d", got, now.Year(), now.Month())
	}
}

// 同月取值应一致，避免唯一键中的 created_month 随写入日期变化。
func TestMonthStartIsStableWithinAMonth(t *testing.T) {
	first := MonthStart()
	second := MonthStart()
	if !first.Equal(second) {
		t.Fatalf("anchor drifted between calls: %v != %v", first, second)
	}
}

func TestMonthStartAtUsesUTCMonthAcrossTimeZones(t *testing.T) {
	for _, tc := range []struct {
		name string
		at   string
		want string
	}{
		{name: "utc_month_first", at: "2026-10-01T00:00:00Z", want: "2026-10-01T00:00:00Z"},
		{name: "utc_month_last", at: "2026-10-31T23:59:59.999999999Z", want: "2026-10-01T00:00:00Z"},
		{name: "east_local_next_month", at: "2026-11-01T00:01:00+08:00", want: "2026-10-01T00:00:00Z"},
		{name: "west_local_previous_month", at: "2026-10-31T23:59:59-07:00", want: "2026-11-01T00:00:00Z"},
		{name: "east_local_next_year", at: "2027-01-01T00:00:00+08:00", want: "2026-12-01T00:00:00Z"},
		{name: "west_local_previous_year", at: "2026-12-31T23:59:59-05:00", want: "2027-01-01T00:00:00Z"},
		{name: "leap_day", at: "2028-02-29T12:00:00Z", want: "2028-02-01T00:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at, err := time.Parse(time.RFC3339Nano, tc.at)
			if err != nil {
				t.Fatal(err)
			}
			got := MonthStartAt(at)
			if got.Format(time.RFC3339Nano) != tc.want || got.Location() != time.UTC {
				t.Fatalf("MonthStartAt(%v)=%v want=%s UTC", at, got, tc.want)
			}
		})
	}
}

// 月分区包含月初，不包含次月月初。
func TestMonthStartLandsOnPartitionBoundary(t *testing.T) {
	monthStart := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	nextMonth := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if !monthStart.Before(nextMonth) {
		t.Fatal("month start is not before the next month start")
	}
	// 同月任意一天都必须落在 [月首, 次月首) 区间，从而归入同一个分区。
	for day := 1; day <= 31; day++ {
		at := time.Date(2026, 10, day, 12, 0, 0, 0, time.UTC)
		if at.Before(monthStart) || !at.Before(nextMonth) {
			t.Fatalf("2026-10-%02d escapes its month partition", day)
		}
	}
}

func TestIsMySQLDuplicate(t *testing.T) {
	if IsMySQLDuplicate(nil) {
		t.Fatal("nil error reported as duplicate")
	}
	if IsMySQLDuplicate(errors.New("connection reset")) {
		t.Fatal("unrelated error reported as duplicate")
	}
	if !IsMySQLDuplicate(gorm.ErrDuplicatedKey) {
		t.Fatal("gorm.ErrDuplicatedKey not recognised")
	}
	if !IsMySQLDuplicate(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"}) {
		t.Fatal("raw 1062 not recognised")
	}
	if IsMySQLDuplicate(&mysql.MySQLError{Number: 1146, Message: "Table doesn't exist"}) {
		t.Fatal("non-duplicate MySQL error reported as duplicate")
	}
	// 必须穿透包装，否则 fmt.Errorf("%w") 之后的错误会漏判。
	wrapped := errors.Join(errors.New("settle refund"), &mysql.MySQLError{Number: 1062})
	if !IsMySQLDuplicate(wrapped) {
		t.Fatal("wrapped 1062 not recognised")
	}
}
