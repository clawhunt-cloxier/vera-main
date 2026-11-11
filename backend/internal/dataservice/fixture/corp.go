package fixture

import (
	"strconv"
	"strings"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

func (g *gen) corpEvents(s Security, p profile) {
	short := strings.TrimSuffix(s.Code, ".DEMO")
	last := g.days[len(g.days)-1]
	switch short {
	case "DM0001":
		d := g.days[len(g.days)-30]
		g.add(dataservice.Record{DatasetID: "corp.insider_trade", EntityCode: s.Code, RecordKey: s.Code + "|ANN-DM0001-INC|1",
			PublishedAt: dp(at(d, 18)), AvailableAt: at(d, 18), Origin: "company_filing", SourceTier: "L2", Payload: map[string]any{
				"announcement_id": "ANN-DM0001-INC", "seq": 1, "holder_name": "演示控股集团有限公司", "holder_role": "controlling",
				"direction": "increase", "shares": dec(p.totalShares*0.002, 0), "start_date": d.AddDate(0, 0, -20).Format("2006-01-02"),
				"end_date": d.Format("2006-01-02")}})
	case "DM0002":
		g.add(dataservice.Record{DatasetID: "corp.pledge", EntityCode: s.Code, RecordKey: s.Code + "|" + last.Format("2006-01-02"),
			Period: dp(last), AvailableAt: at(last, 18), Origin: "company_filing", SourceTier: "L2", Payload: map[string]any{
				"as_of_date": last.Format("2006-01-02"), "holder_name": "演示亏损控股有限公司", "pledged_ratio_of_holding": "0.620000",
				"pledged_ratio_of_total": "0.186000"}})
		d := g.days[len(g.days)-15]
		g.add(dataservice.Record{DatasetID: "corp.insider_trade", EntityCode: s.Code, RecordKey: s.Code + "|ANN-DM0002-DEC|1",
			PublishedAt: dp(at(d, 18)), AvailableAt: at(d, 18), Origin: "company_filing", SourceTier: "L2", Payload: map[string]any{
				"announcement_id": "ANN-DM0002-DEC", "seq": 1, "holder_name": "某持股 5% 以上股东", "holder_role": "major",
				"direction": "plan_decrease", "shares": dec(p.totalShares*0.01, 0), "start_date": last.AddDate(0, 0, 15).Format("2006-01-02"),
				"end_date": last.AddDate(0, 3, 15).Format("2006-01-02")}})
	case "DM0005":
		unlock := last.AddDate(0, 0, 45)
		g.add(dataservice.Record{DatasetID: "corp.unlock", EntityCode: s.Code, RecordKey: s.Code + "|" + unlock.Format("2006-01-02"),
			Period: dp(unlock), AvailableAt: at(g.days[len(g.days)-40], 18), Origin: "company_filing", SourceTier: "L2", Payload: map[string]any{
				"unlock_date": unlock.Format("2006-01-02"), "shares": dec(p.totalShares*0.18, 0), "holder_type": "首发原股东限售股份"}})
	}
	if f, ok := g.fin[s.Code][lastFY(g)]; ok && f.netParent > 0 {
		d := g.days[len(g.days)-50]
		g.add(dataservice.Record{DatasetID: "corp.dividend", EntityCode: s.Code, RecordKey: s.Code + "|" + lastFY(g),
			PublishedAt: dp(at(d, 18)), AvailableAt: at(d, 18), Origin: "company_filing", SourceTier: "L2", Payload: map[string]any{
				"fiscal_year": lastFY(g), "cash_per_10_shares": dec(f.netParent*0.3/p.totalShares*10, 4), "total_cash": dec(f.netParent*0.3, 2)}})
	}
}

func lastFY(g *gen) string {
	y := g.m.AsOf.Year() - 1
	if g.m.AsOf.Month() < 5 {
		y--
	}
	return strconv.Itoa(y)
}
