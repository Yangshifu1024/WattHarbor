package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	charging "chargepilot/internal/domain/charging"
	"chargepilot/internal/platform/snowflake"
	"chargepilot/internal/settlement"
	"github.com/google/uuid"
)

func demoScheme() charging.Scheme {
	return charging.Scheme{Name: demoPrefix + "完整充电方案", Remark: "开发示例，设备能力需实测核验后才能启动",
		Amount:   &charging.AmountMode{Algorithm: charging.ModeServerRealtimePower, Periods: []charging.Period{{EndMinute: 1440, Tiers: []charging.Tier{{MaxWatts: 200, ElectricCents: 80, ServiceCents: 40}, {MaxWatts: 9990, ElectricCents: 160, ServiceCents: 80}}}}},
		Energy:   &charging.EnergyMode{ElectricCents: 80, ServiceCents: 20},
		Packages: []charging.Package{{ID: 1, Name: "3元金额", Mode: "amount", PriceCents: 300}, {ID: 2, Name: "120分钟", Mode: "duration", PriceCents: 200, Minutes: 120}, {ID: 3, Name: "3度", Mode: "energy", PriceCents: 300, KWh: 3}},
		Card:     charging.CardPolicy{PackageID: 2, MaxMinutes: 600}, Display: charging.DefaultDisplay()}.Normalized()
}

func seedStationScheme(ctx context.Context, db *sql.DB, station int64) (int64, error) {
	s := demoScheme()
	if err := s.Validate(); err != nil {
		return 0, err
	}
	raw, _ := json.Marshal(s.SpecFor(s.Packages[0]))
	display, _ := json.Marshal(s.Display)
	template, err := db.ExecContext(ctx, "INSERT INTO pricing_template(name,spec_json,display_json,status,version) VALUES(?,?,?,'active',1)", s.Name, string(raw), string(display))
	if err != nil {
		return 0, err
	}
	id, _ := template.LastInsertId()
	rule, err := db.ExecContext(ctx, "INSERT INTO pricing_rule(name,station_id,template_id,spec_json,status,version,channel) VALUES(?,?,?,?,'active',1,'default')", s.Name, station, id, string(raw))
	if err != nil {
		return 0, err
	}
	return rule.LastInsertId()
}

// 这些是历史示例回执，不发送到真实设备。
// 使用已保存的完整时长方案，并关联对应的示例支付记录。
func seedOrderContract(ctx context.Context, db *sql.DB, id, user int64, u demoUser, o demoOrder, created time.Time, device string, station, ruleID int64, orderNo string) error {
	price, minutes := o.cents, uint16(60)
	if price <= 0 {
		price = 200
	}
	if o.status == "refunded" {
		price *= 2
		minutes = 120
	}
	s := charging.Scheme{Name: demoPrefix + "历史时长方案", Packages: []charging.Package{{ID: 1, Name: fmt.Sprintf("%d分钟", minutes), Mode: "duration", PriceCents: price, Minutes: minutes}}, Display: charging.DefaultDisplay()}.Normalized()
	rule := charging.Rule{ID: uint64(ruleID), StationID: uint64(station), Version: 1, Spec: s.SpecFor(s.Packages[0])}
	offer := s.Offers(rule)[0]
	snapshot, _ := json.Marshal(map[string]any{"rule": rule, "offer": offer})
	intent, merchant := uuid.NewString(), ""
	paid, refunded, status := price, int64(0), "paid"
	if o.status == "cancelled" {
		paid, status = 0, "closed"
	}
	if o.status == "refunded" {
		refunded, status = price-o.cents, "partial_refunded"
	}
	return withSeedTransaction(ctx, db, func(tx *sql.Tx) error {
		paymentNumber, err := snowflake.NextSQL(ctx, tx)
		if err != nil {
			return err
		}
		merchant = "P" + strconv.FormatUint(paymentNumber, 10)
		payment, err := tx.ExecContext(ctx, "INSERT INTO payment_order(order_no,biz_type,biz_id,user_id,pay_method,total_cents,paid_cents,refunded_cents,status,created_month) VALUES(?,'charge',?,?,'balance',?,?,?,?,?)", merchant, id, user, price, paid, refunded, status, created.Format("2006-01")+"-01")
		if err != nil {
			return err
		}
		pid, _ := payment.LastInsertId()
		port := device + ":1"
		if _, err := tx.ExecContext(ctx, "UPDATE charge_order SET payment_order_id=?,charge_mode=0,charge_quantity=?,port_code=? WHERE id=?", pid, minutes, port, id); err != nil {
			return err
		}
		// charge_payment_intent 的 electric/service/total 三列都是 NOT NULL 且无默认值，
		// 支付意图必须自带完整金额拆分。示例订单用时长方案，包费全部计入服务费，
		// 与 charge_order 一致：电费为 0，套餐价全部计入服务费。
		if _, err := tx.ExecContext(ctx, `INSERT INTO charge_payment_intent(intent_id,client_request_id,merchant_order_no,payment_order_id,user_id,openid,device_id,port_no,port_code,station_id,pricing_rule_id,pricing_rule_version,pricing_snapshot,estimated_kwh,estimated_minutes,electric_cents,service_cents,total_cents,charge_mode,charge_quantity,status,expires_at,paid_at,created_at,charge_order_id,offer_id) VALUES(?,?,?,?,?,?,?,1,?,?,?,1,?,'0.000',?,?,?,?,0,?,?,?,?,?,?,?)`, intent, uuid.NewString(), merchant, pid, user, u.openid, device, port, station, ruleID, string(snapshot), minutes, 0, price, price, minutes, intentStatus(o.status), created.Add(15*time.Minute), created, created, id, offer.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO charge_order_pricing(charge_order_id,payment_intent_id,user_id,port_code,pricing_snapshot) VALUES(?,?,?,?,?)", id, intent, user, port, string(snapshot)); err != nil {
			return err
		}
		if !orderHasEndedAt(o.status) {
			return nil
		}
		kwh, _ := strconv.ParseFloat(o.kwh, 64)
		source := settlement.Source{ChargeOrderID: uint64(id), OrderNo: orderNo, UserID: uint64(user), Rule: rule, Offer: &offer, Meter: charging.ActualMeter{StartedAt: created, EndedAt: created.Add(time.Hour), ChargedSeconds: 3600, ChargedWh: uint32(kwh * 1000)}}
		fee, err := settlement.PriceSource(source)
		if err != nil {
			return err
		}
		result := settlement.Result{CalculationNo: settlement.CalculationNumber(orderNo, uint64(id)), Source: source, ActualFee: fee}
		raw, _ := json.Marshal(result)
		_, err = tx.ExecContext(ctx, "INSERT INTO charge_fee_receipt(charge_order_id,calculation_no,result_json,shortfall_cents) VALUES(?,?,?,0)", id, result.CalculationNo, string(raw))
		return err
	})
}

func withSeedTransaction(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
